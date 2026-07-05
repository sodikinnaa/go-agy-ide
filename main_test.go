package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
	// Set up request and recorder
	req := httptest.NewRequest(http.MethodGet, "/api/auth/status", nil)
	rr := httptest.NewRecorder()

	// Run handler
	handleAuthStatus(rr, req)

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

	// Run handler
	handleListFiles(rr, req)

	// Verify response status is 401
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected status 401, got %d", rr.Code)
	}
}
