package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mobile-agy/internal/auth"
	"mobile-agy/internal/chat"
	"mobile-agy/internal/handler"
	"mobile-agy/internal/terminal"
	"mobile-agy/internal/workspace"
)

// Helper function to setup services & handler for testing
func setupTestFixture(t *testing.T) (*workspace.Service, *auth.Service, *chat.Service, *terminal.Service, *handler.Handler, string) {
	tempWS, err := os.MkdirTemp("", "test_ws_*")
	if err != nil {
		t.Fatalf("failed to create temp workspace: %v", err)
	}

	workspaceSvc := workspace.NewService(tempWS)
	authSvc := auth.NewService(tempWS)
	authSvc.SetBypassDynamicAuthCheck(true)

	chatSvc := chat.NewService()
	terminalSvc := terminal.NewService()

	h := handler.NewHandler(workspaceSvc, authSvc, chatSvc, terminalSvc, handler.EmbeddedHTML{
		IndexHTML:    "<html>index</html>",
		LoginHTML:    "<html>login</html>",
		LoginPwdHTML: "<html>login pwd</html>",
	})

	return workspaceSvc, authSvc, chatSvc, terminalSvc, h, tempWS
}

func TestGenerateRandomPassword(t *testing.T) {
	_, authSvc, _, _, _, tempDir := setupTestFixture(t)
	defer os.RemoveAll(tempDir)

	lengths := []int{8, 16, 32}
	for _, l := range lengths {
		pass := authSvc.GenerateRandomPassword(l)
		if len(pass) != l {
			t.Errorf("expected length %d, got %d", l, len(pass))
		}
	}
}

func TestCheckOAuthTokenExists(t *testing.T) {
	_, authSvc, _, _, _, tempDir := setupTestFixture(t)
	defer os.RemoveAll(tempDir)

	homeDir, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("failed to get home dir: %v", err)
	}

	tokenDir := filepath.Join(homeDir, ".gemini", "antigravity-cli")
	tokenPath := filepath.Join(tokenDir, "antigravity-oauth-token")

	// Save original token status
	originalExists := authSvc.CheckOAuthTokenExists()

	// Backup original token if it exists
	backupPath := tokenPath + ".test_bak"
	if originalExists {
		err := os.Rename(tokenPath, backupPath)
		if err != nil {
			t.Fatalf("failed to backup token: %v", err)
		}
		defer func() {
			os.Rename(backupPath, tokenPath)
		}()
	}

	// 1. Check when token is missing
	if authSvc.CheckOAuthTokenExists() {
		t.Errorf("expected CheckOAuthTokenExists to be false, got true")
	}

	// Create dummy token
	err = os.MkdirAll(tokenDir, 0755)
	if err != nil {
		t.Fatalf("failed to create token dir: %v", err)
	}
	err = os.WriteFile(tokenPath, []byte("dummy-token"), 0600)
	if err != nil {
		t.Fatalf("failed to create dummy token: %v", err)
	}
	defer os.Remove(tokenPath)

	// 2. Check when token is present
	if !authSvc.CheckOAuthTokenExists() {
		t.Errorf("expected CheckOAuthTokenExists to be true, got false")
	}
}

func TestHandleAuthStatus(t *testing.T) {
	_, authSvc, _, _, h, tempDir := setupTestFixture(t)
	defer os.RemoveAll(tempDir)

	// Bypass password check for this test by ensuring no password is set
	os.Setenv("PASSWORD", "")
	authSvc.LoadPassword()
	sessionToken := authSvc.InitSession()

	// Set up request and recorder
	req := httptest.NewRequest(http.MethodGet, "/api/auth/status", nil)
	req.AddCookie(&http.Cookie{Name: "session_password", Value: sessionToken})
	rr := httptest.NewRecorder()

	// Run handler through middleware
	handlerFunc := h.AuthMiddleware(h.HandleAuthStatus)
	handlerFunc.ServeHTTP(rr, req)

	// Verify response status
	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rr.Code)
	}

	// Verify JSON response structure
	var resp map[string]any
	err := json.NewDecoder(rr.Body).Decode(&resp)
	if err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	authVal, ok := resp["authenticated"]
	if !ok {
		t.Errorf("expected 'authenticated' key in response")
	}
	if _, ok := authVal.(bool); !ok {
		t.Errorf("expected 'authenticated' to be bool, got %T", authVal)
	}

	versionVal, ok := resp["version"]
	if !ok {
		t.Errorf("expected 'version' key in response")
	}
	if versionVal != "v1.3.0" {
		t.Errorf("expected version to be 'v1.3.0', got %v", versionVal)
	}
}

