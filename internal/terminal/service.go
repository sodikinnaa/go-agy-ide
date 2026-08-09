package terminal

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mobile-agy/internal/auth"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/creack/pty"
)

var ansiRegex = regexp.MustCompile(`\x1b\[[0-9;?]*[a-zA-Z]|\x1b\][^\x07]*\x07|\x1b[a-zA-Z0-9]`)

func stripANSI(str string) string {
	return ansiRegex.ReplaceAllString(str, "")
}

type Service struct {
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	stdout    io.ReadCloser
	ptyFile   *os.File
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

// buildTerminalEnv constructs a full environment with optimal TUI / CLI terminal variables
func buildTerminalEnv() []string {
	envMap := map[string]string{
		"TERM":        "xterm-256color",
		"COLORTERM":   "truecolor",
		"LANG":        "C.UTF-8",
		"LC_ALL":      "C.UTF-8",
		"PAGER":       "cat",
		"FORCE_COLOR": "true",
		"CLICOLOR":    "1",
	}

	// Ensure PATH includes common user and system binary directories
	currentPath := os.Getenv("PATH")
	homeDir, _ := os.UserHomeDir()
	extraPaths := []string{
		filepath.Join(homeDir, "go", "bin"),
		filepath.Join(homeDir, ".local", "bin"),
		"/usr/local/sbin",
		"/usr/local/bin",
		"/usr/sbin",
		"/usr/bin",
		"/sbin",
		"/bin",
	}

	pathParts := strings.Split(currentPath, string(os.PathListSeparator))
	for _, p := range extraPaths {
		if p == "" {
			continue
		}
		found := false
		for _, existing := range pathParts {
			if existing == p {
				found = true
				break
			}
		}
		if !found {
			pathParts = append(pathParts, p)
		}
	}
	envMap["PATH"] = strings.Join(pathParts, string(os.PathListSeparator))

	if os.Getenv("SHELL") != "" {
		envMap["SHELL"] = os.Getenv("SHELL")
	} else if bashPath, err := exec.LookPath("bash"); err == nil {
		envMap["SHELL"] = bashPath
	} else {
		envMap["SHELL"] = "/bin/bash"
	}

	res := os.Environ()
	for k, v := range envMap {
		prefix := k + "="
		updated := false
		for i, e := range res {
			if strings.HasPrefix(e, prefix) {
				res[i] = k + "=" + v
				updated = true
				break
			}
		}
		if !updated {
			res = append(res, k+"="+v)
		}
	}
	return res
}

func getShellPath() string {
	if shell := os.Getenv("SHELL"); shell != "" {
		if _, err := exec.LookPath(shell); err == nil {
			return shell
		}
	}
	if bashPath, err := exec.LookPath("bash"); err == nil {
		return bashPath
	}
	if shPath, err := exec.LookPath("sh"); err == nil {
		return shPath
	}
	return "/bin/bash"
}

func (s *Service) StartSession(workspaceDir string) error {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	if s.isRunning {
		return nil
	}

	if workspaceDir == "" {
		if home, err := os.UserHomeDir(); err == nil {
			workspaceDir = home
		} else {
			workspaceDir = "."
		}
	} else if _, err := os.Stat(workspaceDir); err != nil {
		if home, err := os.UserHomeDir(); err == nil {
			workspaceDir = home
		}
	}

	env := buildTerminalEnv()

	var cmd *exec.Cmd
	var stdin io.WriteCloser
	var stdout io.ReadCloser
	var ptmx *os.File
	var err error

	if runtime.GOOS != "windows" {
		shell := getShellPath()
		cmd = exec.Command(shell, "-i")
		cmd.Dir = workspaceDir
		cmd.Env = env

		// Allocate real PTY master/slave pair using creack/pty
		ptmx, err = pty.Start(cmd)
		if err == nil {
			// Set standard initial terminal window size (35 rows x 120 cols)
			_ = pty.Setsize(ptmx, &pty.Winsize{
				Rows: 35,
				Cols: 120,
			})
			stdin = ptmx
			stdout = ptmx
			s.ptyFile = ptmx
		} else {
			// Fallback if PTY allocation fails (e.g., restricted containers)
			cmd, stdin, stdout, err = startFallbackUnixSession(workspaceDir, env)
			if err != nil {
				return err
			}
		}
	} else {
		cmd, stdin, stdout, err = startWindowsSession(workspaceDir, env)
		if err != nil {
			return err
		}
	}

	s.cmd = cmd
	s.stdin = stdin
	s.stdout = stdout
	s.isRunning = true

	// Dedicated background read loop for handling escape sequences and PTY stream
	go func() {
		buf := make([]byte, 4096)
		for {
			n, readErr := stdout.Read(buf)
			if n > 0 {
				data := make([]byte, n)
				copy(data, buf[:n])
				s.Broadcast(data)
			}
			if readErr != nil {
				break
			}
		}

		s.mutex.Lock()
		s.isRunning = false
		if s.ptyFile != nil {
			_ = s.ptyFile.Close()
			s.ptyFile = nil
		}
		if s.stdin != nil && s.stdin != s.ptyFile {
			_ = s.stdin.Close()
		}
		if s.stdout != nil && s.stdout != s.ptyFile {
			_ = s.stdout.Close()
		}
		if s.cmd != nil && s.cmd.Process != nil {
			_ = s.cmd.Process.Kill()
			_ = s.cmd.Wait()
		}
		s.mutex.Unlock()
	}()

	return nil
}

func startFallbackUnixSession(workspaceDir string, env []string) (*exec.Cmd, io.WriteCloser, io.ReadCloser, error) {
	var cmd *exec.Cmd
	if _, err := exec.LookPath("script"); err == nil {
		cmd = exec.Command("script", "-q", "-f", "-c", "bash -i", "/dev/null")
	} else {
		cmd = exec.Command("bash", "-i")
	}

	cmd.Dir = workspaceDir
	cmd.Env = env

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, nil, err
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, nil, nil, err
	}
	cmd.Stderr = cmd.Stdout

	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, nil, nil, err
	}

	return cmd, stdin, stdout, nil
}

