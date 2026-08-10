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
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/creack/pty"
)

var ansiRegex = regexp.MustCompile(`\x1b\[[0-9;?]*[a-zA-Z]|\x1b\][^\x07]*\x07|\x1b[a-zA-Z0-9]`)

func stripANSI(str string) string {
	return ansiRegex.ReplaceAllString(str, "")
}

type TerminalSession struct {
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"createdAt"`
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

type Service struct {
	sessions map[string]*TerminalSession
	mu       sync.RWMutex
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
	s := &Service{
		sessions: make(map[string]*TerminalSession),
	}
	_, _ = s.CreateSession("default")
	return s
}

func (s *Service) CreateSession(id string) (*TerminalSession, error) {
	if id == "" {
		id = "default"
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.sessions == nil {
		s.sessions = make(map[string]*TerminalSession)
	}

	if sess, ok := s.sessions[id]; ok && sess != nil {
		return sess, nil
	}

	sess := &TerminalSession{
		ID:        id,
		clients:   make(map[chan []byte]bool),
		CreatedAt: time.Now(),
	}
	s.sessions[id] = sess
	return sess, nil
}

func (s *Service) GetSession(id string) *TerminalSession {
	if id == "" {
		id = "default"
	}

	s.mu.RLock()
	sess, ok := s.sessions[id]
	s.mu.RUnlock()

	if ok && sess != nil {
		return sess
	}

	if id == "default" {
		s.mu.Lock()
		defer s.mu.Unlock()

		if s.sessions == nil {
			s.sessions = make(map[string]*TerminalSession)
		}
		if sess, ok := s.sessions["default"]; ok && sess != nil {
			return sess
		}
		sess = &TerminalSession{
			ID:        "default",
			clients:   make(map[chan []byte]bool),
			CreatedAt: time.Now(),
		}
		s.sessions["default"] = sess
		return sess
	}

	return nil
}

func (s *Service) ListSessions() []*TerminalSession {
	s.mu.RLock()
	defer s.mu.RUnlock()

	sessions := make([]*TerminalSession, 0, len(s.sessions))
	for _, sess := range s.sessions {
		sessions = append(sessions, sess)
	}
	return sessions
}

func (s *Service) CloseSession(id string) {
	if id == "" {
		id = "default"
	}

	s.mu.Lock()
	sess, ok := s.sessions[id]
	if ok && sess != nil {
		delete(s.sessions, id)
	}
	s.mu.Unlock()

	if ok && sess != nil {
		sess.Kill()
	}
}

// Service routing methods targeting specific session ID or defaulting to "default"
func (s *Service) StartSession(workspaceDir string) error {
	return s.StartSessionID("default", workspaceDir)
}

func (s *Service) StartSessionID(id string, workspaceDir string) error {
	if id == "" {
		id = "default"
	}
	sess, err := s.CreateSession(id)
	if err != nil {
		return err
	}
	return sess.Start(workspaceDir)
}

func (s *Service) WriteInput(data string) error {
	return s.WriteInputSession("default", data)
}

func (s *Service) WriteInputSession(id string, data string) error {
	if id == "" {
		id = "default"
	}
	sess := s.GetSession(id)
	if sess == nil {
		return fmt.Errorf("terminal session %q not found", id)
	}
	return sess.WriteInput(data)
}

func (s *Service) RegisterClient(ch chan []byte) {
	s.RegisterClientSession("default", ch)
}

func (s *Service) RegisterClientSession(id string, ch chan []byte) {
	if id == "" {
		id = "default"
	}
	sess, _ := s.CreateSession(id)
	if sess != nil {
		sess.RegisterClient(ch)
	}
}

func (s *Service) UnregisterClient(ch chan []byte) {
	s.UnregisterClientSession("default", ch)
}

func (s *Service) UnregisterClientSession(id string, ch chan []byte) {
	if id == "" {
		id = "default"
	}
	sess := s.GetSession(id)
	if sess != nil {
		sess.UnregisterClient(ch)
	}
}

func (s *Service) Broadcast(data []byte) {
	sess := s.GetSession("default")
	if sess != nil {
		sess.Broadcast(data)
	}
}

func (s *Service) KillSession() {
	s.KillSessionID("default")
}

func (s *Service) KillSessionID(id string) {
	if id == "" {
		id = "default"
	}
	sess := s.GetSession(id)
	if sess != nil {
		sess.Kill()
	}
}

func (s *Service) ResizeSession(cols, rows int) error {
	return s.ResizeSessionID("default", cols, rows)
}

func (s *Service) ResizeSessionID(id string, cols, rows int) error {
	if id == "" {
		id = "default"
	}
	sess := s.GetSession(id)
	if sess == nil {
		return nil
	}
	return sess.Resize(cols, rows)
}

// TerminalSession methods
func (ts *TerminalSession) IsRunning() bool {
	ts.mutex.Lock()
	defer ts.mutex.Unlock()
	return ts.isRunning
}

func (ts *TerminalSession) Start(workspaceDir string) error {
	ts.mutex.Lock()
	defer ts.mutex.Unlock()

	if ts.isRunning {
		return nil
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
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

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
			ts.ptyFile = ptmx
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

	ts.cmd = cmd
	ts.stdin = stdin
	ts.stdout = stdout
	ts.isRunning = true

	// Dedicated background read loop for handling escape sequences and PTY stream
	go func() {
		buf := make([]byte, 4096)
		for {
			n, readErr := stdout.Read(buf)
			if n > 0 {
				data := make([]byte, n)
				copy(data, buf[:n])
				ts.Broadcast(data)
			}
			if readErr != nil {
				break
			}
		}

		if cmd != nil {
			_ = cmd.Wait()
		}

		ts.mutex.Lock()
		ts.isRunning = false
		if ts.ptyFile != nil {
			_ = ts.ptyFile.Close()
			ts.ptyFile = nil
		}
		if ts.stdin != nil && ts.stdin != ts.ptyFile {
			_ = ts.stdin.Close()
		}
		if ts.stdout != nil && ts.stdout != ts.ptyFile {
			_ = ts.stdout.Close()
		}
		ts.mutex.Unlock()
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
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

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

func (ts *TerminalSession) WriteInput(data string) error {
	ts.mutex.Lock()
	defer ts.mutex.Unlock()

	if !ts.isRunning || ts.stdin == nil {
		return fmt.Errorf("terminal session not running")
	}

	_, err := ts.stdin.Write([]byte(data))
	return err
}

func (ts *TerminalSession) RegisterClient(ch chan []byte) {
	ts.clientMux.Lock()
	if ts.clients == nil {
		ts.clients = make(map[chan []byte]bool)
	}
	ts.clients[ch] = true

	// Send history to new client
	if len(ts.history) > 0 {
		histToSend := sanitizeHistoryForClient(ts.history)
		if len(histToSend) > 0 {
			histCopy := make([]byte, len(histToSend))
			copy(histCopy, histToSend)
			select {
			case ch <- histCopy:
			default:
			}
		}
	}
	ts.clientMux.Unlock()
}

func (ts *TerminalSession) UnregisterClient(ch chan []byte) {
	ts.clientMux.Lock()
	if ts.clients != nil {
		delete(ts.clients, ch)
	}
	ts.clientMux.Unlock()
}

func (ts *TerminalSession) Broadcast(data []byte) {
	ts.clientMux.Lock()
	defer ts.clientMux.Unlock()

	ts.history = append(ts.history, data...)

	// Maintain clean buffer history up to 65KB
	maxHistory := 65536
	if len(ts.history) > maxHistory {
		targetCut := len(ts.history) - maxHistory
		cut := findNextSafeBoundary(ts.history, targetCut)
		if cut < len(ts.history) {
			ts.history = ts.history[cut:]
		} else {
			ts.history = ts.history[len(ts.history)-maxHistory:]
			ts.history = bytes.ToValidUTF8(ts.history, nil)
		}
	}

	for ch := range ts.clients {
		select {
		case ch <- data:
		default:
		}
	}
}

func (ts *TerminalSession) Kill() {
	ts.mutex.Lock()
	defer ts.mutex.Unlock()

	if ts.isRunning && ts.cmd != nil && ts.cmd.Process != nil {
		pid := ts.cmd.Process.Pid
		if pid > 0 {
			if runtime.GOOS != "windows" {
				if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil {
					_ = ts.cmd.Process.Kill()
				}
			} else {
				_ = ts.cmd.Process.Kill()
			}
		}
	}
	if ts.ptyFile != nil {
		_ = ts.ptyFile.Close()
		ts.ptyFile = nil
	}
	ts.isRunning = false
}

func (ts *TerminalSession) Resize(cols, rows int) error {
	ts.mutex.Lock()
	defer ts.mutex.Unlock()

	if cols <= 0 || rows <= 0 {
		return nil
	}

	if ts.ptyFile != nil {
		return pty.Setsize(ts.ptyFile, &pty.Winsize{
			Rows: uint16(rows),
			Cols: uint16(cols),
		})
	}
	return nil
}

// isSafeBoundary checks if index cut in buf is at a safe UTF-8 / ANSI escape sequence boundary.
func isSafeBoundary(buf []byte, cut int) (bool, int) {
	if cut <= 0 || cut >= len(buf) {
		return true, cut
	}

	for i := cut - 1; i >= 0 && i >= cut-4; i-- {
		if utf8.RuneStart(buf[i]) {
			if !utf8.FullRune(buf[i:]) {
				return false, cut + (utf8.UTFMax - (cut - i))
			}
			break
		}
	}

	checkBack := 64
	start := cut - checkBack
	if start < 0 {
		start = 0
	}

	window := buf[start:cut]
	if lastESC := bytes.LastIndexByte(window, 0x1b); lastESC >= 0 {
		escPos := start + lastESC
		sub := buf[escPos:]
		loc := ansiRegex.FindIndex(sub)
		if loc == nil {
			return false, escPos
		}
		endEsc := escPos + loc[1]
		if cut < endEsc {
			return false, endEsc
		}
	}

	return true, cut
}

// findNextSafeBoundary finds a safe cut index at or after targetCut in buf.
func findNextSafeBoundary(buf []byte, targetCut int) int {
	if targetCut <= 0 {
		return 0
	}
	if targetCut >= len(buf) {
		return len(buf)
	}

	checkWindow := 2048
	endWin := targetCut + checkWindow
	if endWin > len(buf) {
		endWin = len(buf)
	}

	if idx := bytes.IndexByte(buf[targetCut:endWin], '\n'); idx >= 0 {
		cand := targetCut + idx + 1
		if safe, nextCand := isSafeBoundary(buf, cand); safe {
			return cand
		} else if nextCand < len(buf) {
			return nextCand
		}
	}

	cut := targetCut
	for cut < len(buf) {
		safe, nextCut := isSafeBoundary(buf, cut)
		if safe {
			return cut
		}
		if nextCut <= cut {
			cut++
		} else {
			cut = nextCut
		}
	}

	return len(buf)
}

// sanitizeHistoryForClient prepares history buffer for broadcasting to a new client.
func sanitizeHistoryForClient(history []byte) []byte {
	if len(history) == 0 {
		return nil
	}

	start := 0
	if safe, next := isSafeBoundary(history, 0); !safe {
		start = next
	}

	if start >= len(history) {
		return nil
	}

	end := len(history)

	for i := end - 1; i >= start && i >= end-4; i-- {
		if utf8.RuneStart(history[i]) {
			if !utf8.FullRune(history[i:end]) {
				end = i
			}
			break
		}
	}

	if start >= end {
		return nil
	}

	slice := history[start:end]

	if !utf8.Valid(slice) {
		slice = bytes.ToValidUTF8(slice, nil)
	}

	return slice
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
		"BROWSER":     "false",
		"DISPLAY":     "",
	}

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
