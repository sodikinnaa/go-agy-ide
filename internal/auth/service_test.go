package auth

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestDecodeJWTEmail(t *testing.T) {
	payloadJSON := `{"email":"testuser@gmail.com","sub":"12345"}`
	encodedPayload := base64.RawURLEncoding.EncodeToString([]byte(payloadJSON))
	jwtStr := fmt.Sprintf("header.%s.signature", encodedPayload)

	email := decodeJWTEmail(jwtStr)
	if email != "testuser@gmail.com" {
		t.Errorf("expected testuser@gmail.com, got %s", email)
	}

	tokenJSON := fmt.Sprintf(`{"token":{"access_token":"abc","id_token":"%s"}}`, jwtStr)
	extracted := extractEmailFromTokenJSON(tokenJSON)
	if extracted != "testuser@gmail.com" {
		t.Errorf("expected testuser@gmail.com from token JSON, got %s", extracted)
	}
}

func TestSyncCurrentAccountToPool(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "test_auth_pool_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	HomeDirOverride = tempDir
	defer func() { HomeDirOverride = "" }()

	svc := NewService(tempDir)

	payloadJSON := `{"email":"pooluser@gmail.com"}`
	encodedPayload := base64.RawURLEncoding.EncodeToString([]byte(payloadJSON))
	jwtStr := fmt.Sprintf("header.%s.sig", encodedPayload)
	tokenJSON := fmt.Sprintf(`{"token":{"access_token":"token123","id_token":"%s"}}`, jwtStr)

	tokenPath := filepath.Join(tempDir, ".gemini", "antigravity-cli", "antigravity-oauth-token")
	_ = os.MkdirAll(filepath.Dir(tokenPath), 0755)
	_ = os.WriteFile(tokenPath, []byte(tokenJSON), 0600)

	err = svc.SyncCurrentAccountToPool()
	if err != nil {
		t.Fatalf("SyncCurrentAccountToPool failed: %v", err)
	}

	pool, err := svc.LoadAccountsPool()
	if err != nil {
		t.Fatalf("failed to load pool: %v", err)
	}

	if len(pool) != 1 {
		t.Fatalf("expected 1 entry in pool, got %d", len(pool))
	}

	if pool[0].Email != "pooluser@gmail.com" {
		t.Errorf("expected pooluser@gmail.com in pool, got %s", pool[0].Email)
	}
}

func TestUpdateAccountStatus(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "test_auth_status_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	HomeDirOverride = tempDir
	defer func() { HomeDirOverride = "" }()

	svc := NewService(tempDir)
	pool := []AccountEntry{
		{
			Email:        "user1@gmail.com",
			KeyringValue: `{"token":{"access_token":"t1"}}`,
			Status:       "valid",
		},
	}
	_ = svc.SaveAccountsPool(pool)

	err = svc.UpdateAccountStatus("user1@gmail.com", "suspended", "This service has been disabled in this account for violation of Terms of Service")
	if err != nil {
		t.Fatalf("UpdateAccountStatus failed: %v", err)
	}

	updatedPool, err := svc.LoadAccountsPool()
	if err != nil {
		t.Fatalf("LoadAccountsPool failed: %v", err)
	}

	if len(updatedPool) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(updatedPool))
	}
	if updatedPool[0].Status != "suspended" {
		t.Errorf("expected status 'suspended', got %s", updatedPool[0].Status)
	}
	if updatedPool[0].ErrorMsg != "This service has been disabled in this account for violation of Terms of Service" {
		t.Errorf("unexpected errorMsg: %s", updatedPool[0].ErrorMsg)
	}
}