func startWindowsSession(workspaceDir string, env []string) (*exec.Cmd, io.WriteCloser, io.ReadCloser, error) {
	var cmd *exec.Cmd
	if _, err := exec.LookPath("bash"); err == nil {
		cmd = exec.Command("bash", "-i")
	} else if _, err := exec.LookPath("powershell"); err == nil {
		cmd = exec.Command("powershell")
	} else {
		cmd = exec.Command("cmd")
	}

	cmd.Dir = workspaceDir
	cmd.Env = env

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, nil, err
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, nil, nil, err
	}
	cmd.Stderr = cmd.Stdout

	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, nil, nil, err
	}

	return cmd, stdin, stdout, nil
}

func (s *Service) WriteInput(data string) error {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	if !s.isRunning || s.stdin == nil {
		return fmt.Errorf("terminal session not running")
	}

	_, err := s.stdin.Write([]byte(data))
	if err != nil {
		s.isRunning = false
	}
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

	// Maintain clean buffer history up to 65KB
	maxHistory := 65536
	if len(s.history) > maxHistory {
		s.history = s.history[len(s.history)-maxHistory:]
		// Ensure history doesn't truncate in middle of line/sequence if possible
		checkLen := 1024
		if len(s.history) < checkLen {
			checkLen = len(s.history)
		}
		if idx := bytes.IndexByte(s.history[:checkLen], '\n'); idx >= 0 && idx < len(s.history)-1 {
			s.history = s.history[idx+1:]
		}
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
	if s.ptyFile != nil {
		_ = s.ptyFile.Close()
		s.ptyFile = nil
	}
	s.isRunning = false
}

// ResizeSession updates the winsize of the active PTY
func (s *Service) ResizeSession(cols, rows int) error {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	if cols <= 0 || rows <= 0 {
		return nil
	}

	if s.ptyFile != nil {
		return pty.Setsize(s.ptyFile, &pty.Winsize{
			Rows: uint16(rows),
			Cols: uint16(cols),
		})
	}
	return nil
}

// StartCommand executes a command and returns its stdout/stderr reader
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
	cmd.Env = buildTerminalEnv()

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

func getHomeDir() (string, error) {
	if auth.HomeDirOverride != "" {
		return auth.HomeDirOverride, nil
	}
	return os.UserHomeDir()
}

// GetModelsList fetches available models from agy CLI or falls back to defaults
func (s *Service) GetModelsList() ([]string, error) {
	var models []string

	hasToken := false
	homeDir, errToken := getHomeDir()
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
			cmdDirect.Env = buildTerminalEnv()
			outputBytes, err = cmdDirect.Output()
		} else {
			cmdStr := fmt.Sprintf("%s models", agyPath)
			cmd := exec.Command("script", "-q", "-f", "-c", cmdStr, "/dev/null")
			cmd.Env = buildTerminalEnv()
			outputBytes, err = cmd.Output()

			if err != nil {
				cmdDirect := exec.Command(agyPath, "models")
				cmdDirect.Env = buildTerminalEnv()
				outputBytes, err = cmdDirect.Output()
			}
		}

		if err == nil {
			rawStr := stripANSI(string(outputBytes))
			rawStr = strings.ReplaceAll(rawStr, "\r", "\n")
			lines := strings.Split(rawStr, "\n")
			for _, line := range lines {
				trimmed := strings.TrimSpace(line)
				if trimmed == "" || strings.Contains(trimmed, "Fetching") {
					continue
				}
				trimmed = strings.Map(func(r rune) rune {
					if strings.ContainsRune("⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏", r) || r < 32 || r == 127 {
						return -1
					}
					return r
				}, trimmed)
				trimmed = strings.TrimSpace(trimmed)
				if trimmed == "" {
					continue
				}
				fields := strings.Fields(trimmed)
				if len(fields) > 0 {
					cleanModel := stripANSI(fields[0])
					cleanModel = strings.TrimSpace(cleanModel)
					if cleanModel != "" &&
						!strings.Contains(cleanModel, "Fetching") &&
						!strings.Contains(cleanModel, "Error") &&
						!strings.Contains(cleanModel, "Usage") &&
						!strings.Contains(cleanModel, "Authentication") &&
						!strings.Contains(cleanModel, "invalid") &&
						!strings.Contains(cleanModel, "Waiting") {
						models = append(models, cleanModel)
					}
				}
			}
		}
	}

	if len(models) == 0 {
		models = []string{
			"gemini-3.6-flash-high",
			"gemini-3.6-flash-medium",
			"gemini-3.6-flash-low",
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