func TestHandleQuotaSummaryUnauthorized(t *testing.T) {
	_, authSvc, _, _, h, tempDir := setupTestFixture(t)
	defer os.RemoveAll(tempDir)

	os.Setenv("PASSWORD", "")
	authSvc.LoadPassword()
	sessionToken := authSvc.InitSession()

	// Temporarily simulate missing token
	homeDir, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("failed to get home dir: %v", err)
	}
	tokenPath := filepath.Join(homeDir, ".gemini", "antigravity-cli", "antigravity-oauth-token")

	originalExists := authSvc.CheckOAuthTokenExists()
	backupPath := tokenPath + ".test_bak"
	if originalExists {
		err := os.Rename(tokenPath, backupPath)
		if err != nil {
			t.Fatalf("failed to backup token: %v", err)
		}
		defer func() {
			_ = os.Rename(backupPath, tokenPath)
		}()
	}

	req := httptest.NewRequest(http.MethodGet, "/api/quota", nil)
	req.AddCookie(&http.Cookie{Name: "session_password", Value: sessionToken})
	rr := httptest.NewRecorder()

	handlerFunc := h.AuthMiddleware(h.HandleQuotaSummary)
	handlerFunc.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected status 401, got %d", rr.Code)
	}
}

func TestHandleListFilesUnauthorized(t *testing.T) {
	_, authSvc, _, _, h, tempDir := setupTestFixture(t)
	defer os.RemoveAll(tempDir)

	sessionToken := authSvc.InitSession()

	// Temporarily simulate missing token
	homeDir, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("failed to get home dir: %v", err)
	}
	tokenPath := filepath.Join(homeDir, ".gemini", "antigravity-cli", "antigravity-oauth-token")

	originalExists := authSvc.CheckOAuthTokenExists()
	backupPath := tokenPath + ".test_bak"
	if originalExists {
		err := os.Rename(tokenPath, backupPath)
		if err != nil {
			t.Fatalf("failed to backup token: %v", err)
		}
		defer func() {
			os.Rename(backupPath, tokenPath)
		}()
	}

	// Set up request with password session token but NO Google token
	req := httptest.NewRequest(http.MethodGet, "/api/files", nil)
	req.AddCookie(&http.Cookie{Name: "session_password", Value: sessionToken})
	rr := httptest.NewRecorder()

	// Run handler through middleware
	handlerFunc := h.AuthMiddleware(h.HandleListFiles)
	handlerFunc.ServeHTTP(rr, req)

	// Verify response status is 401
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected status 401, got %d", rr.Code)
	}
}

