package chat

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestWorkspaceMatch(t *testing.T) {
	svc := NewService()
	if !svc.isWorkspaceMatch("/home/user/project", "/home/user/project") {
		t.Errorf("expected exact workspace match to return true")
	}
	if !svc.isWorkspaceMatch("/home/user/project/sub", "/home/user/project") {
		t.Errorf("expected subfolder workspace match to return true")
	}
	if svc.isWorkspaceMatch("/home/user/project2", "/home/user/project") {
		t.Errorf("expected different folder to return false")
	}
}

func TestBuildWorkspaceSnapshot(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "snapshot_test_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	_ = os.WriteFile(filepath.Join(tmpDir, "main.go"), []byte("package main"), 0644)
	_ = os.MkdirAll(filepath.Join(tmpDir, ".git"), 0755)
	_ = os.WriteFile(filepath.Join(tmpDir, ".git", "config"), []byte("gitconfig"), 0644)

	snapshot := buildWorkspaceSnapshot(tmpDir)
	if snapshot == "" {
		t.Errorf("expected non-empty snapshot")
	}
}

func TestCleanupChat(t *testing.T) {
	svc := NewService()
	ctx, cancel := context.WithCancel(context.Background())
	svc.activeChatCancels["test-conv"] = cancel

	svc.CleanupChat("test-conv", nil)

	if _, exists := svc.activeChatCancels["test-conv"]; exists {
		t.Errorf("expected cancel func to be removed from activeChatCancels")
	}
	if ctx.Err() != context.Canceled {
		t.Errorf("expected context to be canceled on cleanup")
	}
}

func TestChatRequestPureFlags(t *testing.T) {
	req := ChatRequest{
		Prompt:     "Hello",
		SkipAddDir: true,
		IsPure:     true,
	}

	if !req.SkipAddDir || !req.IsPure {
		t.Errorf("expected SkipAddDir and IsPure to be true")
	}
}
