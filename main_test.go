package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerateRandomPassword(t *testing.T) {
	lengths := []int{8, 16, 32}
	for _, l := range lengths {
		pass := generateRandomPassword(l)
		if len(pass) != l {
			t.Errorf("expected length %d, got %d", l, len(pass))
		}
	}
}

func TestCheckOAuthTokenExists(t *testing.T) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("failed to get home dir: %v", err)
	}

	tokenDir := filepath.Join(homeDir, ".gemini", "antigravity-cli")
	tokenPath := filepath.Join(tokenDir, "antigravity-oauth-token")

	// Save original token status
	originalExists := checkOAuthTokenExists()

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
	if checkOAuthTokenExists() {
		t.Errorf("expected checkOAuthTokenExists to be false, got true")
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
	if !checkOAuthTokenExists() {
		t.Errorf("expected checkOAuthTokenExists to be true, got false")
	}
}

func TestHandleAuthStatus(t *testing.T) {
	// Bypass password check for this test
	originalPwd := secretPassword
	secretPassword = ""
	defer func() { secretPassword = originalPwd }()

	// Set up request and recorder
	req := httptest.NewRequest(http.MethodGet, "/api/auth/status", nil)
	rr := httptest.NewRecorder()

	// Run handler through middleware
	handler := authMiddleware(handleAuthStatus)
	handler.ServeHTTP(rr, req)

	// Verify response status
	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rr.Code)
	}

	// Verify JSON response structure
	var resp map[string]bool
	err := json.NewDecoder(rr.Body).Decode(&resp)
	if err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if _, ok := resp["authenticated"]; !ok {
		t.Errorf("expected 'authenticated' key in response")
	}
}

func TestHandleListFilesUnauthorized(t *testing.T) {
	// Bypass password check for this test
	originalPwd := secretPassword
	secretPassword = ""
	defer func() { secretPassword = originalPwd }()

	// Verify that listing files returns 401 when the server is not authenticated.
	// Temporarily simulate missing token
	homeDir, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("failed to get home dir: %v", err)
	}
	tokenPath := filepath.Join(homeDir, ".gemini", "antigravity-cli", "antigravity-oauth-token")
	
	originalExists := checkOAuthTokenExists()
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

	// Set up request and recorder
	req := httptest.NewRequest(http.MethodGet, "/api/files", nil)
	rr := httptest.NewRecorder()

	// Run handler through middleware
	handler := authMiddleware(handleListFiles)
	handler.ServeHTTP(rr, req)

	// Verify response status is 401
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected status 401, got %d", rr.Code)
	}
}

func TestHandlePasswordAuth(t *testing.T) {
	// Set mock password
	originalPwd := secretPassword
	secretPassword = "supersecretpassword123"
	defer func() { secretPassword = originalPwd }()

	// 1. Test Incorrect Password
	reqIncorrect := httptest.NewRequest(http.MethodPost, "/api/auth/pwd", strings.NewReader("password=wrongpassword"))
	reqIncorrect.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rrIncorrect := httptest.NewRecorder()

	handlePasswordAuth(rrIncorrect, reqIncorrect)

	if rrIncorrect.Code != http.StatusUnauthorized {
		t.Errorf("expected status 401 for wrong password, got %d", rrIncorrect.Code)
	}

	// 2. Test Correct Password
	reqCorrect := httptest.NewRequest(http.MethodPost, "/api/auth/pwd", strings.NewReader("password=supersecretpassword123"))
	reqCorrect.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rrCorrect := httptest.NewRecorder()

	handlePasswordAuth(rrCorrect, reqCorrect)

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

	if sessionCookie.Value != passwordSessionToken {
		t.Errorf("expected cookie value to match session token, got %s", sessionCookie.Value)
	}
}

func TestHandleChatHistoryList(t *testing.T) {
	// Bypass password check
	originalPwd := secretPassword
	secretPassword = ""
	defer func() { secretPassword = originalPwd }()

	// Set active workspace to match mock history workspace
	originalWorkspace := activeWorkspaceDir
	activeWorkspaceDir = "/workspace"
	defer func() { activeWorkspaceDir = originalWorkspace }()

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

	mockData := `{"display":"Test Prompt 1","timestamp":1000,"workspace":"/workspace","conversationId":"conv-1"}
{"display":"Test Prompt 2","timestamp":2000,"workspace":"/workspace","conversationId":"conv-2"}
{"display":"Test Prompt 3","timestamp":3000,"workspace":"/workspace","conversationId":"conv-1"}
`
	err = os.WriteFile(filepath.Join(historyDir, "history.jsonl"), []byte(mockData), 0644)
	if err != nil {
		t.Fatalf("failed to write mock history file: %v", err)
	}

	// Request
	req := httptest.NewRequest(http.MethodGet, "/api/chat/history", nil)
	rr := httptest.NewRecorder()

	handler := authMiddleware(handleChatHistoryList)
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rr.Code)
	}

	var list []ChatHistoryItem
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
	// Bypass password check
	originalPwd := secretPassword
	secretPassword = ""
	defer func() { secretPassword = originalPwd }()

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
	rr := httptest.NewRecorder()

	handler := authMiddleware(handleChatHistoryDetail)
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rr.Code)
	}

	var detail ChatHistoryDetail
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