func TestHandlePasswordAuth(t *testing.T) {
	// Set mock password in env
	os.Setenv("PASSWORD", "supersecretpassword123")
	defer os.Unsetenv("PASSWORD")

	_, authSvc, _, _, h, tempDir := setupTestFixture(t)
	defer os.RemoveAll(tempDir)

	// Reload password in authSvc
	authSvc.LoadPassword()

	// 1. Test Incorrect Password
	reqIncorrect := httptest.NewRequest(http.MethodPost, "/api/auth/pwd", strings.NewReader("password=wrongpassword"))
	reqIncorrect.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rrIncorrect := httptest.NewRecorder()

	h.HandlePasswordAuth(rrIncorrect, reqIncorrect)

	if rrIncorrect.Code != http.StatusUnauthorized {
		t.Errorf("expected status 401 for wrong password, got %d", rrIncorrect.Code)
	}

	// 2. Test Correct Password
	reqCorrect := httptest.NewRequest(http.MethodPost, "/api/auth/pwd", strings.NewReader("password=supersecretpassword123"))
	reqCorrect.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rrCorrect := httptest.NewRecorder()

	h.HandlePasswordAuth(rrCorrect, reqCorrect)

	if rrCorrect.Code != http.StatusOK {
		t.Errorf("expected status 200 for correct password, got %d", rrCorrect.Code)
	}

	// Check if session cookie is set
	cookies := rrCorrect.Result().Cookies()
	var sessionCookie *http.Cookie
	for _, c := range cookies {
		if c.Name == "session_password" {
			sessionCookie = c
			break
		}
	}

	if sessionCookie == nil {
		t.Fatalf("expected session_password cookie to be set")
	}

	if sessionCookie.Value != authSvc.SessionToken() {
		t.Errorf("expected cookie value to match session token, got %s", sessionCookie.Value)
	}
}

func TestHandleChatHistoryList(t *testing.T) {
	workspaceSvc, authSvc, _, _, h, tempDir := setupTestFixture(t)
	defer os.RemoveAll(tempDir)

	sessionToken := authSvc.InitSession()

	// Set active workspace to match mock history workspace
	originalWorkspace := workspaceSvc.ActiveWorkspaceDir()
	
	// Create mock workspace directory
	mockWSDir := filepath.Join(tempDir, "workspace")
	err := os.MkdirAll(mockWSDir, 0755)
	if err != nil {
		t.Fatalf("failed to create mock workspace: %v", err)
	}
	err = workspaceSvc.Select(mockWSDir)
	if err != nil {
		t.Fatalf("failed to select mock workspace: %v", err)
	}
	defer func() { _ = workspaceSvc.Select(originalWorkspace) }()

	activeWS := workspaceSvc.ActiveWorkspaceDir()

	// Mock HOME env
	originalHome := os.Getenv("HOME")
	tempHome, err := os.MkdirTemp("", "test_home_*")
	if err != nil {
		t.Fatalf("failed to create temp home: %v", err)
	}
	defer os.RemoveAll(tempHome)

	os.Setenv("HOME", tempHome)
	defer os.Setenv("HOME", originalHome)

	// Write mock history.jsonl
	historyDir := filepath.Join(tempHome, ".gemini", "antigravity-cli")
	err = os.MkdirAll(historyDir, 0755)
	if err != nil {
		t.Fatalf("failed to create history dir: %v", err)
	}

	// Write dummy Google token to pass Layer 2 Google Auth check
	err = os.WriteFile(filepath.Join(historyDir, "antigravity-oauth-token"), []byte("dummy"), 0600)
	if err != nil {
		t.Fatalf("failed to write dummy token: %v", err)
	}

	mockData := `{"display":"Test Prompt 1","timestamp":1000,"workspace":"` + activeWS + `","conversationId":"conv-1"}
{"display":"Test Prompt 2","timestamp":2000,"workspace":"` + activeWS + `","conversationId":"conv-2"}
{"display":"Test Prompt 3","timestamp":3000,"workspace":"` + activeWS + `","conversationId":"conv-1"}
`
	err = os.WriteFile(filepath.Join(historyDir, "history.jsonl"), []byte(mockData), 0644)
	if err != nil {
		t.Fatalf("failed to write mock history file: %v", err)
	}

	// Request
	req := httptest.NewRequest(http.MethodGet, "/api/chat/history", nil)
	req.AddCookie(&http.Cookie{Name: "session_password", Value: sessionToken})
	rr := httptest.NewRecorder()

	handlerFunc := h.AuthMiddleware(h.HandleChatHistoryList)
	handlerFunc.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d, body: %s", rr.Code, rr.Body.String())
	}

	var list []chat.ChatHistoryItem
	err = json.NewDecoder(rr.Body).Decode(&list)
	if err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	// Should have 2 unique items
	if len(list) != 2 {
		t.Errorf("expected 2 history items, got %d", len(list))
	}

	// Check sorting (descending by timestamp)
	// conv-1 should have latest 3000, conv-2 has 2000, so conv-1 is first
	if list[0].ID != "conv-1" || list[1].ID != "conv-2" {
		t.Errorf("incorrect sorting or grouping: %+v", list)
	}

	if list[0].Title != "Test Prompt 1" {
		t.Errorf("expected title to be 'Test Prompt 1' (earliest prompt), got '%s'", list[0].Title)
	}
}

