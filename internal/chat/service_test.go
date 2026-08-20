package chat

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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

func TestStartOpenAIChat_SSEStreaming(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "openai_test_chat_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	HomeDirOverride = tempDir
	defer func() { HomeDirOverride = "" }()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer test-api-key" {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)

		flusher, ok := w.(http.Flusher)
		if !ok {
			return
		}

		// Send reasoning chunk
		_, _ = w.Write([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"reasoning_content\":\"thinking step 1\\nthinking step 2\"}}]}\n\n"))
		flusher.Flush()

		// Send content chunk 1
		_, _ = w.Write([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hello \"}}]}\n\n"))
		flusher.Flush()

		// Send content chunk 2
		_, _ = w.Write([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"from SSE stream!\"}}]}\n\n"))
		flusher.Flush()

		// Send DONE
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
		flusher.Flush()
	}))
	defer ts.Close()

	os.Setenv("OPENAI_API_KEY", "test-api-key")
	os.Setenv("OPENAI_API_BASE", ts.URL)
	defer os.Unsetenv("OPENAI_API_KEY")
	defer os.Unsetenv("OPENAI_API_BASE")

	svc := NewService()
	req := ChatRequest{
		Prompt: "Say hello",
		Model:  "openai/gpt-4o",
		IsPure: true,
	}

	reader, err := svc.StartOpenAIChat(context.Background(), &req, tempDir)
	if err != nil {
		t.Fatalf("StartOpenAIChat failed: %v", err)
	}
	defer reader.Close()

	outBytes, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("failed to read from pipe: %v", err)
	}

	output := string(outBytes)
	if !strings.Contains(output, "▸ Thought") {
		t.Errorf("expected output to contain '▸ Thought', got: %s", output)
	}
	if !strings.Contains(output, "thinking step 1") {
		t.Errorf("expected output to contain thinking step 1, got: %s", output)
	}
	if !strings.Contains(output, "Hello from SSE stream!") {
		t.Errorf("expected output to contain 'Hello from SSE stream!', got: %s", output)
	}
}

func TestStartOpenAIChat_NonSSEFallback(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "openai_fallback_chat_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	HomeDirOverride = tempDir
	defer func() { HomeDirOverride = "" }()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		respJSON := `{
			"choices": [
				{
					"message": {
						"role": "assistant",
						"content": "Hello from fallback JSON!"
					},
					"finish_reason": "stop"
				}
			]
		}`
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(respJSON))
	}))
	defer ts.Close()

	os.Setenv("OPENAI_API_KEY", "test-api-key")
	os.Setenv("OPENAI_API_BASE", ts.URL)
	defer os.Unsetenv("OPENAI_API_KEY")
	defer os.Unsetenv("OPENAI_API_BASE")

	svc := NewService()
	req := ChatRequest{
		Prompt: "Say hello fallback",
		Model:  "openai/gpt-4o",
		IsPure: true,
	}

	reader, err := svc.StartOpenAIChat(context.Background(), &req, tempDir)
	if err != nil {
		t.Fatalf("StartOpenAIChat failed: %v", err)
	}
	defer reader.Close()

	outBytes, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("failed to read from pipe: %v", err)
	}

	output := string(outBytes)
	if !strings.Contains(output, "Hello from fallback JSON!") {
		t.Errorf("expected output to contain 'Hello from fallback JSON!', got: %s", output)
	}
}

func TestStartOpenAIChat_ToolCallsStreaming(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "openai_tool_chat_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	_ = os.WriteFile(filepath.Join(tempDir, "test.txt"), []byte("Hello tool call"), 0644)

	HomeDirOverride = tempDir
	defer func() { HomeDirOverride = "" }()

	round := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)

		if round == 0 {
			round++
			// Stream tool call chunk
			_, _ = w.Write([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"list_dir\",\"arguments\":\"{\\\"path\\\": \\\".\\\"}\"}}]}}]}\n\n"))
			flusher.Flush()
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
			flusher.Flush()
		} else {
			// Stream final answer
			_, _ = w.Write([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Found test.txt in workspace.\"}}]}\n\n"))
			flusher.Flush()
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
			flusher.Flush()
		}
	}))
	defer ts.Close()

	os.Setenv("OPENAI_API_KEY", "test-api-key")
	os.Setenv("OPENAI_API_BASE", ts.URL)
	defer os.Unsetenv("OPENAI_API_KEY")
	defer os.Unsetenv("OPENAI_API_BASE")

	svc := NewService()
	req := ChatRequest{
		Prompt: "List files",
		Model:  "openai/gpt-4o",
		IsPure: false,
	}

	reader, err := svc.StartOpenAIChat(context.Background(), &req, tempDir)
	if err != nil {
		t.Fatalf("StartOpenAIChat failed: %v", err)
	}
	defer reader.Close()

	outBytes, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("failed to read from pipe: %v", err)
	}

	output := string(outBytes)
	if !strings.Contains(output, "list_dir") {
		t.Errorf("expected output to contain tool name 'list_dir', got: %s", output)
	}
	if !strings.Contains(output, "Found test.txt in workspace.") {
		t.Errorf("expected output to contain final answer, got: %s", output)
	}
}
