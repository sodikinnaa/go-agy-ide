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
