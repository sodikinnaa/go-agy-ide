package chat

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mobile-agy/internal/auth"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

type ChatRequest struct {
	Prompt       string `json:"prompt"`
	Model        string `json:"model"`
	Continue     bool   `json:"continue"`
	Conversation string `json:"conversation"`
}

type HistoryEntry struct {
	Display        string `json:"display"`
	Timestamp      int64  `json:"timestamp"`
	Workspace      string `json:"workspace"`
	ConversationID string `json:"conversationId"`
}

type ChatHistoryItem struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Timestamp int64  `json:"timestamp"`
}

type ChatMessage struct {
	Role      string `json:"role"`
	Content   string `json:"content"`
	Timestamp string `json:"timestamp"`
}

type openAIFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type openAIToolCall struct {
	ID       string             `json:"id,omitempty"`
	Type     string             `json:"type,omitempty"`
	Function openAIFunctionCall `json:"function"`
}

type openAIMessage struct {
	Role       string           `json:"role"`
	Content    string           `json:"content,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
	ToolCalls  []openAIToolCall `json:"tool_calls,omitempty"`
}

type ChatHistoryDetail struct {
	ID       string        `json:"id"`
	Messages []ChatMessage `json:"messages"`
}

type ToolCall struct {
	Name        string `json:"name"`
	ToolAction  string `json:"tool_action"`
	ToolSummary string `json:"tool_summary"`
	Arguments   any    `json:"args"`
}

type TranscriptLine struct {
	Source    string     `json:"source"`
	Type      string     `json:"type"`
	Content   string     `json:"content"`
	CreatedAt string     `json:"created_at"`
	Thinking  string     `json:"thinking"`
	ToolCalls []ToolCall `json:"tool_calls"`
}

type Service struct {
	mu                sync.Mutex
	activeChatCmds    map[string]*exec.Cmd
	activeChatCancels map[string]context.CancelFunc
}

func NewService() *Service {
	return &Service{
		activeChatCmds:    make(map[string]*exec.Cmd),
		activeChatCancels: make(map[string]context.CancelFunc),
	}
}

var HomeDirOverride string

func getHomeDir() (string, error) {
	if HomeDirOverride != "" {
		return HomeDirOverride, nil
	}
	return os.UserHomeDir()
}

func (s *Service) getHistoryFilePath() (string, error) {
	homeDir, err := getHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(homeDir, ".gemini", "antigravity-cli", "history.jsonl"), nil
}

func (s *Service) isWorkspaceMatch(w1, w2 string) bool {
	p1 := filepath.Clean(w1)
	p2 := filepath.Clean(w2)
	if p1 == p2 {
		return true
	}
	return strings.HasPrefix(p1, p2+string(filepath.Separator)) || strings.HasPrefix(p2, p1+string(filepath.Separator))
}

// GetHistory reads chat history entries from history.jsonl
func (s *Service) GetHistory(activeWorkspaceDir string) ([]ChatHistoryItem, error) {
	historyPath, err := s.getHistoryFilePath()
	if err != nil {
		return nil, err
	}

	file, err := os.Open(historyPath)
	if err != nil {
		return []ChatHistoryItem{}, nil
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	var entries []HistoryEntry
	for scanner.Scan() {
		lineBytes := scanner.Bytes()
		if len(lineBytes) == 0 {
			continue
		}
		var entry HistoryEntry
		if err := json.Unmarshal(lineBytes, &entry); err != nil {
			continue
		}
		entries = append(entries, entry)
	}

	if err := scanner.Err(); err != nil {
		log.Printf("Scanner error reading history list: %v", err)
	}

	type groupInfo struct {
		title    string
		earliest int64
		latest   int64
	}
	groups := make(map[string]*groupInfo)

	for _, entry := range entries {
		if entry.ConversationID == "" {
			continue
		}
		if !s.isWorkspaceMatch(entry.Workspace, activeWorkspaceDir) {
			continue
		}
		info, ok := groups[entry.ConversationID]
		if !ok {
			groups[entry.ConversationID] = &groupInfo{
				title:    entry.Display,
				earliest: entry.Timestamp,
				latest:   entry.Timestamp,
			}
		} else {
			if entry.Timestamp < info.earliest {
				info.earliest = entry.Timestamp
				info.title = entry.Display
			}
			if entry.Timestamp > info.latest {
				info.latest = entry.Timestamp
			}
		}
	}

	list := []ChatHistoryItem{}
	for id, info := range groups {
		title := info.title
		if len(title) > 60 {
			title = title[:57] + "..."
		}
		list = append(list, ChatHistoryItem{
			ID:        id,
			Title:     title,
			Timestamp: info.latest,
		})
	}

	// Sort descending (newest first)
	for i := 0; i < len(list); i++ {
		for j := i + 1; j < len(list); j++ {
			if list[i].Timestamp < list[j].Timestamp {
				list[i], list[j] = list[j], list[i]
			}
		}
	}

	return list, nil
}

// GetHistoryDetail reads detail messages of a conversation from transcript.jsonl
func (s *Service) GetHistoryDetail(id string) (ChatHistoryDetail, error) {
	homeDir, err := getHomeDir()
	if err != nil {
		return ChatHistoryDetail{}, err
	}

	id = filepath.Base(id) // Prevent directory traversal

	transcriptPath := filepath.Join(homeDir, ".gemini", "antigravity-cli", "brain", id, ".system_generated", "logs", "transcript.jsonl")
	file, err := os.Open(transcriptPath)
	if err != nil {
		return ChatHistoryDetail{
			ID: id,
			Messages: []ChatMessage{
				{
					Role:    "model",
					Content: "⚠️ Berkas detail obrolan (transcript) ora ditemokake ing PC lokal iki. Obrolan iki kemungkinan digawe ing Codespace utawa piranti liyane saengga log riwayate ora sinkron ing kene.",
				},
			},
		}, nil
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	const maxCapacity = 10 * 1024 * 1024 // 10MB
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, maxCapacity)

	messages := []ChatMessage{}
	for scanner.Scan() {
		lineBytes := scanner.Bytes()
		if len(lineBytes) == 0 {
			continue
		}

		var line TranscriptLine
		if err := json.Unmarshal(lineBytes, &line); err != nil {
			continue
		}

		if line.Type == "USER_INPUT" && line.Content != "" {
			messages = append(messages, ChatMessage{
				Role:      "user",
				Content:   line.Content,
				Timestamp: line.CreatedAt,
			})
		} else if line.Type == "PLANNER_RESPONSE" {
			var sb strings.Builder

			if line.Thinking != "" {
				sb.WriteString(`<details class="bg-brand-dark/40 border border-brand-border rounded-xl p-3 my-2 text-xs">
<summary class="cursor-pointer font-mono font-semibold text-slate-400 hover:text-white transition flex items-center space-x-2 select-none">
    <i data-lucide="brain" class="w-3.5 h-3.5 text-brand-accent animate-pulse shrink-0"></i>
    <span>Proses Mikir (Thought Process)</span>
</summary>
<div class="mt-2 text-slate-300 leading-relaxed font-sans whitespace-pre-line">`)
				sb.WriteString(strings.TrimSpace(line.Thinking))
				sb.WriteString(`</div>
</details>
`)
			}

			for _, tc := range line.ToolCalls {
				toolLabel := "Tool Execution"
				iconColor := "text-brand-accent"
				iconName := "play"

				if strings.Contains(tc.Name, "Read") || strings.Contains(tc.Name, "view_file") {
					toolLabel = "Moco Berkas (Read)"
					iconColor = "text-blue-400"
					iconName = "file-text"
				} else if strings.Contains(tc.Name, "Edit") || strings.Contains(tc.Name, "Write") || strings.Contains(tc.Name, "replace_file_content") || strings.Contains(tc.Name, "write_to_file") {
					toolLabel = "Nulis/Edit Berkas (Write)"
					iconColor = "text-yellow-400"
					iconName = "edit-3"
				} else if strings.Contains(tc.Name, "Search") || strings.Contains(tc.Name, "Grep") || strings.Contains(tc.Name, "ListDir") || strings.Contains(tc.Name, "list_dir") || strings.Contains(tc.Name, "grep_search") {
					toolLabel = "Mriksa Folder/Grep (Search)"
					iconColor = "text-purple-400"
					iconName = "search"
				} else if strings.Contains(tc.Name, "Bash") || strings.Contains(tc.Name, "run_command") {
					toolLabel = "Perintah Terminal (Bash)"
					iconColor = "text-green-400"
					iconName = "terminal"
				}

				argsBytes, _ := json.MarshalIndent(tc.Arguments, "", "  ")

				sb.WriteString(fmt.Sprintf(`<details class="bg-brand-dark/60 border border-brand-border rounded-xl p-3 my-3 text-xs shadow-inner">
<summary class="cursor-pointer font-mono font-semibold text-slate-200 hover:text-white transition flex items-center justify-between select-none">
    <div class="flex items-center space-x-2">
        <i data-lucide="%s" class="w-4 h-4 %s shrink-0"></i>
        <span class="text-slate-400">[%s]</span>
        <span class="text-brand-accent">%s</span>
    </div>
    <span class="text-[10px] text-slate-500 font-normal italic">(klik kanggo ndelok detail)</span>
</summary>
<pre class="mt-2.5 p-3 bg-black/40 text-slate-300 rounded-lg border border-brand-border overflow-x-auto text-[11px] leading-relaxed code-font font-mono">
%s
</pre>
</details>
`, iconName, iconColor, toolLabel, tc.Name, string(argsBytes)))
			}

			if line.Content != "" {
				sb.WriteString(line.Content)
			}

			builtContent := sb.String()
			if strings.TrimSpace(builtContent) != "" {
				messages = append(messages, ChatMessage{
					Role:      "model",
					Content:   builtContent,
					Timestamp: line.CreatedAt,
				})
			}
		}
	}

	if err := scanner.Err(); err != nil {
		log.Printf("Scanner error reading transcript detail: %v", err)
	}

	return ChatHistoryDetail{
		ID:       id,
		Messages: messages,
	}, nil
}

// StartChat spawns a chat command and returns its stdout pipe
func (s *Service) StartChat(ctx context.Context, req ChatRequest, activeWorkspaceDir string) (*exec.Cmd, io.ReadCloser, error) {
	if strings.HasPrefix(req.Model, "openai/") {
		reader, err := s.StartOpenAIChat(ctx, &req, activeWorkspaceDir)
		return nil, reader, err
	}

	args := []string{"--add-dir", activeWorkspaceDir}
	if req.Model != "" {
		modelArg := strings.TrimSpace(req.Model)
		fields := strings.Fields(modelArg)
		if len(fields) > 0 {
			modelArg = fields[0]
		}
		args = append(args, "--model", modelArg)
	}
	args = append(args, "--print", req.Prompt, "--dangerously-skip-permissions")

	if req.Conversation != "" {
		args = append(args, "--conversation", req.Conversation)
	} else if req.Continue {
		args = append(args, "--continue")
	}

	// Use background context with a 1-hour timeout to prevent premature process termination
	// when request context is cancelled (due to HTTP timeout or tab sleep).
	cmdCtx, cmdCancel := context.WithTimeout(context.Background(), 1*time.Hour)

	cmd := exec.CommandContext(cmdCtx, auth.FindAgyPath(), args...)
	cmd.Dir = activeWorkspaceDir
	cmd.Env = os.Environ()

	convID := req.Conversation
	if convID != "" {
		s.mu.Lock()
		if oldCmd, exists := s.activeChatCmds[convID]; exists && oldCmd != nil && oldCmd.Process != nil {
			_ = oldCmd.Process.Kill()
		}
		if oldCancel, exists := s.activeChatCancels[convID]; exists && oldCancel != nil {
			oldCancel()
		}
		s.activeChatCmds[convID] = cmd
		s.activeChatCancels[convID] = cmdCancel
		s.mu.Unlock()
	}

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		if convID != "" {
			s.mu.Lock()
			delete(s.activeChatCmds, convID)
			delete(s.activeChatCancels, convID)
			s.mu.Unlock()
		}
		cmdCancel()
		return nil, nil, err
	}
	cmd.Stderr = cmd.Stdout

	if err := cmd.Start(); err != nil {
		if convID != "" {
			s.mu.Lock()
			delete(s.activeChatCmds, convID)
			delete(s.activeChatCancels, convID)
			s.mu.Unlock()
		}
		cmdCancel()
		return nil, nil, err
	}

	return cmd, stdoutPipe, nil
}

// CleanupChat removes chat command reference from active list
func (s *Service) CleanupChat(convID string, cmd *exec.Cmd) {
	if convID == "" {
		return
	}
	s.mu.Lock()
	if cmd != nil && s.activeChatCmds[convID] == cmd {
		delete(s.activeChatCmds, convID)
	}
	if _, exists := s.activeChatCancels[convID]; exists {
		delete(s.activeChatCancels, convID)
	}
	s.mu.Unlock()
}

// StopChat terminates an active chat command process
func (s *Service) StopChat(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	stopped := false
	cmd, exists := s.activeChatCmds[id]
	if exists && cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
		delete(s.activeChatCmds, id)
		stopped = true
	}

	if cancel, exists := s.activeChatCancels[id]; exists {
		cancel()
		delete(s.activeChatCancels, id)
		stopped = true
	}

	return stopped
}

// Helper methods to support OpenAI history saving
func (s *Service) appendToHistory(entry HistoryEntry) error {
	historyPath, err := s.getHistoryFilePath()
	if err != nil {
		return err
	}
	_ = os.MkdirAll(filepath.Dir(historyPath), 0755)

	f, err := os.OpenFile(historyPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()

	bytes, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	_, err = f.Write(append(bytes, '\n'))
	return err
}

func (s *Service) appendToTranscript(convID string, line TranscriptLine) error {
	homeDir, err := getHomeDir()
	if err != nil {
		return err
	}
	id := filepath.Base(convID)
	dir := filepath.Join(homeDir, ".gemini", "antigravity-cli", "brain", id, ".system_generated", "logs")
	_ = os.MkdirAll(dir, 0755)

	transcriptPath := filepath.Join(dir, "transcript.jsonl")
	f, err := os.OpenFile(transcriptPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()

	bytes, err := json.Marshal(line)
	if err != nil {
		return err
	}
	_, err = f.Write(append(bytes, '\n'))
	return err
}

func agyCompatibilitySystemPrompt(activeWorkspaceDir string) string {
	var sb strings.Builder
	sb.WriteString("You are Mobile AGY running in OpenAI-compatible fallback mode. ")
	sb.WriteString("The user selected an external OpenAI-compatible model because the original Antigravity quota may be exhausted, but they still expect the AGY coding-assistant behavior.\n\n")
	sb.WriteString("Follow the original AGY style and workflow as closely as possible:\n")
	sb.WriteString("- Act as a senior autonomous coding agent inside the user's workspace.\n")
	sb.WriteString("- Be concise, direct, and technically accurate.\n")
	sb.WriteString("- Use the provided workspace path, file snapshot, and conversation transcript as your working context.\n")
	sb.WriteString("- Prefer root-cause fixes, minimal focused changes, and existing project patterns.\n")
	sb.WriteString("- If the user asks for code, provide complete practical code or exact file-level guidance.\n")
	sb.WriteString("- Do not pretend you executed terminal commands, read files, edited files, or used AGY tools unless that evidence is explicitly present in the conversation.\n")
	sb.WriteString("- If a task needs real filesystem/tool execution that the OpenAI-compatible API cannot perform directly, say what should be run or changed instead of fabricating results.\n")
	sb.WriteString("- For tasks/features related to \"Kelola Produk Kontrol Panel\", ensure all requirements are saved in a Markdown (.md) file inside the \"asset-marketing\" folder.\n")
	sb.WriteString("- Preserve Indonesian/Javanese tone when the user uses it.\n\n")
	sb.WriteString("Active workspace: ")
	sb.WriteString(activeWorkspaceDir)
	return sb.String()
}

func buildWorkspaceSnapshot(activeWorkspaceDir string) string {
	if strings.TrimSpace(activeWorkspaceDir) == "" {
		return "Workspace snapshot unavailable: active workspace is empty."
	}

	info, err := os.Stat(activeWorkspaceDir)
	if err != nil || !info.IsDir() {
		return fmt.Sprintf("Workspace snapshot unavailable for %q: %v", activeWorkspaceDir, err)
	}

	skippedDirs := map[string]bool{
		".git": true, ".zed": true, "node_modules": true, "vendor": true, "dist": true, "build": true,
		".next": true, ".nuxt": true, "coverage": true, "tmp": true, "temp": true,
	}
	const maxFiles = 160
	var files []string

	_ = filepath.WalkDir(activeWorkspaceDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			if path != activeWorkspaceDir && skippedDirs[name] {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(name, ".") || strings.HasSuffix(name, ".exe") || strings.HasSuffix(name, ".dll") || strings.HasSuffix(name, ".so") || strings.HasSuffix(name, ".zip") {
			return nil
		}
		rel, relErr := filepath.Rel(activeWorkspaceDir, path)
		if relErr == nil {
			files = append(files, filepath.ToSlash(rel))
		}
		if len(files) >= maxFiles {
			return io.EOF
		}
		return nil
	})

	if len(files) == 0 {
		return "Workspace snapshot: no visible source files found."
	}
	return "Workspace file snapshot (read-only context, first files only):\n- " + strings.Join(files, "\n- ")
}

func cleanWorkspaceRelPath(pathParam string) string {
	pathParam = strings.TrimSpace(pathParam)
	pathParam = strings.Trim(pathParam, "\"'")
	pathParam = strings.ReplaceAll(pathParam, "\\", string(filepath.Separator))
	if filepath.IsAbs(pathParam) {
		return pathParam
	}
	return filepath.Clean(pathParam)
}

func resolveWorkspacePath(activeWorkspaceDir, pathParam string) (string, string, error) {
	if strings.TrimSpace(pathParam) == "" || pathParam == "." {
		return activeWorkspaceDir, ".", nil
	}
	cleaned := cleanWorkspaceRelPath(pathParam)
	var absPath string
	if filepath.IsAbs(cleaned) {
		absPath = filepath.Clean(cleaned)
	} else {
		absPath = filepath.Join(activeWorkspaceDir, cleaned)
	}
	activeClean := filepath.Clean(activeWorkspaceDir)
	if absPath != activeClean && !strings.HasPrefix(absPath, activeClean+string(filepath.Separator)) {
		return "", "", fmt.Errorf("Access Denied: path must stay inside workspace")
	}
	rel, err := filepath.Rel(activeClean, absPath)
	if err != nil {
		return "", "", err
	}
	return absPath, filepath.ToSlash(rel), nil
}

func openAIToolDefinitions() []map[string]any {
	return []map[string]any{
		{
			"type": "function",
			"function": map[string]any{
				"name":        "list_dir",
				"description": "List files and folders inside the active workspace, similar to AGY list_dir.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path": map[string]any{"type": "string", "description": "Workspace-relative directory path. Use . for root."},
					},
				},
			},
		},
		{
			"type": "function",
			"function": map[string]any{
				"name":        "view_file",
				"description": "Read a text file from the active workspace, similar to AGY view_file.",
				"parameters": map[string]any{
					"type":     "object",
					"required": []string{"path"},
					"properties": map[string]any{
						"path":       map[string]any{"type": "string", "description": "Workspace-relative file path."},
						"start_line": map[string]any{"type": "integer", "description": "Optional 1-based start line."},
						"end_line":   map[string]any{"type": "integer", "description": "Optional 1-based end line."},
					},
				},
			},
		},
		{
			"type": "function",
			"function": map[string]any{
				"name":        "write_to_file",
				"description": "Create or overwrite a file inside the active workspace.",
				"parameters": map[string]any{
					"type":     "object",
					"required": []string{"path", "content"},
					"properties": map[string]any{
						"path":    map[string]any{"type": "string", "description": "Workspace-relative file path."},
						"content": map[string]any{"type": "string", "description": "Full new file content."},
					},
				},
			},
		},
		{
			"type": "function",
			"function": map[string]any{
				"name":        "replace_file_content",
				"description": "Replace exact text inside a workspace file. Prefer this for focused edits.",
				"parameters": map[string]any{
					"type":     "object",
					"required": []string{"path", "old_text", "new_text"},
					"properties": map[string]any{
						"path":     map[string]any{"type": "string", "description": "Workspace-relative file path."},
						"old_text": map[string]any{"type": "string", "description": "Exact text to replace."},
						"new_text": map[string]any{"type": "string", "description": "Replacement text."},
					},
				},
			},
		},
		{
			"type": "function",
			"function": map[string]any{
				"name":        "grep_search",
				"description": "Search text files in the active workspace using a regular expression.",
				"parameters": map[string]any{
					"type":     "object",
					"required": []string{"pattern"},
					"properties": map[string]any{
						"pattern": map[string]any{"type": "string", "description": "Go regexp pattern."},
						"path":    map[string]any{"type": "string", "description": "Optional workspace-relative subtree."},
					},
				},
			},
		},
		{
			"type": "function",
			"function": map[string]any{
				"name":        "run_command",
				"description": "Run a short non-interactive command in the active workspace. Use mainly for tests, builds, and git read-only checks.",
				"parameters": map[string]any{
					"type":     "object",
					"required": []string{"command"},
					"properties": map[string]any{
						"command": map[string]any{"type": "string", "description": "Command to run."},
					},
				},
			},
		},
	}
}

func agyToolType(name string) string {
	switch name {
	case "list_dir":
		return "LIST_DIRECTORY"
	case "view_file":
		return "VIEW_FILE"
	case "write_to_file", "replace_file_content":
		return "WRITE_FILE"
	case "grep_search":
		return "GREP_SEARCH"
	case "run_command":
		return "COMMAND"
	default:
		return "TOOL_RESULT"
	}
}

func formatToolResult(name, body string, started time.Time) string {
	return fmt.Sprintf("Created At: %s\nCompleted At: %s\n%s", started.Format("2006-01-02T15:04:05Z07:00"), time.Now().Format("2006-01-02T15:04:05Z07:00"), body)
}

func (s *Service) executeOpenAITool(ctx context.Context, activeWorkspaceDir string, call openAIToolCall) (string, ToolCall) {
	name := call.Function.Name
	started := time.Now()
	var args map[string]any
	if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
		result := formatToolResult(name, "Error: invalid tool arguments: "+err.Error(), started)
		return result, ToolCall{Name: name, ToolAction: "Invalid arguments", ToolSummary: "Tool failed", Arguments: call.Function.Arguments}
	}

	toolCall := ToolCall{Name: name, ToolAction: "Executing " + name, ToolSummary: "OpenAI-compatible tool call", Arguments: args}
	resultBody := ""
	fail := func(err error) string {
		return "Error: " + err.Error()
	}
	argString := func(key string) string {
		if v, ok := args[key].(string); ok {
			return v
		}
		return ""
	}
	argInt := func(key string) int {
		switch v := args[key].(type) {
		case float64:
			return int(v)
		case int:
			return v
		default:
			return 0
		}
	}

	switch name {
	case "list_dir":
		pathArg := argString("path")
		if pathArg == "" {
			pathArg = "."
		}
		absPath, _, err := resolveWorkspacePath(activeWorkspaceDir, pathArg)
		if err != nil {
			resultBody = fail(err)
			break
		}
		entries, err := os.ReadDir(absPath)
		if err != nil {
			resultBody = fail(err)
			break
		}
		sort.Slice(entries, func(i, j int) bool {
			if entries[i].IsDir() != entries[j].IsDir() {
				return entries[i].IsDir()
			}
			return strings.ToLower(entries[i].Name()) < strings.ToLower(entries[j].Name())
		})
		var sb strings.Builder
		dirs, files := 0, 0
		for i, entry := range entries {
			if i >= 200 {
				sb.WriteString("... truncated after 200 entries\n")
				break
			}
			info, _ := entry.Info()
			if entry.IsDir() {
				dirs++
				sb.WriteString(fmt.Sprintf("{\"name\":%q, \"type\":\"directory\"}\n", entry.Name()))
			} else {
				files++
				size := int64(0)
				if info != nil {
					size = info.Size()
				}
				sb.WriteString(fmt.Sprintf("{\"name\":%q, \"sizeBytes\":%q}\n", entry.Name(), fmt.Sprintf("%d", size)))
			}
		}
		sb.WriteString(fmt.Sprintf("\nSummary: This directory contains %d subdirectories and %d files.", dirs, files))
		resultBody = sb.String()

	case "view_file":
		absPath, rel, err := resolveWorkspacePath(activeWorkspaceDir, argString("path"))
		if err != nil {
			resultBody = fail(err)
			break
		}
		data, err := os.ReadFile(absPath)
		if err != nil {
			resultBody = fail(err)
			break
		}
		content := string(data)
		lines := strings.Split(content, "\n")
		startLine := argInt("start_line")
		endLine := argInt("end_line")
		if startLine <= 0 {
			startLine = 1
		}
		if endLine <= 0 || endLine > len(lines) {
			endLine = len(lines)
		}
		if startLine > endLine {
			resultBody = fmt.Sprintf("File: %s\n(no lines in requested range)", rel)
			break
		}
		var sb strings.Builder
		sb.WriteString("File: " + rel + "\n")
		for i := startLine; i <= endLine && i <= startLine+399; i++ {
			sb.WriteString(fmt.Sprintf("%6d\t%s\n", i, lines[i-1]))
		}
		if endLine-startLine >= 400 {
			sb.WriteString("... truncated after 400 lines\n")
		}
		resultBody = sb.String()

	case "write_to_file":
		absPath, rel, err := resolveWorkspacePath(activeWorkspaceDir, argString("path"))
		if err != nil {
			resultBody = fail(err)
			break
		}
		if err := os.MkdirAll(filepath.Dir(absPath), 0755); err != nil {
			resultBody = fail(err)
			break
		}
		if err := os.WriteFile(absPath, []byte(argString("content")), 0644); err != nil {
			resultBody = fail(err)
			break
		}
		resultBody = "Wrote file: " + rel

	case "replace_file_content":
		absPath, rel, err := resolveWorkspacePath(activeWorkspaceDir, argString("path"))
		if err != nil {
			resultBody = fail(err)
			break
		}
		data, err := os.ReadFile(absPath)
		if err != nil {
			resultBody = fail(err)
			break
		}
		oldText := argString("old_text")
		if oldText == "" {
			resultBody = "Error: old_text is empty"
			break
		}
		content := string(data)
		if !strings.Contains(content, oldText) {
			resultBody = "Error: old_text not found in " + rel
			break
		}
		updated := strings.Replace(content, oldText, argString("new_text"), 1)
		if err := os.WriteFile(absPath, []byte(updated), 0644); err != nil {
			resultBody = fail(err)
			break
		}
		resultBody = "Replaced content in: " + rel

	case "grep_search":
		pattern := argString("pattern")
		re, err := regexp.Compile(pattern)
		if err != nil {
			resultBody = fail(err)
			break
		}
		rootPath := activeWorkspaceDir
		if pathArg := argString("path"); pathArg != "" {
			rootPath, _, err = resolveWorkspacePath(activeWorkspaceDir, pathArg)
			if err != nil {
				resultBody = fail(err)
				break
			}
		}
		var matches []string
		_ = filepath.WalkDir(rootPath, func(path string, d os.DirEntry, err error) error {
			if err != nil || len(matches) >= 80 {
				return nil
			}
			name := d.Name()
			if d.IsDir() {
				if strings.HasPrefix(name, ".") || name == "node_modules" || name == "vendor" || name == "dist" || name == "build" {
					return filepath.SkipDir
				}
				return nil
			}
			if strings.HasSuffix(name, ".exe") || strings.HasSuffix(name, ".dll") || strings.HasSuffix(name, ".zip") || strings.HasSuffix(name, ".png") || strings.HasSuffix(name, ".jpg") {
				return nil
			}
			data, readErr := os.ReadFile(path)
			if readErr != nil || bytes.Contains(data, []byte{0}) {
				return nil
			}
			rel, _ := filepath.Rel(activeWorkspaceDir, path)
			for idx, line := range strings.Split(string(data), "\n") {
				if re.MatchString(line) {
					matches = append(matches, fmt.Sprintf("%s:%d: %s", filepath.ToSlash(rel), idx+1, strings.TrimSpace(line)))
					if len(matches) >= 80 {
						break
					}
				}
			}
			return nil
		})
		if len(matches) == 0 {
			resultBody = "No matches found."
		} else {
			resultBody = strings.Join(matches, "\n")
		}

	case "run_command":
		command := strings.TrimSpace(argString("command"))
		if command == "" {
			resultBody = "Error: command is empty"
			break
		}
		commandLower := strings.ToLower(command)
		blocked := []string{" rm ", " del ", " rmdir ", " format ", " shutdown", " reboot", "git push", "git reset", "git clean", "sudo ", "su "}
		padded := " " + commandLower + " "
		for _, token := range blocked {
			if strings.Contains(padded, token) {
				resultBody = "Error: command blocked by OpenAI-compatible safety policy: " + command
				return formatToolResult(name, resultBody, started), toolCall
			}
		}
		cmdCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		var cmd *exec.Cmd
		if runtime.GOOS == "windows" {
			if _, err := exec.LookPath("bash"); err == nil {
				cmd = exec.CommandContext(cmdCtx, "bash", "-c", command)
			} else if _, err := exec.LookPath("powershell"); err == nil {
				cmd = exec.CommandContext(cmdCtx, "powershell", "-Command", command)
			} else {
				cmd = exec.CommandContext(cmdCtx, "cmd", "/c", command)
			}
		} else {
			cmd = exec.CommandContext(cmdCtx, "bash", "-c", command)
		}
		cmd.Dir = activeWorkspaceDir
		cmd.Env = os.Environ()
		out, err := cmd.CombinedOutput()
		text := string(out)
		if len(text) > 12000 {
			text = text[:12000] + "\n... truncated"
		}
		if err != nil {
			resultBody = fmt.Sprintf("Command failed: %v\n%s", err, text)
		} else {
			resultBody = text
			if strings.TrimSpace(resultBody) == "" {
				resultBody = "Command completed with no output."
			}
		}

	default:
		resultBody = "Error: unsupported tool: " + name
	}

	return formatToolResult(name, resultBody, started), toolCall
}

func (s *Service) getOpenAITranscriptMessages(convID string) []openAIMessage {
	homeDir, err := getHomeDir()
	if err != nil || strings.TrimSpace(convID) == "" {
		return nil
	}

	id := filepath.Base(convID)
	transcriptPath := filepath.Join(homeDir, ".gemini", "antigravity-cli", "brain", id, ".system_generated", "logs", "transcript.jsonl")
	file, err := os.Open(transcriptPath)
	if err != nil {
		return nil
	}
	defer file.Close()

	var messages []openAIMessage
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	for scanner.Scan() {
		var line TranscriptLine
		if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
			continue
		}
		content := strings.TrimSpace(line.Content)
		if content == "" {
			continue
		}
		switch line.Type {
		case "USER_INPUT":
			messages = append(messages, openAIMessage{Role: "user", Content: content})
		case "PLANNER_RESPONSE":
			messages = append(messages, openAIMessage{Role: "assistant", Content: content})
		}
	}
	return messages
}

// StartOpenAIChat handles streaming OpenAI compatible completions through the
// same Antigravity brain/history layout, with an AGY-compatible system prompt.
func (s *Service) StartOpenAIChat(ctx context.Context, req *ChatRequest, activeWorkspaceDir string) (io.ReadCloser, error) {
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		return nil, fmt.Errorf("OPENAI_API_KEY environment variable is not set")
	}

	apiBase := os.Getenv("OPENAI_API_BASE")
	if apiBase == "" {
		apiBase = "https://api.openai.com/v1"
	}

	modelName := strings.TrimPrefix(req.Model, "openai/")

	// 1. Manage conversation ID
	if req.Conversation == "" {
		req.Conversation = fmt.Sprintf("openai-%d", time.Now().UnixNano())

		title := req.Prompt
		if len(title) > 50 {
			title = title[:47] + "..."
		}
		_ = s.appendToHistory(HistoryEntry{
			Display:        title,
			Timestamp:      time.Now().Unix(),
			Workspace:      activeWorkspaceDir,
			ConversationID: req.Conversation,
		})
	}

	// 2. Load AGY-compatible context and raw transcript history.
	type openAIResponse struct {
		Choices []struct {
			Message struct {
				Role             string           `json:"role"`
				Content          string           `json:"content"`
				ReasoningContent string           `json:"reasoning_content"`
				ToolCalls        []openAIToolCall `json:"tool_calls"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Error *struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
	}

	messages := []openAIMessage{
		{Role: "system", Content: agyCompatibilitySystemPrompt(activeWorkspaceDir)},
		{Role: "system", Content: "You have AGY-like tools available. Use tools whenever the user asks to inspect, create, edit, test, or run code in the workspace. Do not merely provide code snippets when a file change is requested; call write_to_file or replace_file_content. After tool calls, summarize exactly what changed."},
		{Role: "system", Content: buildWorkspaceSnapshot(activeWorkspaceDir)},
	}
	messages = append(messages, s.getOpenAITranscriptMessages(req.Conversation)...)
	messages = append(messages, openAIMessage{Role: "user", Content: req.Prompt})

	openaiCtx, cancel := context.WithCancel(ctx)
	s.mu.Lock()
	s.activeChatCancels[req.Conversation] = cancel
	s.mu.Unlock()

	pr, pw := io.Pipe()
	client := &http.Client{}
	url := strings.TrimSuffix(apiBase, "/") + "/chat/completions"

	go func() {
		defer cancel()
		defer func() {
			s.mu.Lock()
			delete(s.activeChatCancels, req.Conversation)
			s.mu.Unlock()
		}()
		defer pw.Close()

		// Append the user query to transcript log immediately.
		_ = s.appendToTranscript(req.Conversation, TranscriptLine{
			Source:    "user",
			Type:      "USER_INPUT",
			Content:   req.Prompt,
			CreatedAt: time.Now().Format("2006-01-02T15:04:05Z07:00"),
		})

		maxToolRounds := 12
		if raw := strings.TrimSpace(os.Getenv("OPENAI_MAX_TOOL_ROUNDS")); raw != "" {
			var parsed int
			if _, err := fmt.Sscanf(raw, "%d", &parsed); err == nil && parsed > 0 {
				maxToolRounds = parsed
			}
		}

		var accumulatedContent strings.Builder
		var accumulatedThinking strings.Builder
		var transcriptTools []ToolCall

		writeError := func(format string, args ...any) {
			_, _ = pw.Write([]byte("\n\n[OpenAI-compatible error] " + fmt.Sprintf(format, args...) + "\n"))
		}

		callModel := func() (openAIResponse, error) {
			reqBody, err := json.Marshal(map[string]any{
				"model":       modelName,
				"messages":    messages,
				"tools":       openAIToolDefinitions(),
				"tool_choice": "auto",
				"stream":      false,
			})
			if err != nil {
				return openAIResponse{}, err
			}

			httpReq, err := http.NewRequestWithContext(openaiCtx, http.MethodPost, url, bytes.NewReader(reqBody))
			if err != nil {
				return openAIResponse{}, err
			}
			httpReq.Header.Set("Content-Type", "application/json")
			httpReq.Header.Set("Authorization", "Bearer "+apiKey)

			resp, err := client.Do(httpReq)
			if err != nil {
				return openAIResponse{}, err
			}
			defer resp.Body.Close()

			bodyBytes, _ := io.ReadAll(resp.Body)
			if resp.StatusCode != http.StatusOK {
				return openAIResponse{}, fmt.Errorf("API status %d: %s", resp.StatusCode, strings.TrimSpace(string(bodyBytes)))
			}

			var parsed openAIResponse
			if err := json.Unmarshal(bodyBytes, &parsed); err != nil {
				return openAIResponse{}, err
			}
			if parsed.Error != nil && parsed.Error.Message != "" {
				return openAIResponse{}, fmt.Errorf("%s", parsed.Error.Message)
			}
			return parsed, nil
		}

		for round := 0; round < maxToolRounds; round++ {
			parsed, err := callModel()
			if err != nil {
				if openaiCtx.Err() != nil {
					return
				}
				writeError("%v", err)
				return
			}
			if len(parsed.Choices) == 0 {
				writeError("API ora mbalekake choices")
				return
			}

			msg := parsed.Choices[0].Message
			if msg.ReasoningContent != "" {
				accumulatedThinking.WriteString(msg.ReasoningContent)
				_, _ = pw.Write([]byte("▸ Thought\n"))
				for _, l := range strings.Split(msg.ReasoningContent, "\n") {
					_, _ = pw.Write([]byte("  " + l + "\n"))
				}
				_, _ = pw.Write([]byte("\n"))
			}

			assistantMsg := openAIMessage{Role: "assistant", Content: msg.Content, ToolCalls: msg.ToolCalls}
			messages = append(messages, assistantMsg)

			if len(msg.ToolCalls) == 0 {
				if msg.Content != "" {
					accumulatedContent.WriteString(msg.Content)
					_, _ = pw.Write([]byte(msg.Content))
				}
				break
			}

			for _, toolCall := range msg.ToolCalls {
				if toolCall.ID == "" {
					toolCall.ID = fmt.Sprintf("call_%d_%d", round, len(transcriptTools))
				}
				if toolCall.Type == "" {
					toolCall.Type = "function"
				}
				_, _ = pw.Write([]byte(fmt.Sprintf("\n● %s\n", toolCall.Function.Name)))
				result, transcriptTool := s.executeOpenAITool(openaiCtx, activeWorkspaceDir, toolCall)
				transcriptTools = append(transcriptTools, transcriptTool)
				_, _ = pw.Write([]byte(result + "\n"))
				messages = append(messages, openAIMessage{
					Role:       "tool",
					ToolCallID: toolCall.ID,
					Content:    result,
				})
				_ = s.appendToTranscript(req.Conversation, TranscriptLine{
					Source:    "model",
					Type:      agyToolType(toolCall.Function.Name),
					Content:   result,
					CreatedAt: time.Now().Format("2006-01-02T15:04:05Z07:00"),
				})
			}
		}

		if strings.TrimSpace(accumulatedContent.String()) == "" {
			_, _ = pw.Write([]byte("\n[OpenAI-compatible warning] Model mandheg tanpa final answer sawise tool execution.\n"))
		}

		// Write the completed tool-loop response to transcript log once.
		_ = s.appendToTranscript(req.Conversation, TranscriptLine{
			Source:    "model",
			Type:      "PLANNER_RESPONSE",
			Content:   accumulatedContent.String(),
			CreatedAt: time.Now().Format("2006-01-02T15:04:05Z07:00"),
			Thinking:  accumulatedThinking.String(),
			ToolCalls: transcriptTools,
		})
	}()

	return pr, nil
}

// DeleteChat deletes chat brain directory and history entry
func (s *Service) DeleteChat(id string) error {
	// 1. Terminate running process if any
	s.StopChat(id)

	// 2. Remove brain dir
	homeDir, err := getHomeDir()
	if err == nil {
		brainDir := filepath.Join(homeDir, ".gemini", "antigravity-cli", "brain", filepath.Base(id))
		_ = os.RemoveAll(brainDir)
	}

	// 3. Remove history entry
	historyPath, err := s.getHistoryFilePath()
	if err != nil {
		return err
	}

	file, err := os.Open(historyPath)
	if err != nil {
		return nil // No history file to delete from
	}

	var newLines []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		var entry HistoryEntry
		if err := json.Unmarshal([]byte(line), &entry); err == nil {
			if entry.ConversationID == id {
				continue // Skip the matching conversation entries
			}
		}
		newLines = append(newLines, line)
	}
	file.Close()

	// Rewrite history file
	err = os.WriteFile(historyPath, []byte(strings.Join(newLines, "\n")+"\n"), 0644)
	return err
}