func TestHandleChatHistoryDetail(t *testing.T) {
	_, authSvc, _, _, h, tempDir := setupTestFixture(t)
	defer os.RemoveAll(tempDir)

	sessionToken := authSvc.InitSession()

	// Mock HOME env
	originalHome := os.Getenv("HOME")
	tempHome, err := os.MkdirTemp("", "test_home_*")
	if err != nil {
		t.Fatalf("failed to create temp home: %v", err)
	}
	defer os.RemoveAll(tempHome)

	os.Setenv("HOME", tempHome)
	defer os.Setenv("HOME", originalHome)

	// Write mock transcript.jsonl
	convID := "conv-123"
	transcriptDir := filepath.Join(tempHome, ".gemini", "antigravity-cli", "brain", convID, ".system_generated", "logs")
	err = os.MkdirAll(transcriptDir, 0755)
	if err != nil {
		t.Fatalf("failed to create transcript dir: %v", err)
	}

	// Write dummy Google token to pass Layer 2 Google Auth check
	historyDir := filepath.Join(tempHome, ".gemini", "antigravity-cli")
	err = os.MkdirAll(historyDir, 0755)
	if err != nil {
		t.Fatalf("failed to create history dir: %v", err)
	}
	err = os.WriteFile(filepath.Join(historyDir, "antigravity-oauth-token"), []byte("dummy"), 0600)
	if err != nil {
		t.Fatalf("failed to write dummy token: %v", err)
	}

	mockTranscript := `{"step_index":1,"source":"USER","type":"USER_INPUT","status":"DONE","created_at":"2026-07-05T03:00:00Z","content":"hello assistant"}
{"step_index":2,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-07-05T03:00:05Z","content":"hello human"}
`
	err = os.WriteFile(filepath.Join(transcriptDir, "transcript.jsonl"), []byte(mockTranscript), 0644)
	if err != nil {
		t.Fatalf("failed to write mock transcript: %v", err)
	}

	// Request
	req := httptest.NewRequest(http.MethodGet, "/api/chat/history/detail?id="+convID, nil)
	req.AddCookie(&http.Cookie{Name: "session_password", Value: sessionToken})
	rr := httptest.NewRecorder()

	handlerFunc := h.AuthMiddleware(h.HandleChatHistoryDetail)
	handlerFunc.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rr.Code)
	}

	var detail chat.ChatHistoryDetail
	err = json.NewDecoder(rr.Body).Decode(&detail)
	if err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if detail.ID != convID {
		t.Errorf("expected ID %s, got %s", convID, detail.ID)
	}

	if len(detail.Messages) != 2 {
		t.Errorf("expected 2 messages, got %d", len(detail.Messages))
	}

	if detail.Messages[0].Role != "user" || detail.Messages[0].Content != "hello assistant" {
		t.Errorf("incorrect message 0 format: %+v", detail.Messages[0])
	}

	if detail.Messages[1].Role != "model" || detail.Messages[1].Content != "hello human" {
		t.Errorf("incorrect message 1 format: %+v", detail.Messages[1])
	}
}

