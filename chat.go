package main

import (
	"bufio"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

type ChatRequest struct {
	Prompt       string `json:"prompt"`
	Model        string `json:"model"`
	Continue     bool   `json:"continue"`
	Conversation string `json:"conversation"`
}

// Helper struct kanggo riwayat chat
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

type ChatHistoryDetail struct {
	ID       string        `json:"id"`
	Messages []ChatMessage `json:"messages"`
}

type TranscriptLine struct {
	Source    string `json:"source"`
	Type      string `json:"type"`
	Content   string `json:"content"`
	CreatedAt string `json:"created_at"`
}

var activeChatCmds = make(map[string]*exec.Cmd)
var activeChatCmdsMu sync.Mutex

func getHistoryFilePath() (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(homeDir, ".gemini", "antigravity-cli", "history.jsonl"), nil
}

func isWorkspaceMatch(w1, w2 string) bool {
	p1 := filepath.Clean(w1)
	p2 := filepath.Clean(w2)
	if p1 == p2 {
		return true
	}
	// Cek yen salah siji minangka subfolder saka liyane
	return strings.HasPrefix(p1, p2+string(filepath.Separator)) || strings.HasPrefix(p2, p1+string(filepath.Separator))
}

// API GET /api/chat/history - Maca daftar riwayat obrolan saka history.jsonl
func handleChatHistoryList(w http.ResponseWriter, r *http.Request) {
	historyPath, err := getHistoryFilePath()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	file, err := os.Open(historyPath)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("[]"))
		return
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
		// Filter by active workspace to avoid path mismatch (allowing subdirectory match)
		if !isWorkspaceMatch(entry.Workspace, activeWorkspaceDir) {
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

	// Sortir descending (paling anyar ing dhuwur)
	for i := 0; i < len(list); i++ {
		for j := i + 1; j < len(list); j++ {
			if list[i].Timestamp < list[j].Timestamp {
				list[i], list[j] = list[j], list[i]
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(list)
}

// API GET /api/chat/history/detail - Maca isi obrolan saka transcript.jsonl
func handleChatHistoryDetail(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		http.Error(w, "missing id parameter", http.StatusBadRequest)
		return
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	id = filepath.Base(id) // Nyegah directory traversal

	transcriptPath := filepath.Join(homeDir, ".gemini", "antigravity-cli", "brain", id, ".system_generated", "logs", "transcript.jsonl")
	file, err := os.Open(transcriptPath)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ChatHistoryDetail{
			ID: id,
			Messages: []ChatMessage{
				{
					Role:    "model",
					Content: "⚠️ Berkas detail obrolan (transcript) ora ditemokake ing PC lokal iki. Obrolan iki kemungkinan digawe ing Codespace utawa piranti liyane saengga log riwayate ora sinkron ing kene.",
				},
			},
		})
		return
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	// Set larger buffer size in case of long lines
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
		} else if line.Type == "PLANNER_RESPONSE" && line.Content != "" {
			messages = append(messages, ChatMessage{
				Role:      "model",
				Content:   line.Content,
				Timestamp: line.CreatedAt,
			})
		}
	}

	if err := scanner.Err(); err != nil {
		log.Printf("Scanner error reading transcript detail: %v", err)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(ChatHistoryDetail{
		ID:       id,
		Messages: messages,
	})
}

// Handler chat streaming
func handleChatStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req ChatRequest
	if strings.Contains(r.Header.Get("Content-Type"), "application/json") {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
	} else {
		req.Prompt = r.FormValue("prompt")
		req.Model = r.FormValue("model")
		req.Continue = r.FormValue("continue") == "true"
		req.Conversation = r.FormValue("conversation")
	}

	if req.Prompt == "" {
		http.Error(w, "missing prompt parameter", http.StatusBadRequest)
		return
	}

	args := []string{"--add-dir", activeWorkspaceDir}
	if req.Model != "" {
		args = append(args, "--model", req.Model)
	}
	args = append(args, "--print", req.Prompt, "--dangerously-skip-permissions")

	if req.Conversation != "" {
		args = append(args, "--conversation", req.Conversation)
	} else if req.Continue {
		args = append(args, "--continue")
	}

	cmd := exec.CommandContext(r.Context(), findAgyPath(), args...)
	cmd.Dir = activeWorkspaceDir
	cmd.Env = os.Environ()

	convId := req.Conversation
	if convId != "" {
		activeChatCmdsMu.Lock()
		if oldCmd, exists := activeChatCmds[convId]; exists && oldCmd != nil && oldCmd.Process != nil {
			oldCmd.Process.Kill()
		}
		activeChatCmds[convId] = cmd
		activeChatCmdsMu.Unlock()

		defer func() {
			activeChatCmdsMu.Lock()
			if activeChatCmds[convId] == cmd {
				delete(activeChatCmds, convId)
			}
			activeChatCmdsMu.Unlock()
		}()
	}

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	cmd.Stderr = cmd.Stdout

	if err := cmd.Start(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	// Flush headers immediately so client fetch resolves and spinner starts
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	buf := make([]byte, 256)
	for {
		n, err := stdoutPipe.Read(buf)
		if n > 0 {
			w.Write(buf[:n])
			flusher.Flush()
		}
		if err != nil {
			break
		}
	}

	cmd.Wait()
}

// API POST /api/chat/stop - Mateni proses agen sing lagi mlaku
func handleChatStop(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		http.Error(w, "missing id parameter", http.StatusBadRequest)
		return
	}

	activeChatCmdsMu.Lock()
	cmd, exists := activeChatCmds[id]
	if exists && cmd != nil && cmd.Process != nil {
		cmd.Process.Kill()
		delete(activeChatCmds, id)
		activeChatCmdsMu.Unlock()
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("Sukses mateni agen"))
		return
	}
	activeChatCmdsMu.Unlock()

	w.WriteHeader(http.StatusOK)
	w.Write([]byte("Agen ora lagi mlaku"))
}

// API DELETE /api/chat/delete - Mbusak agen sarta riwayate
func handleChatDelete(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		http.Error(w, "missing id parameter", http.StatusBadRequest)
		return
	}

	// 1. Mateni proses agen dhisik yen lagi mlaku
	activeChatCmdsMu.Lock()
	if cmd, exists := activeChatCmds[id]; exists && cmd != nil && cmd.Process != nil {
		cmd.Process.Kill()
		delete(activeChatCmds, id)
	}
	activeChatCmdsMu.Unlock()

	// 2. Mbusak folder brain (transcript) saka agen kasebut
	homeDir, err := os.UserHomeDir()
	if err == nil {
		brainDir := filepath.Join(homeDir, ".gemini", "antigravity-cli", "brain", filepath.Base(id))
		os.RemoveAll(brainDir)
	}

	// 3. Mbusak entri saka history.jsonl
	historyPath, err := getHistoryFilePath()
	if err == nil {
		file, err := os.Open(historyPath)
		if err == nil {
			var newLines []string
			scanner := bufio.NewScanner(file)
			for scanner.Scan() {
				line := scanner.Text()
				var entry HistoryEntry
				if err := json.Unmarshal([]byte(line), &entry); err == nil {
					if entry.ConversationID == id {
						continue // Saring (buang) entri sing dicocogake
					}
				}
				newLines = append(newLines, line)
			}
			file.Close()

			// Tulis ulang history.jsonl
			os.WriteFile(historyPath, []byte(strings.Join(newLines, "\n")+"\n"), 0644)
		}
	}

	w.WriteHeader(http.StatusOK)
	w.Write([]byte("Sukses mbusak agen"))
}
