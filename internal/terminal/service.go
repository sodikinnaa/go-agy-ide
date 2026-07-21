package terminal

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mobile-agy/internal/auth"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Service struct {
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	stdout    io.ReadCloser
	isRunning bool
	mutex     sync.Mutex
	clients   map[chan []byte]bool
	clientMux sync.Mutex
	history   []byte
}

type OpenAISettings struct {
	APIBase          string   `json:"apiBase"`
	APIKey           string   `json:"apiKey,omitempty"`
	APIKeySet        bool     `json:"apiKeySet"`
	APIKeyMasked     string   `json:"apiKeyMasked,omitempty"`
	ConfiguredModels string   `json:"configuredModels"`
	AvailableModels  []string `json:"availableModels,omitempty"`
}

func NewService() *Service {
	return &Service{
		clients: make(map[chan []byte]bool),
	}
}

func (s *Service) StartSession(workspaceDir string) error {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	if s.isRunning {
		return nil
	}

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		if _, err := exec.LookPath("bash"); err == nil {
			cmd = exec.Command("bash", "-i")
		} else if _, err := exec.LookPath("powershell"); err == nil {
			cmd = exec.Command("powershell")
		} else {
			cmd = exec.Command("cmd")
		}
	} else {
		cmd = exec.Command("bash", "-i")
	}

	cmd.Dir = workspaceDir
	cmd.Env = os.Environ()
	cmd.Env = append(cmd.Env, "TERM=xterm-256color")

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		return err
	}
	cmd.Stderr = cmd.Stdout

	if err := cmd.Start(); err != nil {
		stdin.Close()
		stdout.Close()
		return err
	}

	s.cmd = cmd
	s.stdin = stdin
	s.stdout = stdout
	s.isRunning = true

	// Read loop
	go func() {
		buf := make([]byte, 1024)
		for {
			n, err := stdout.Read(buf)
			if n > 0 {
				data := make([]byte, n)
				copy(data, buf[:n])
				s.Broadcast(data)
			}
			if err != nil {
				break
			}
		}
		s.mutex.Lock()
		s.isRunning = false
		if s.stdin != nil {
			_ = s.stdin.Close()
		}
		if s.stdout != nil {
			_ = s.stdout.Close()
		}
		s.mutex.Unlock()
	}()

	return nil
}

func (s *Service) WriteInput(data string) error {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	if !s.isRunning || s.stdin == nil {
		return fmt.Errorf("terminal session not running")
	}

	_, err := s.stdin.Write([]byte(data))
	return err
}

func (s *Service) RegisterClient(ch chan []byte) {
	s.clientMux.Lock()
	if s.clients == nil {
		s.clients = make(map[chan []byte]bool)
	}
	s.clients[ch] = true
	
	// Send history to new client
	if len(s.history) > 0 {
		histCopy := make([]byte, len(s.history))
		copy(histCopy, s.history)
		select {
		case ch <- histCopy:
		default:
		}
	}
	s.clientMux.Unlock()
}

func (s *Service) UnregisterClient(ch chan []byte) {
	s.clientMux.Lock()
	if s.clients != nil {
		delete(s.clients, ch)
	}
	s.clientMux.Unlock()
}

func (s *Service) Broadcast(data []byte) {
	s.clientMux.Lock()
	defer s.clientMux.Unlock()
	
	s.history = append(s.history, data...)
	if len(s.history) > 20000 {
		s.history = s.history[len(s.history)-20000:]
	}
	
	for ch := range s.clients {
		select {
		case ch <- data:
		default:
		}
	}
}

func (s *Service) KillSession() {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	if s.isRunning && s.cmd != nil && s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}
}