func TestHandlePreviewFile(t *testing.T) {
	workspaceSvc, authSvc, _, _, h, tempDir := setupTestFixture(t)
	defer os.RemoveAll(tempDir)

	sessionToken := authSvc.InitSession()

	// Mock HOME env
	originalHome := os.Getenv("HOME")
	tempHome, err := os.MkdirTemp("", "test_home_*")
	if err != nil {
		t.Fatalf("failed to create temp home: %v", err)
	}
	defer os.RemoveAll(tempHome)

	os.Setenv("HOME", tempHome)
	defer os.Setenv("HOME", originalHome)

	// Write dummy Google token to pass Layer 2 Google Auth check
	historyDir := filepath.Join(tempHome, ".gemini", "antigravity-cli")
	err = os.MkdirAll(historyDir, 0755)
	if err != nil {
		t.Fatalf("failed to create history dir: %v", err)
	}
	err = os.WriteFile(filepath.Join(historyDir, "antigravity-oauth-token"), []byte("dummy"), 0600)
	if err != nil {
		t.Fatalf("failed to write dummy token: %v", err)
	}

	// Write a mock test file in workspace
	testFile := "preview.html"
	testContent := "<html>Hello Preview</html>"
	err = workspaceSvc.WriteFile(testFile, []byte(testContent))
	if err != nil {
		t.Fatalf("failed to write mock file: %v", err)
	}

	// Request for /preview/preview.html
	req := httptest.NewRequest(http.MethodGet, "/preview/preview.html", nil)
	req.AddCookie(&http.Cookie{Name: "session_password", Value: sessionToken})
	rr := httptest.NewRecorder()

	handlerFunc := h.AuthMiddleware(h.HandlePreviewFile)
	handlerFunc.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rr.Code)
	}

	if rr.Body.String() != testContent {
		t.Errorf("expected body %s, got %s", testContent, rr.Body.String())
	}
}

func TestHandleSelfUpdate(t *testing.T) {
	_, authSvc, _, _, h, tempDir := setupTestFixture(t)
	defer os.RemoveAll(tempDir)

	sessionToken := authSvc.InitSession()

	// Mock HOME env to bypass Layer 2 Google Auth
	originalHome := os.Getenv("HOME")
	tempHome, err := os.MkdirTemp("", "test_home_*")
	if err != nil {
		t.Fatalf("failed to create temp home: %v", err)
	}
	defer os.RemoveAll(tempHome)

	os.Setenv("HOME", tempHome)
	defer os.Setenv("HOME", originalHome)

	// Write dummy Google token to pass Layer 2 Google Auth check
	historyDir := filepath.Join(tempHome, ".gemini", "antigravity-cli")
	err = os.MkdirAll(historyDir, 0755)
	if err != nil {
		t.Fatalf("failed to create history dir: %v", err)
	}
	err = os.WriteFile(filepath.Join(historyDir, "antigravity-oauth-token"), []byte("dummy"), 0600)
	if err != nil {
		t.Fatalf("failed to write dummy token: %v", err)
	}

	// 1. Test GET not allowed
	reqGet := httptest.NewRequest(http.MethodGet, "/api/update", nil)
	reqGet.AddCookie(&http.Cookie{Name: "session_password", Value: sessionToken})
	rrGet := httptest.NewRecorder()

	handlerFunc := h.AuthMiddleware(h.HandleSelfUpdate)
	handlerFunc.ServeHTTP(rrGet, reqGet)

	if rrGet.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected status 405, got %d", rrGet.Code)
	}

	// 2. Test POST successful triggering
	reqPost := httptest.NewRequest(http.MethodPost, "/api/update", nil)
	reqPost.AddCookie(&http.Cookie{Name: "session_password", Value: sessionToken})
	rrPost := httptest.NewRecorder()

	handlerFunc.ServeHTTP(rrPost, reqPost)

	if rrPost.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rrPost.Code)
	}

	if !strings.Contains(rrPost.Body.String(), "Pembaruan dimulai") {
		t.Errorf("expected response to indicate update started, got: %s", rrPost.Body.String())
	}
}
