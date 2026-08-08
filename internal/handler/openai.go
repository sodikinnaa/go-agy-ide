package handler

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand"
	"mobile-agy/internal/chat"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type OpenAIModelItem struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

type OpenAIModelsResponse struct {
	Object string            `json:"object"`
	Data   []OpenAIModelItem `json:"data"`
}

type OpenAIChatCompletionRequest struct {
	Model       string              `json:"model"`
	Messages    []OpenAIChatMessage `json:"messages"`
	Stream      bool                `json:"stream"`
	Temperature *float64            `json:"temperature,omitempty"`
	MaxTokens   *int                `json:"max_tokens,omitempty"`
	User        string              `json:"user,omitempty"`
}

type OpenAIChatMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
	Name    string          `json:"name,omitempty"`
}

type OpenAIContentPart struct {
	Type     string          `json:"type"`
	Text     string          `json:"text,omitempty"`
	ImageURL *OpenAIImageURL `json:"image_url,omitempty"`
	FileURL  *OpenAIFileURL  `json:"file_url,omitempty"`
}

type OpenAIImageURL struct {
	URL    string `json:"url"`
	Detail string `json:"detail,omitempty"`
}

type OpenAIFileURL struct {
	URL  string `json:"url"`
	Name string `json:"name,omitempty"`
}

type OpenAIChatCompletionChunk struct {
	ID      string                       `json:"id"`
	Object  string                       `json:"object"`
	Created int64                        `json:"created"`
	Model   string                       `json:"model"`
	Choices []OpenAIChatCompletionChoice `json:"choices"`
}

type OpenAIChatCompletionChoice struct {
	Index        int         `json:"index"`
	Delta        OpenAIDelta `json:"delta"`
	FinishReason *string     `json:"finish_reason"`
}

type OpenAIDelta struct {
	Role    string `json:"role,omitempty"`
	Content string `json:"content,omitempty"`
}

type OpenAIChatCompletionResponse struct {
	ID      string                  `json:"id"`
	Object  string                  `json:"object"`
	Created int64                   `json:"created"`
	Model   string                  `json:"model"`
	Choices []OpenAINonStreamChoice `json:"choices"`
	Usage   OpenAIUsage             `json:"usage"`
}

type OpenAINonStreamChoice struct {
	Index        int                       `json:"index"`
	Message      OpenAIChatMessageResponse `json:"message"`
	FinishReason string                    `json:"finish_reason"`
}

