package handler

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mobile-agy/internal/auth"
	"mobile-agy/internal/chat"
	"mobile-agy/internal/telegram"
	"mobile-agy/internal/terminal"
	"mobile-agy/internal/workspace"
)

func setupOpenAITestFixture(t *testing.T) (*workspace.Service, *auth.Service, *chat.Service, *terminal.Service, *Handler, string) {
	tempWS, err := os.MkdirTemp("", "test_openai_ws_*")
	if err != nil {
		t.Fatalf("failed to create temp workspace: %v", err)
	}

	auth.HomeDirOverride = tempWS
	workspaceSvc := workspace.NewService(tempWS)
	authSvc := auth.NewService(tempWS)
	authSvc.LoadPassword()

	chatSvc := chat.NewService()
	terminalSvc := terminal.NewService()
	telegramSvc := telegram.NewService(chatSvc, workspaceSvc)

	h := NewHandler(workspaceSvc, authSvc, chatSvc, terminalSvc, telegramSvc, EmbeddedHTML{})
	return workspaceSvc, authSvc, chatSvc, terminalSvc, h, tempWS
}

func TestHandleV1Models(t *testing.T) {
	_, authSvc, _, _, h, tempWS := setupOpenAITestFixture(t)
	defer os.RemoveAll(tempWS)

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	pwd := authSvc.GetPassword()
	if pwd != "" {
		req.Header.Set("Authorization", "Bearer "+pwd)
	}
	rr := httptest.NewRecorder()

	handlerFunc := h.AuthMiddleware(h.HandleV1Models)
	handlerFunc.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d, body: %s", rr.Code, rr.Body.String())
	}

	var resp OpenAIModelsResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp.Object != "list" {
		t.Errorf("expected object to be 'list', got %s", resp.Object)
	}

	if len(resp.Data) == 0 {
		t.Errorf("expected models data list to not be empty")
	}

	foundGemini := false
	for _, m := range resp.Data {
		if m.Object != "model" {
			t.Errorf("expected item object to be 'model', got %s", m.Object)
		}
		if strings.Contains(m.ID, "gemini") {
			foundGemini = true
		}
	}

	if !foundGemini {
		t.Errorf("expected gemini model in models list")
	}
}

func TestSaveBase64OrURLFile(t *testing.T) {
	tempWS, err := os.MkdirTemp("", "test_save_b64_*")
	if err != nil {
		t.Fatalf("failed to create temp ws: %v", err)
	}
	defer os.RemoveAll(tempWS)

	sampleText := "Hello, OpenAI Multimodal Test File!"
	b64Text := base64.StdEncoding.EncodeToString([]byte(sampleText))
	dataURI := fmt.Sprintf("data:text/plain;base64,%s", b64Text)

	relPath, absPath, err := saveBase64OrURLFile(tempWS, dataURI, "file")
	if err != nil {
		t.Fatalf("saveBase64OrURLFile failed: %v", err)
	}

	if absPath == "" || relPath == "" {
		t.Fatalf("expected non-empty paths, got rel: %s, abs: %s", relPath, absPath)
	}

	if !strings.HasPrefix(relPath, "scratch/openai_files/") {
		t.Errorf("expected relative path to start with scratch/openai_files/, got %s", relPath)
	}

	content, err := os.ReadFile(absPath)
	if err != nil {
		t.Fatalf("failed to read saved file: %v", err)
	}

	if string(content) != sampleText {
		t.Errorf("expected content %q, got %q", sampleText, string(content))
	}
}

func TestBuildOpenAIPromptWithMultimodal(t *testing.T) {
	tempWS, err := os.MkdirTemp("", "test_prompt_b64_*")
	if err != nil {
		t.Fatalf("failed to create temp ws: %v", err)
	}
	defer os.RemoveAll(tempWS)

	h := &Handler{}
	dummyImageB64 := base64.StdEncoding.EncodeToString([]byte("fake-png-bytes"))
	dataURI := "data:image/png;base64," + dummyImageB64

	reqMsg := OpenAIChatMessage{
		Role: "user",
		Content: json.RawMessage(fmt.Sprintf(`[
			{"type": "text", "text": "Tolong analisa gambar ini"},
			{"type": "image_url", "image_url": {"url": "%s"}}
		]`, dataURI)),
	}

	prompt := h.buildOpenAIPrompt([]OpenAIChatMessage{reqMsg}, tempWS)

	if !strings.Contains(prompt, "Tolong analisa gambar ini") {
		t.Errorf("expected prompt to contain text prompt, got: %s", prompt)
	}

	if !strings.Contains(prompt, "[Attached Image/File: scratch/openai_files/file_") {
		t.Errorf("expected prompt to contain saved image reference tag, got: %s", prompt)
	}

	// Verify file was physically saved in scratch/openai_files/
	files, _ := os.ReadDir(filepath.Join(tempWS, "scratch", "openai_files"))
	if len(files) != 1 {
		t.Errorf("expected 1 file in scratch/openai_files, found %d", len(files))
	}
}

