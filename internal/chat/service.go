package chat

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"mobile-agy/internal/auth"
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

type Service struct {
	mu             sync.Mutex
	activeChatCmds map[string]*exec.Cmd
}

func NewService() *Service {
	return &Service{
		activeChatCmds: make(map[string]*exec.Cmd),
	}
}

func (s *Service) getHistoryFilePath() (string, error) {
	homeDir, err := os.UserHomeDir()
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
	homeDir, err := os.UserHomeDir()
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

	return ChatHistoryDetail{
		ID:       id,
		Messages: messages,
	}, nil
}

// StartChat spawns a chat command and returns its stdout pipe
func (s *Service) StartChat(ctx context.Context, req ChatRequest, activeWorkspaceDir string) (*exec.Cmd, io.ReadCloser, error) {
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

	cmd := exec.CommandContext(ctx, auth.FindAgyPath(), args...)
	cmd.Dir = activeWorkspaceDir
	cmd.Env = os.Environ()

	convID := req.Conversation
	if convID != "" {
		s.mu.Lock()
		if oldCmd, exists := s.activeChatCmds[convID]; exists && oldCmd != nil && oldCmd.Process != nil {
			_ = oldCmd.Process.Kill()
		}
		s.activeChatCmds[convID] = cmd
		s.mu.Unlock()
	}

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		if convID != "" {
			s.mu.Lock()
			delete(s.activeChatCmds, convID)
			s.mu.Unlock()
		}
		return nil, nil, err
	}
	cmd.Stderr = cmd.Stdout

	if err := cmd.Start(); err != nil {
		if convID != "" {
			s.mu.Lock()
			delete(s.activeChatCmds, convID)
			s.mu.Unlock()
		}
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
	if s.activeChatCmds[convID] == cmd {
		delete(s.activeChatCmds, convID)
	}
	s.mu.Unlock()
}

// StopChat terminates an active chat command process
func (s *Service) StopChat(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	cmd, exists := s.activeChatCmds[id]
	if exists && cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
		delete(s.activeChatCmds, id)
		return true
	}
	return false
}

// DeleteChat deletes chat brain directory and history entry
func (s *Service) DeleteChat(id string) error {
	// 1. Terminate running process if any
	s.StopChat(id)

	// 2. Remove brain dir
	homeDir, err := os.UserHomeDir()
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