// StartCommand executes a bash command and returns its stdout/stderr reader
func (s *Service) StartCommand(ctx context.Context, command string, activeWorkspaceDir string) (*exec.Cmd, io.ReadCloser, error) {
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		if _, err := exec.LookPath("bash"); err == nil {
			cmd = exec.Command("bash", "-c", command)
		} else if _, err := exec.LookPath("powershell"); err == nil {
			cmd = exec.Command("powershell", "-Command", command)
		} else {
			cmd = exec.Command("cmd", "/c", command)
		}
	} else {
		cmd = exec.Command("bash", "-c", command)
	}

	cmd.Dir = activeWorkspaceDir
	cmd.Env = os.Environ()

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
	cmd.Stderr = cmd.Stdout

	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}

	return cmd, stdoutPipe, nil
}

func defaultOpenAIBase() string {
	if apiBase := os.Getenv("OPENAI_API_BASE"); apiBase != "" {
		return apiBase
	}
	return "https://api.openai.com/v1"
}

func maskAPIKey(key string) string {
	if key == "" {
		return ""
	}
	if len(key) <= 8 {
		return "********"
	}
	return key[:4] + strings.Repeat("*", len(key)-8) + key[len(key)-4:]
}

func parseConfiguredOpenAIModels(modelsEnv string) []string {
	var models []string
	for _, p := range strings.Split(modelsEnv, ",") {
		trimmed := strings.TrimSpace(p)
		if trimmed != "" {
			models = append(models, "openai/"+trimmed)
		}
	}
	return models
}

func (s *Service) GetOpenAISettings(fetchModels bool) OpenAISettings {
	settings := OpenAISettings{
		APIBase:          defaultOpenAIBase(),
		APIKeySet:        os.Getenv("OPENAI_API_KEY") != "",
		APIKeyMasked:     maskAPIKey(os.Getenv("OPENAI_API_KEY")),
		ConfiguredModels: os.Getenv("OPENAI_MODELS"),
	}
	if fetchModels && settings.APIKeySet {
		models, err := s.FetchOpenAIModels("", "")
		if err == nil {
			settings.AvailableModels = models
		}
	}
	return settings
}

func (s *Service) SaveOpenAISettings(apiKey, apiBase, models string, clearAPIKey bool) error {
	apiKey = strings.TrimSpace(apiKey)
	apiBase = strings.TrimSpace(apiBase)
	models = strings.TrimSpace(models)

	if apiBase == "" {
		apiBase = "https://api.openai.com/v1"
	}

	updates := map[string]*string{
		"OPENAI_API_BASE": &apiBase,
		"OPENAI_MODELS":   &models,
	}

	if clearAPIKey {
		os.Unsetenv("OPENAI_API_KEY")
		updates["OPENAI_API_KEY"] = nil
	} else if apiKey != "" {
		os.Setenv("OPENAI_API_KEY", apiKey)
		updates["OPENAI_API_KEY"] = &apiKey
	}

	os.Setenv("OPENAI_API_BASE", apiBase)
	if models == "" {
		os.Unsetenv("OPENAI_MODELS")
	} else {
		os.Setenv("OPENAI_MODELS", models)
	}

	return updateEnvFile(".env", updates)
}

func updateEnvFile(path string, updates map[string]*string) error {
	seen := map[string]bool{}
	var lines []string

	file, err := os.Open(path)
	if err == nil {
		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			line := scanner.Text()
			trimmed := strings.TrimSpace(line)
			if trimmed == "" || strings.HasPrefix(trimmed, "#") || !strings.Contains(line, "=") {
				lines = append(lines, line)
				continue
			}
			key := strings.TrimSpace(strings.SplitN(line, "=", 2)[0])
			if val, ok := updates[key]; ok {
				seen[key] = true
				if val == nil {
					continue
				}
				lines = append(lines, key+"="+strconv.Quote(*val))
				continue
			}
			lines = append(lines, line)
		}
		if scanErr := scanner.Err(); scanErr != nil {
			_ = file.Close()
			return scanErr
		}
		_ = file.Close()
	} else if !os.IsNotExist(err) {
		return err
	}

	for key, val := range updates {
		if seen[key] || val == nil {
			continue
		}
		lines = append(lines, key+"="+strconv.Quote(*val))
	}

	content := strings.Join(lines, "\n")
	if content != "" {
		content += "\n"
	}
	return os.WriteFile(path, []byte(content), 0600)
}