type OpenAIChatMessageResponse struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type OpenAIUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// HandleV1Models lists available models in OpenAI JSON format (GET /v1/models)
func (h *Handler) HandleV1Models(w http.ResponseWriter, r *http.Request) {
	h.enableCORS(w)
	if r.Method == http.MethodOptions {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	modelsList, err := h.terminalSvc.GetModelsList()
	if err != nil || len(modelsList) == 0 {
		modelsList = []string{
			"gemini-3.5-flash-high",
			"gemini-3.5-flash-medium",
			"gemini-3.5-flash-low",
			"gemini-3.1-pro-high",
			"gemini-3.1-pro-low",
			"claude-sonnet-4-6",
			"claude-opus-4-6-thinking",
			"gpt-oss-120b-medium",
		}
	}

	now := time.Now().Unix()
	data := make([]OpenAIModelItem, 0, len(modelsList))
	for _, m := range modelsList {
		data = append(data, OpenAIModelItem{
			ID:      m,
			Object:  "model",
			Created: now,
			OwnedBy: "mobile-agy",
		})
	}

	resp := OpenAIModelsResponse{
		Object: "list",
		Data:   data,
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// HandleV1ChatCompletions handles chat completion requests in OpenAI format (POST /v1/chat/completions)
func (h *Handler) HandleV1ChatCompletions(w http.ResponseWriter, r *http.Request) {
	h.enableCORS(w)
	if r.Method == http.MethodOptions {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req OpenAIChatCompletionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON request: "+err.Error(), http.StatusBadRequest)
		return
	}

	if len(req.Messages) == 0 {
		http.Error(w, "messages cannot be empty", http.StatusBadRequest)
		return
	}

	modelName := strings.TrimSpace(req.Model)
	if modelName == "" {
		modelName = "gemini-3.5-flash-high"
	}

	activeWorkspace := h.workspaceSvc.ActiveWorkspaceDir()
	fullPrompt := h.buildOpenAIPrompt(req.Messages, activeWorkspace)

	convID := fmt.Sprintf("openai-v1-%d", time.Now().UnixNano())

	chatReq := chat.ChatRequest{
		Prompt:       fullPrompt,
		Model:        modelName,
		Conversation: convID,
	}

	var cmd *exec.Cmd
	var stdoutPipe io.ReadCloser
	var err error

	for attempt := 0; attempt < 3; attempt++ {
		cmd, stdoutPipe, err = h.chatSvc.StartChat(r.Context(), chatReq, activeWorkspace)
		if err != nil {
			errStr := err.Error()
			if strings.Contains(errStr, "403") || strings.Contains(errStr, "disabled") || strings.Contains(errStr, "quota") || strings.Contains(errStr, "429") || strings.Contains(errStr, "UNAUTHENTICATED") {
				if _, rotated, rotErr := h.authSvc.RotateToNextHealthyAccount(errStr); rotErr == nil && rotated {
					log.Printf("[OPENAI WRAPPER] Account failure on attempt %d (%v). Rotated to next healthy account and retrying...", attempt+1, errStr)
					continue
				}
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		break
	}

	defer h.chatSvc.CleanupChat(convID, cmd)

	if cmd != nil {
		go func() {
			_ = cmd.Wait()
			_ = stdoutPipe.Close()
		}()
	}

	chatID := fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano())
	createdTime := time.Now().Unix()

	if req.Stream {
		h.streamOpenAIChatResponse(w, chatID, createdTime, modelName, stdoutPipe)
	} else {
		h.nonStreamOpenAIChatResponse(w, chatID, createdTime, modelName, stdoutPipe)
	}
}

func (h *Handler) streamOpenAIChatResponse(w http.ResponseWriter, chatID string, createdTime int64, modelName string, stdoutPipe io.Reader) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	buf := make([]byte, 1024)
	for {
		n, err := stdoutPipe.Read(buf)
		if n > 0 {
			chunkText := string(buf[:n])
			chunkText = strings.ReplaceAll(chunkText, "<!-- keep-alive -->", "")
			if chunkText != "" {
				chunkObj := OpenAIChatCompletionChunk{
					ID:      chatID,
					Object:  "chat.completion.chunk",
					Created: createdTime,
					Model:   modelName,
					Choices: []OpenAIChatCompletionChoice{
						{
							Index: 0,
							Delta: OpenAIDelta{
								Content: chunkText,
							},
							FinishReason: nil,
						},
					},
				}
				chunkJSON, jsonErr := json.Marshal(chunkObj)
				if jsonErr == nil {
					_, _ = fmt.Fprintf(w, "data: %s\n\n", chunkJSON)
					flusher.Flush()
				}
			}
		}
		if err != nil {
			break
		}
	}

	finishReason := "stop"
	finalChunk := OpenAIChatCompletionChunk{
		ID:      chatID,
		Object:  "chat.completion.chunk",
		Created: createdTime,
		Model:   modelName,
		Choices: []OpenAIChatCompletionChoice{
			{
				Index:        0,
				Delta:        OpenAIDelta{},
				FinishReason: &finishReason,
			},
		},
	}
	finalJSON, _ := json.Marshal(finalChunk)
	_, _ = fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", finalJSON)
	flusher.Flush()
}

func (h *Handler) nonStreamOpenAIChatResponse(w http.ResponseWriter, chatID string, createdTime int64, modelName string, stdoutPipe io.Reader) {
	outBytes, _ := io.ReadAll(stdoutPipe)
	fullText := string(outBytes)
	fullText = strings.ReplaceAll(fullText, "<!-- keep-alive -->", "")

	if strings.Contains(fullText, "status 403") || strings.Contains(fullText, "TOS_VIOLATION") || strings.Contains(fullText, "disabled in this account") || strings.Contains(fullText, "PERMISSION_DENIED") || strings.Contains(fullText, "quota summary") {
		log.Printf("[OPENAI WRAPPER] Detected account error in response text: %s. Triggering automatic pool rotation...", fullText)
		_, _, _ = h.authSvc.RotateToNextHealthyAccount(fullText)
	}

	resp := OpenAIChatCompletionResponse{
		ID:      chatID,
		Object:  "chat.completion",
		Created: createdTime,
		Model:   modelName,
		Choices: []OpenAINonStreamChoice{
			{
				Index: 0,
				Message: OpenAIChatMessageResponse{
					Role:    "assistant",
					Content: fullText,
				},
				FinishReason: "stop",
			},
		},
		Usage: OpenAIUsage{
			PromptTokens:     0,
			CompletionTokens: 0,
			TotalTokens:      0,
		},
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func parseMessageContent(raw json.RawMessage) (string, []OpenAIContentPart, error) {
	if len(raw) == 0 {
		return "", nil, nil
	}
	var strContent string
	if err := json.Unmarshal(raw, &strContent); err == nil {
		return strContent, nil, nil
	}
	var parts []OpenAIContentPart
	if err := json.Unmarshal(raw, &parts); err == nil {
		return "", parts, nil
	}
	return string(raw), nil, nil
}

func (h *Handler) buildOpenAIPrompt(messages []OpenAIChatMessage, activeWorkspace string) string {
	var sb strings.Builder
	var userBuffer strings.Builder

	for i, msg := range messages {
		strContent, parts, _ := parseMessageContent(msg.Content)
		msgText := strContent
		var attachedFiles []string

		for _, part := range parts {
			if part.Type == "text" && part.Text != "" {
				if msgText != "" {
					msgText += "\n"
				}
				msgText += part.Text
			}

			if (part.Type == "image_url" || part.ImageURL != nil) && part.ImageURL != nil && part.ImageURL.URL != "" {
				relPath, absPath, err := saveBase64OrURLFile(activeWorkspace, part.ImageURL.URL, "image")
				if err == nil && absPath != "" {
					ref := fmt.Sprintf("[Attached Image/File: %s (Absolute Path: %s)]", relPath, absPath)
					attachedFiles = append(attachedFiles, ref)
				} else if relPath != "" {
					attachedFiles = append(attachedFiles, fmt.Sprintf("[Attached Image URL: %s]", relPath))
				}
			}

			if (part.Type == "file_url" || part.Type == "input_file" || part.FileURL != nil) && part.FileURL != nil && part.FileURL.URL != "" {
				relPath, absPath, err := saveBase64OrURLFile(activeWorkspace, part.FileURL.URL, "file")
				if err == nil && absPath != "" {
					ref := fmt.Sprintf("[Attached Document/File: %s (Absolute Path: %s)]", relPath, absPath)
					attachedFiles = append(attachedFiles, ref)
				} else if relPath != "" {
					attachedFiles = append(attachedFiles, fmt.Sprintf("[Attached File URL: %s]", relPath))
				}
			}
		}

		if len(attachedFiles) > 0 {
			if msgText != "" {
				msgText += "\n\n"
			}
			msgText += strings.Join(attachedFiles, "\n")
		}

		role := strings.ToLower(strings.TrimSpace(msg.Role))
		switch role {
		case "system":
			sb.WriteString("System Instruction: ")
			sb.WriteString(msgText)
			sb.WriteString("\n\n")
		case "assistant":
			if len(messages) > 1 {
				userBuffer.WriteString(fmt.Sprintf("Assistant: %s\n\n", msgText))
			}
		case "user":
			if i == len(messages)-1 || len(messages) == 1 {
				userBuffer.WriteString(fmt.Sprintf("User: %s\n\n", msgText))
			} else {
				userBuffer.WriteString(fmt.Sprintf("User: %s\n\n", msgText))
			}
		default:
			userBuffer.WriteString(fmt.Sprintf("%s: %s\n\n", msg.Role, msgText))
		}
	}

	if sb.Len() > 0 {
		sb.WriteString(userBuffer.String())
		return strings.TrimSpace(sb.String())
	}

	return strings.TrimSpace(userBuffer.String())
}

func saveBase64OrURLFile(activeWorkspaceDir, urlStr, fileTypeHint string) (string, string, error) {
	if strings.HasPrefix(urlStr, "data:") {
		commaIdx := strings.Index(urlStr, ",")
		if commaIdx == -1 {
			return "", "", fmt.Errorf("invalid data URI format")
		}
		header := urlStr[:commaIdx]
		b64Data := urlStr[commaIdx+1:]

		mimeType := ""
		if strings.Contains(header, ";") {
			mimeType = strings.TrimPrefix(strings.Split(header, ";")[0], "data:")
		} else {
			mimeType = strings.TrimPrefix(header, "data:")
		}

		ext := mimeToExt(mimeType, fileTypeHint)

		b64Data = strings.ReplaceAll(b64Data, " ", "+")
		b64Data = strings.ReplaceAll(b64Data, "\n", "")
		b64Data = strings.ReplaceAll(b64Data, "\r", "")

		decoded, err := base64.StdEncoding.DecodeString(b64Data)
		if err != nil {
			decoded, err = base64.RawStdEncoding.DecodeString(b64Data)
			if err != nil {
				return "", "", fmt.Errorf("failed to decode base64 data: %v", err)
			}
		}

		scratchDir := filepath.Join(activeWorkspaceDir, "scratch", "openai_files")
		if err := os.MkdirAll(scratchDir, 0755); err != nil {
			return "", "", fmt.Errorf("failed to create scratch directory: %v", err)
		}

		fileName := fmt.Sprintf("file_%d_%d%s", time.Now().UnixNano(), rand.Intn(10000), ext)
		absPath := filepath.Join(scratchDir, fileName)
		if err := os.WriteFile(absPath, decoded, 0644); err != nil {
			return "", "", fmt.Errorf("failed to save file: %v", err)
		}

		relPath, _ := filepath.Rel(activeWorkspaceDir, absPath)
		return filepath.ToSlash(relPath), absPath, nil
	} else if strings.HasPrefix(urlStr, "http://") || strings.HasPrefix(urlStr, "https://") {
		client := &http.Client{Timeout: 15 * time.Second}
		resp, err := client.Get(urlStr)
		if err == nil && resp.StatusCode == http.StatusOK {
			defer resp.Body.Close()
			data, err := io.ReadAll(resp.Body)
			if err == nil && len(data) > 0 {
				mimeType := resp.Header.Get("Content-Type")
				if strings.Contains(mimeType, ";") {
					mimeType = strings.TrimSpace(strings.Split(mimeType, ";")[0])
				}
				ext := mimeToExt(mimeType, fileTypeHint)
				if ext == ".bin" || ext == "" {
					if uExt := filepath.Ext(urlStr); uExt != "" && len(uExt) <= 5 {
						ext = uExt
					}
				}

				scratchDir := filepath.Join(activeWorkspaceDir, "scratch", "openai_files")
				_ = os.MkdirAll(scratchDir, 0755)
				fileName := fmt.Sprintf("file_%d_%d%s", time.Now().UnixNano(), rand.Intn(10000), ext)
				absPath := filepath.Join(scratchDir, fileName)
				if err := os.WriteFile(absPath, data, 0644); err == nil {
					relPath, _ := filepath.Rel(activeWorkspaceDir, absPath)
					return filepath.ToSlash(relPath), absPath, nil
				}
			}
		}
	}
	return urlStr, "", nil
}

func mimeToExt(mimeType, fileTypeHint string) string {
	switch strings.ToLower(mimeType) {
	case "image/png":
		return ".png"
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/webp":
		return ".webp"
	case "image/gif":
		return ".gif"
	case "image/svg+xml":
		return ".svg"
	case "application/pdf":
		return ".pdf"
	case "text/plain":
		return ".txt"
	case "text/markdown", "text/x-markdown":
		return ".md"
	case "text/csv":
		return ".csv"
	case "application/json":
		return ".json"
	case "text/html":
		return ".html"
	default:
		if fileTypeHint == "image" || strings.HasPrefix(mimeType, "image/") {
			return ".png"
		}
		return ".bin"
	}
}
