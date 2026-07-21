package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"mobile-agy/internal/auth"
	"mobile-agy/internal/chat"
	"mobile-agy/internal/handler"
	"mobile-agy/internal/terminal"
	"mobile-agy/internal/workspace"

	"github.com/zalando/go-keyring"
)

// Helper function to setup services & handler for testing
func setupTestFixture(t *testing.T) (*workspace.Service, *auth.Service, *chat.Service, *terminal.Service, *handler.Handler, string) {
	tempWS, err := os.MkdirTemp("", "test_ws_*")
	if err != nil {
		t.Fatalf("failed to create temp workspace: %v", err)
	}

	auth.HomeDirOverride = tempWS
	workspaceSvc := workspace.NewService(tempWS)
	authSvc := auth.NewService(tempWS)
	authSvc.LoadPassword()
	authSvc.SetBypassDynamicAuthCheck(false)

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
	authSvc.SetBypassDynamicAuthCheck(false)

	homeDir := auth.HomeDirOverride
	if homeDir == "" {
		var err error
		homeDir, err = os.UserHomeDir()
		if err != nil {
			t.Fatalf("failed to get home dir: %v", err)
		}
	}

	tokenDir := filepath.Join(homeDir, ".gemini", "antigravity-cli")
	tokenPath := filepath.Join(tokenDir, "antigravity-oauth-token")

	// Backup original token if it exists
	backupPath := tokenPath + ".test_bak"
	if _, err := os.Stat(tokenPath); err == nil {
		_ = os.Rename(tokenPath, backupPath)
		defer func() {
			_ = os.Rename(backupPath, tokenPath)
		}()
	}

	// 1. Check when token is missing
	if authSvc.CheckOAuthTokenExists() {
		t.Errorf("expected CheckOAuthTokenExists to be false, got true")
	}

	// Create dummy token
	err := os.MkdirAll(tokenDir, 0755)
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

func TestAutoRestoreAccountFromPool(t *testing.T) {
	_, authSvc, _, _, _, tempDir := setupTestFixture(t)
	defer os.RemoveAll(tempDir)
	authSvc.SetBypassDynamicAuthCheck(false)

	mockVal := `{"token":{"access_token":"mock_access_token_123"}}`
	pool := []auth.AccountEntry{
		{
			Email:        "user@example.com",
			KeyringValue: mockVal,
		},
	}
	err := authSvc.SaveAccountsPool(pool)
	if err != nil {
		t.Fatalf("failed to save accounts pool: %v", err)
	}

	homeDir := auth.HomeDirOverride
	tokenPath := filepath.Join(homeDir, ".gemini", "antigravity-cli", "antigravity-oauth-token")
	_ = os.Remove(tokenPath)

	if !authSvc.CheckOAuthTokenExists() {
		t.Errorf("expected CheckOAuthTokenExists to return true when account is available in pool")
	}

	if fi, err := os.Stat(tokenPath); err != nil || fi.Size() == 0 {
		t.Errorf("expected token file to be restored from pool, but was not found or 0 bytes")
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
	if versionVal != handler.AppVersion {
		t.Errorf("expected version to be '%v', got %v", handler.AppVersion, versionVal)
	}
}

func TestHandleQuotaSummaryUnauthorized(t *testing.T) {
	_, authSvc, _, _, h, tempDir := setupTestFixture(t)
	defer os.RemoveAll(tempDir)

	os.Setenv("PASSWORD", "")
	authSvc.LoadPassword()
	sessionToken := authSvc.InitSession()

	// Temporarily simulate missing token
	homeDir := auth.HomeDirOverride
	if homeDir == "" {
		var err error
		homeDir, err = os.UserHomeDir()
		if err != nil {
			t.Fatalf("failed to get home dir: %v", err)
		}
	}
	tokenPath := filepath.Join(homeDir, ".gemini", "antigravity-cli", "antigravity-oauth-token")

	backupPath := tokenPath + ".test_bak"
	if _, err := os.Stat(tokenPath); err == nil {
		_ = os.Rename(tokenPath, backupPath)
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
	homeDir := auth.HomeDirOverride
	if homeDir == "" {
		var err error
		homeDir, err = os.UserHomeDir()
		if err != nil {
			t.Fatalf("failed to get home dir: %v", err)
		}
	}
	tokenPath := filepath.Join(homeDir, ".gemini", "antigravity-cli", "antigravity-oauth-token")

	backupPath := tokenPath + ".test_bak"
	if _, err := os.Stat(tokenPath); err == nil {
		_ = os.Rename(tokenPath, backupPath)
		defer func() {
			_ = os.Rename(backupPath, tokenPath)
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

func TestPasswordUpdateKeepsSessionLoggedIn(t *testing.T) {
	_, authSvc, _, _, h, tempDir := setupTestFixture(t)
	defer os.RemoveAll(tempDir)

	req := httptest.NewRequest(http.MethodPost, "/api/auth/pwd/update", strings.NewReader("new_password=brandnewpassword123"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()

	h.HandlePasswordUpdate(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200 for password update, got %d", rr.Code)
	}

	cookies := rr.Result().Cookies()
	var sessionCookie *http.Cookie
	for _, c := range cookies {
		if c.Name == "session_password" {
			sessionCookie = c
			break
		}
	}

	if sessionCookie == nil {
		t.Fatalf("expected session_password cookie to be returned after updating password")
	}

	if !authSvc.ValidateSession(sessionCookie.Value) {
		t.Errorf("expected updated session cookie to validate against auth service")
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
	originalUserProfile := os.Getenv("USERPROFILE")
	tempHome, err := os.MkdirTemp("", "test_home_*")
	if err != nil {
		t.Fatalf("failed to create temp home: %v", err)
	}
	defer os.RemoveAll(tempHome)

	os.Setenv("HOME", tempHome)
	os.Setenv("USERPROFILE", tempHome)
	auth.HomeDirOverride = tempHome
	chat.HomeDirOverride = tempHome
	defer func() {
		os.Setenv("HOME", originalHome)
		os.Setenv("USERPROFILE", originalUserProfile)
		auth.HomeDirOverride = ""
		chat.HomeDirOverride = ""
	}()

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

	type MockHistoryEntry struct {
		Display        string `json:"display"`
		Timestamp      int64  `json:"timestamp"`
		Workspace      string `json:"workspace"`
		ConversationID string `json:"conversationId"`
	}

	e1, _ := json.Marshal(MockHistoryEntry{Display: "Test Prompt 1", Timestamp: 1000, Workspace: activeWS, ConversationID: "conv-1"})
	e2, _ := json.Marshal(MockHistoryEntry{Display: "Test Prompt 2", Timestamp: 2000, Workspace: activeWS, ConversationID: "conv-2"})
	e3, _ := json.Marshal(MockHistoryEntry{Display: "Test Prompt 3", Timestamp: 3000, Workspace: activeWS, ConversationID: "conv-1"})
	mockData := string(e1) + "\n" + string(e2) + "\n" + string(e3) + "\n"

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
	originalUserProfile := os.Getenv("USERPROFILE")
	tempHome, err := os.MkdirTemp("", "test_home_*")
	if err != nil {
		t.Fatalf("failed to create temp home: %v", err)
	}
	defer os.RemoveAll(tempHome)

	os.Setenv("HOME", tempHome)
	os.Setenv("USERPROFILE", tempHome)
	auth.HomeDirOverride = tempHome
	chat.HomeDirOverride = tempHome
	defer func() {
		os.Setenv("HOME", originalHome)
		os.Setenv("USERPROFILE", originalUserProfile)
		auth.HomeDirOverride = ""
		chat.HomeDirOverride = ""
	}()

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
	originalUserProfile := os.Getenv("USERPROFILE")
	tempHome, err := os.MkdirTemp("", "test_home_*")
	if err != nil {
		t.Fatalf("failed to create temp home: %v", err)
	}
	defer os.RemoveAll(tempHome)

	os.Setenv("HOME", tempHome)
	os.Setenv("USERPROFILE", tempHome)
	auth.HomeDirOverride = tempHome
	chat.HomeDirOverride = tempHome
	defer func() {
		os.Setenv("HOME", originalHome)
		os.Setenv("USERPROFILE", originalUserProfile)
		auth.HomeDirOverride = ""
		chat.HomeDirOverride = ""
	}()

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
	originalUserProfile := os.Getenv("USERPROFILE")
	tempHome, err := os.MkdirTemp("", "test_home_*")
	if err != nil {
		t.Fatalf("failed to create temp home: %v", err)
	}
	defer os.RemoveAll(tempHome)

	os.Setenv("HOME", tempHome)
	os.Setenv("USERPROFILE", tempHome)
	auth.HomeDirOverride = tempHome
	chat.HomeDirOverride = tempHome
	defer func() {
		os.Setenv("HOME", originalHome)
		os.Setenv("USERPROFILE", originalUserProfile)
		auth.HomeDirOverride = ""
		chat.HomeDirOverride = ""
	}()

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

func TestAccountPoolAPI(t *testing.T) {
	_, authSvc, _, _, h, tempDir := setupTestFixture(t)
	defer os.RemoveAll(tempDir)

	os.Setenv("PASSWORD", "")
	authSvc.LoadPassword()
	sessionToken := authSvc.InitSession()

	// Mock valid Google OAuth token file
	homeDir, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("failed to get home dir: %v", err)
	}
	tokenDir := filepath.Join(homeDir, ".gemini", "antigravity-cli")
	tokenPath := filepath.Join(tokenDir, "antigravity-oauth-token")

	originalExists := false
	if _, err := os.Stat(tokenPath); err == nil {
		originalExists = true
	}
	backupPath := tokenPath + ".test_bak"
	if originalExists {
		_ = os.Rename(tokenPath, backupPath)
	}
	defer func() {
		if originalExists {
			_ = os.Rename(backupPath, tokenPath)
		} else {
			_ = os.Remove(tokenPath)
		}
	}()

	_ = os.MkdirAll(tokenDir, 0755)
	_ = os.WriteFile(tokenPath, []byte("dummy-token"), 0600)

	// Clear out any existing pool file first
	poolPath := authSvc.GetAccountsPoolPath()
	_ = os.Remove(poolPath)
	defer os.Remove(poolPath)

	// Save test accounts to pool file directly to simulate pool state
	mockPool := []auth.AccountEntry{
		{
			Email:        "test1@gmail.com",
			KeyringValue: `{"token":{"access_token":"mock-token-1"}}`,
		},
		{
			Email:        "test2@gmail.com",
			KeyringValue: `{"token":{"access_token":"mock-token-2"}}`,
		},
	}
	_ = authSvc.SaveAccountsPool(mockPool)

	// 1. Test GET /api/auth/pool
	reqGet := httptest.NewRequest(http.MethodGet, "/api/auth/pool", nil)
	reqGet.AddCookie(&http.Cookie{Name: "session_password", Value: sessionToken})
	rrGet := httptest.NewRecorder()

	h.AuthMiddleware(h.HandleGetAccountsPool).ServeHTTP(rrGet, reqGet)
	if rrGet.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rrGet.Code)
	}

	var getResp map[string]any
	if err := json.NewDecoder(rrGet.Body).Decode(&getResp); err != nil {
		t.Fatalf("failed to parse GET pool response: %v", err)
	}

	accountsVal, ok := getResp["accounts"]
	if !ok {
		t.Errorf("expected 'accounts' in pool response")
	}
	accountsSlice, ok := accountsVal.([]any)
	if !ok || len(accountsSlice) < 2 {
		t.Errorf("expected accounts slice of length at least 2, got: %v", accountsVal)
	}

	// 2. Test POST /api/auth/pool/switch
	switchReqBody := `{"email":"test2@gmail.com"}`
	reqSwitch := httptest.NewRequest(http.MethodPost, "/api/auth/pool/switch", strings.NewReader(switchReqBody))
	reqSwitch.AddCookie(&http.Cookie{Name: "session_password", Value: sessionToken})
	rrSwitch := httptest.NewRecorder()

	h.AuthMiddleware(h.HandleSwitchAccount).ServeHTTP(rrSwitch, reqSwitch)
	if rrSwitch.Code != http.StatusOK {
		t.Errorf("expected status 200 on switch, got %d", rrSwitch.Code)
	}

	// 3. Test POST /api/auth/pool/delete
	deleteReqBody := `{"email":"test1@gmail.com"}`
	reqDelete := httptest.NewRequest(http.MethodPost, "/api/auth/pool/delete", strings.NewReader(deleteReqBody))
	reqDelete.AddCookie(&http.Cookie{Name: "session_password", Value: sessionToken})
	rrDelete := httptest.NewRecorder()

	h.AuthMiddleware(h.HandleDeleteAccount).ServeHTTP(rrDelete, reqDelete)
	if rrDelete.Code != http.StatusOK {
		t.Errorf("expected status 200 on delete, got %d", rrDelete.Code)
	}

	// Verify it got deleted from the pool
	updatedPool, err := authSvc.LoadAccountsPool()
	if err != nil {
		t.Fatalf("failed to load pool: %v", err)
	}
	foundTest1 := false
	foundTest2 := false
	for _, entry := range updatedPool {
		if entry.Email == "test1@gmail.com" {
			foundTest1 = true
		}
		if entry.Email == "test2@gmail.com" {
			foundTest2 = true
		}
	}
	if foundTest1 {
		t.Errorf("expected test1@gmail.com to be deleted from pool")
	}
	if !foundTest2 {
		t.Errorf("expected test2@gmail.com to remain in pool")
	}

	// 4. Test POST /api/auth/google/clear
	reqClear := httptest.NewRequest(http.MethodPost, "/api/auth/google/clear", nil)
	reqClear.AddCookie(&http.Cookie{Name: "session_password", Value: sessionToken})
	rrClear := httptest.NewRecorder()

	h.AuthMiddleware(h.HandleClearGoogleAuth).ServeHTTP(rrClear, reqClear)
	if rrClear.Code != http.StatusOK {
		t.Errorf("expected status 200 on google clear, got %d", rrClear.Code)
	}
}

func TestStartGoogleAuthAndSubmitGoogleAuthCode(t *testing.T) {
	// 1. Isolate auth files from the real user profile. Windows caches os.UserHomeDir(),
	// so use package overrides in addition to HOME/USERPROFILE.
	originalHome := os.Getenv("HOME")
	originalUserProfile := os.Getenv("USERPROFILE")
	tempHome, err := os.MkdirTemp("", "test_auth_home_*")
	if err != nil {
		t.Fatalf("failed to create temp home: %v", err)
	}
	defer os.RemoveAll(tempHome)

	os.Setenv("HOME", tempHome)
	os.Setenv("USERPROFILE", tempHome)
	auth.HomeDirOverride = tempHome
	chat.HomeDirOverride = tempHome

	// Backup existing keyring, but keep token files inside the temp home only.
	backupVal, backupErr := keyring.Get("gemini", "antigravity")

	defer func() {
		// Restore everything in deferred cleanup
		if backupErr == nil && !strings.Contains(backupVal, "mock-") {
			_ = keyring.Set("gemini", "antigravity", backupVal)
		} else {
			_ = keyring.Delete("gemini", "antigravity")
		}
		os.Setenv("HOME", originalHome)
		os.Setenv("USERPROFILE", originalUserProfile)
		auth.HomeDirOverride = ""
		chat.HomeDirOverride = ""
	}()

	// 2. Create mock agy Go program source code
	tempDir, err := os.MkdirTemp("", "mock-agy-dir")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	mockSource := `package main

import (
	"bufio"
	"fmt"
	"os"
	"github.com/zalando/go-keyring"
)

func logMsg(msg string) {
	f, _ := os.OpenFile("/tmp/mock-agy.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
	if f != nil {
		f.WriteString(msg + "\n")
		f.Close()
	}
}

func main() {
	logMsg("started")
	fmt.Println("Select login method:")
	reader := bufio.NewReader(os.Stdin)
	logMsg("waiting for selection")
	sel, err := reader.ReadString('\n')
	logMsg("selection received: " + sel + " err: " + fmt.Sprint(err))

	fmt.Println("https://accounts.google.com/o/oauth2/auth?state=dummy_state")

	logMsg("waiting for code")
	code, err := reader.ReadString('\n')
	logMsg("code received: " + code + " err: " + fmt.Sprint(err))

	logMsg("setting keyring")
	err = keyring.Set("gemini", "antigravity", ` + "`" + `{"token":{"access_token":"mock-fresh-token"}}` + "`" + `)
	logMsg("keyring set, err: " + fmt.Sprint(err))
}
`
	srcPath := filepath.Join(tempDir, "main.go")
	if err := os.WriteFile(srcPath, []byte(mockSource), 0644); err != nil {
		t.Fatalf("failed to write mock source: %v", err)
	}

	// 3. Compile mock agy binary
	mockBinPath := filepath.Join(tempDir, "agy.exe")
	cmdCompile := exec.Command("go", "build", "-o", mockBinPath, srcPath)
	if out, err := cmdCompile.CombinedOutput(); err != nil {
		t.Fatalf("failed to compile mock agy: %v\nOutput: %s", err, out)
	}

	// 4. Force auth to use the mock agy binary instead of a real system install.
	oldPath := os.Getenv("PATH")
	oldAgyPath := os.Getenv("AGY_PATH")
	os.Setenv("PATH", tempDir+string(os.PathListSeparator)+oldPath)
	os.Setenv("AGY_PATH", mockBinPath)
	defer func() {
		os.Setenv("PATH", oldPath)
		os.Setenv("AGY_PATH", oldAgyPath)
	}()

	os.Setenv("FORCE_DIRECT_AUTH", "true")
	defer os.Unsetenv("FORCE_DIRECT_AUTH")

	// 5. Initialize auth service
	appTempDir, err := os.MkdirTemp("", "app-auth-test")
	if err != nil {
		t.Fatalf("failed to create temp app dir: %v", err)
	}
	defer os.RemoveAll(appTempDir)

	authSvc := auth.NewService(appTempDir)

	// 6. Test StartGoogleAuth
	url, err := authSvc.StartGoogleAuth(appTempDir)
	if err != nil {
		t.Fatalf("StartGoogleAuth failed: %v", err)
	}
	if !strings.HasPrefix(url, "https://accounts.google.com/o/oauth2/auth") {
		t.Errorf("expected oauth URL, got: %s", url)
	}

	// 7. Test SubmitGoogleAuthCode
	err = authSvc.SubmitGoogleAuthCode("dummy-code-123")
	if err != nil {
		t.Fatalf("SubmitGoogleAuthCode failed: %v", err)
	}

	// 8. Verify token added to pool
	pool, err := authSvc.LoadAccountsPool()
	if err != nil {
		t.Fatalf("failed to load accounts pool: %v", err)
	}

	found := false
	for _, entry := range pool {
		if entry.KeyringValue == `{"token":{"access_token":"mock-fresh-token"}}` {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected mock token to be present in accounts pool, pool: %+v", pool)
	}
}

func TestPWARoutes(t *testing.T) {
	_, _, _, _, h, tempWS := setupTestFixture(t)
	defer os.RemoveAll(tempWS)

	_ = os.WriteFile(filepath.Join(tempWS, "manifest.json"), []byte(`{"name":"test"}`), 0644)
	_ = os.WriteFile(filepath.Join(tempWS, "sw.js"), []byte("console.log('sw')"), 0644)
	_ = os.WriteFile(filepath.Join(tempWS, "icon-192.png"), []byte{1, 2, 3}, 0644)
	_ = os.WriteFile(filepath.Join(tempWS, "icon-512.png"), []byte{4, 5, 6}, 0644)

	tests := []struct {
		url          string
		handler      http.HandlerFunc
		expectedCode int
		expectedCT   string
	}{
		{"/manifest.json", h.HandleManifest, http.StatusOK, "application/json"},
		{"/sw.js", h.HandleServiceWorker, http.StatusOK, "application/javascript"},
		{"/icon-192.png", h.HandleIcon192, http.StatusOK, "image/png"},
		{"/icon-512.png", h.HandleIcon512, http.StatusOK, "image/png"},
	}

	for _, tc := range tests {
		req := httptest.NewRequest("GET", tc.url, nil)
		w := httptest.NewRecorder()
		tc.handler(w, req)

		if w.Code != tc.expectedCode {
			t.Errorf("%s: expected code %d, got %d", tc.url, tc.expectedCode, w.Code)
		}
		ct := w.Header().Get("Content-Type")
		if !strings.HasPrefix(ct, tc.expectedCT) {
			t.Errorf("%s: expected Content-Type prefix %q, got %q", tc.url, tc.expectedCT, ct)
		}
	}
}