func (s *Service) FetchOpenAIModels(apiKey, apiBase string) ([]string, error) {
	apiKey = strings.TrimSpace(apiKey)
	apiBase = strings.TrimSpace(apiBase)
	if apiKey == "" {
		apiKey = os.Getenv("OPENAI_API_KEY")
	}
	if apiBase == "" {
		apiBase = defaultOpenAIBase()
	}
	if apiKey == "" {
		return nil, fmt.Errorf("OPENAI_API_KEY is not configured")
	}

	url := strings.TrimSuffix(apiBase, "/") + "/models"
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("OpenAI models endpoint returned status %d: %s", resp.StatusCode, string(body))
	}

	var parsed struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, err
	}

	var models []string
	for _, item := range parsed.Data {
		id := strings.TrimSpace(item.ID)
		if id != "" {
			models = append(models, "openai/"+id)
		}
	}
	return models, nil
}

// GetModelsList fetches available models from agy CLI or falls back to defaults
func (s *Service) GetModelsList() ([]string, error) {
	var models []string

	hasToken := false
	homeDir, errToken := os.UserHomeDir()
	if errToken == nil {
		tokenPath := filepath.Join(homeDir, ".gemini", "antigravity-cli", "antigravity-oauth-token")
		if _, errStat := os.Stat(tokenPath); errStat == nil {
			hasToken = true
		}
	}

	if hasToken {
		agyPath := auth.FindAgyPath()
		var outputBytes []byte
		var err error

		useDirect := false
		if _, lookErr := exec.LookPath("script"); lookErr != nil {
			useDirect = true
		}

		if useDirect {
			cmdDirect := exec.Command(agyPath, "models")
			cmdDirect.Env = os.Environ()
			outputBytes, err = cmdDirect.Output()
		} else {
			cmdStr := fmt.Sprintf("%s models", agyPath)
			cmd := exec.Command("script", "-q", "-f", "-c", cmdStr, "/dev/null")
			cmd.Env = os.Environ()
			outputBytes, err = cmd.Output()

			if err != nil {
				cmdDirect := exec.Command(agyPath, "models")
				cmdDirect.Env = os.Environ()
				outputBytes, err = cmdDirect.Output()
			}
		}

		if err == nil {
			lines := strings.Split(string(outputBytes), "\n")
			for _, line := range lines {
				trimmed := strings.TrimSpace(line)
				if trimmed == "" {
					continue
				}
				if strings.Contains(trimmed, "Fetching") || strings.Contains(trimmed, "⠋") || strings.Contains(trimmed, "⠙") || strings.Contains(trimmed, "⠹") || strings.Contains(trimmed, "⠸") || strings.Contains(trimmed, "⠼") || strings.Contains(trimmed, "⠴") || strings.Contains(trimmed, "⠦") || strings.Contains(trimmed, "⠧") || strings.Contains(trimmed, "⠇") || strings.Contains(trimmed, "⠏") {
					continue
				}
				fields := strings.Fields(trimmed)
				if len(fields) > 0 {
					models = append(models, fields[0])
				}
			}
		}
	}

	if len(models) == 0 {
		models = []string{
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

	// Append OpenAI-compatible models if configured. Prefer the live /models
	// endpoint so the dropdown reflects models available for the configured key.
	if os.Getenv("OPENAI_API_KEY") != "" {
		openAIModels, fetchErr := s.FetchOpenAIModels("", "")
		if fetchErr == nil && len(openAIModels) > 0 {
			models = append(models, openAIModels...)
		} else if configured := parseConfiguredOpenAIModels(os.Getenv("OPENAI_MODELS")); len(configured) > 0 {
			models = append(models, configured...)
		} else {
			models = append(models,
				"openai/gpt-4o",
				"openai/gpt-4o-mini",
				"openai/deepseek-chat",
				"openai/deepseek-reasoner",
			)
		}
	}

	return models, nil
}