func TestHandleV1ChatCompletionsAuthBearer(t *testing.T) {
	_, authSvc, _, _, h, tempWS := setupOpenAITestFixture(t)
	defer os.RemoveAll(tempWS)

	os.Setenv("PASSWORD", "mysecretkey123")
	authSvc.LoadPassword()

	reqBody := `{"model":"gemini-3.5-flash-high","messages":[{"role":"user","content":"Hello"}],"stream":false}`

	// 1. Test without auth -> 401 Unauthorized
	reqUnauthorized := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(reqBody))
	reqUnauthorized.Header.Set("Content-Type", "application/json")
	rrUnauthorized := httptest.NewRecorder()

	h.AuthMiddleware(h.HandleV1ChatCompletions).ServeHTTP(rrUnauthorized, reqUnauthorized)
	if rrUnauthorized.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 Unauthorized without token, got %d", rrUnauthorized.Code)
	}

	// 2. Test with correct Password Bearer token in Authorization header
	reqAuthorized := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(reqBody))
	reqAuthorized.Header.Set("Content-Type", "application/json")
	reqAuthorized.Header.Set("Authorization", "Bearer mysecretkey123")
	rrAuthorized := httptest.NewRecorder()

	h.AuthMiddleware(h.HandleV1ChatCompletions).ServeHTTP(rrAuthorized, reqAuthorized)
	if rrAuthorized.Code != http.StatusOK {
		t.Errorf("expected 200 OK with valid Bearer password, got %d, body: %s", rrAuthorized.Code, rrAuthorized.Body.String())
	}

	// 3. Test with wrapper API key (sk-agy-...) Bearer token
	wrapperKey := authSvc.GetAPIKey()
	if !strings.HasPrefix(wrapperKey, "sk-agy-") {
		t.Errorf("expected wrapper key prefix 'sk-agy-', got: %s", wrapperKey)
	}

	reqWrapperAuth := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(reqBody))
	reqWrapperAuth.Header.Set("Content-Type", "application/json")
	reqWrapperAuth.Header.Set("Authorization", "Bearer "+wrapperKey)
	rrWrapperAuth := httptest.NewRecorder()

	h.AuthMiddleware(h.HandleV1ChatCompletions).ServeHTTP(rrWrapperAuth, reqWrapperAuth)
	if rrWrapperAuth.Code != http.StatusOK {
		t.Errorf("expected 200 OK with valid wrapper API key Bearer token, got %d, body: %s", rrWrapperAuth.Code, rrWrapperAuth.Body.String())
	}
}

func TestWrapperAPIKeyEndpoints(t *testing.T) {
	_, authSvc, _, _, h, tempWS := setupOpenAITestFixture(t)
	defer os.RemoveAll(tempWS)

	sessionToken := authSvc.InitSession()

	// 1. GET /api/auth/wrapper-key
	reqGet := httptest.NewRequest(http.MethodGet, "/api/auth/wrapper-key", nil)
	reqGet.AddCookie(&http.Cookie{Name: "session_password", Value: sessionToken})
	rrGet := httptest.NewRecorder()

	h.AuthMiddleware(h.HandleGetWrapperAPIKey).ServeHTTP(rrGet, reqGet)
	if rrGet.Code != http.StatusOK {
		t.Fatalf("expected 200 OK on GET wrapper-key, got %d, body: %s", rrGet.Code, rrGet.Body.String())
	}

	var getRes map[string]string
	if err := json.NewDecoder(rrGet.Body).Decode(&getRes); err != nil {
		t.Fatalf("failed to decode wrapper key response: %v", err)
	}

	initialKey := getRes["apiKey"]
	if !strings.HasPrefix(initialKey, "sk-agy-") {
		t.Errorf("expected apiKey prefix 'sk-agy-', got %s", initialKey)
	}

	// 2. POST /api/auth/wrapper-key/regenerate
	pwdBefore := authSvc.GetPassword()

	reqRegen := httptest.NewRequest(http.MethodPost, "/api/auth/wrapper-key/regenerate", nil)
	reqRegen.AddCookie(&http.Cookie{Name: "session_password", Value: sessionToken})
	rrRegen := httptest.NewRecorder()

	h.AuthMiddleware(h.HandleRegenerateWrapperAPIKey).ServeHTTP(rrRegen, reqRegen)
	if rrRegen.Code != http.StatusOK {
		t.Fatalf("expected 200 OK on POST regenerate wrapper-key, got %d, body: %s", rrRegen.Code, rrRegen.Body.String())
	}

	var regenRes map[string]string
	if err := json.NewDecoder(rrRegen.Body).Decode(&regenRes); err != nil {
		t.Fatalf("failed to decode regenerate response: %v", err)
	}

	newKey := regenRes["apiKey"]
	if !strings.HasPrefix(newKey, "sk-agy-") {
		t.Errorf("expected new key prefix 'sk-agy-', got %s", newKey)
	}

	if newKey == initialKey {
		t.Errorf("expected new key to be different from initial key")
	}

	// 3. Verify password.txt remains unchanged
	pwdAfter := authSvc.GetPassword()
	if pwdBefore != pwdAfter {
		t.Errorf("expected password to remain unchanged (%s), got %s", pwdBefore, pwdAfter)
	}

	// 4. Verify api_key.txt file content
	keyFileContent, err := os.ReadFile(filepath.Join(tempWS, "api_key.txt"))
	if err != nil {
		t.Fatalf("failed to read api_key.txt: %v", err)
	}
	if strings.TrimSpace(string(keyFileContent)) != newKey {
		t.Errorf("expected api_key.txt content to match new key %s, got %s", newKey, string(keyFileContent))
	}
}

func TestPureLLMModeHeaderToggle(t *testing.T) {
	reqDefault := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	isAgentModeDefault := strings.EqualFold(strings.TrimSpace(reqDefault.Header.Get("X-AGY-Agent-Mode")), "true")
	if isAgentModeDefault {
		t.Errorf("expected default agent mode to be false")
	}

	reqAgent := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	reqAgent.Header.Set("X-AGY-Agent-Mode", "true")
	isAgentModeAgent := strings.EqualFold(strings.TrimSpace(reqAgent.Header.Get("X-AGY-Agent-Mode")), "true")
	if !isAgentModeAgent {
		t.Errorf("expected X-AGY-Agent-Mode: true to activate agent mode")
	}
}
