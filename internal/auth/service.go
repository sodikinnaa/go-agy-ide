package auth

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/zalando/go-keyring"
)

type Service struct {
	mu                     sync.RWMutex
	serverStartDir         string
	secretPassword         string
	passwordSessionToken   string
	bypassDynamicAuthCheck bool

	// Google OAuth process variables
	activeAuthCmd   *exec.Cmd
	activeAuthStdin io.WriteCloser
	activeAuthURL   string
}

func NewService(serverStartDir string) *Service {
	s := &Service{
		serverStartDir: serverStartDir,
	}
	s.LoadPassword()
	return s
}

func (s *Service) SetBypassDynamicAuthCheck(bypass bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bypassDynamicAuthCheck = bypass
}

func (s *Service) LoadPassword() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.secretPassword = os.Getenv("PASSWORD")
	if s.secretPassword != "" {
		log.Printf("[SECURITY] Sandi keamanan dimuat saka env variable PASSWORD\n")
		return
	}

	configPath := filepath.Join(s.serverStartDir, "password.txt")
	data, err := os.ReadFile(configPath)
	if err == nil {
		s.secretPassword = strings.TrimSpace(string(data))
		if s.secretPassword != "" {
			log.Printf("[SECURITY] Sandi keamanan dimuat saka %s\n", configPath)
			return
		}
	}

	s.secretPassword = s.GenerateRandomPassword(8)
	_ = os.WriteFile(configPath, []byte(s.secretPassword), 0600)
	log.Printf("[SECURITY] Sandi keamanan login acak digawe: %s (disimpen ing password.txt)\n", s.secretPassword)
}

func (s *Service) VerifyPassword(pwd string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.secretPassword == "" || pwd == s.secretPassword
}

func (s *Service) GetPassword() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.secretPassword
}

func (s *Service) SessionToken() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.passwordSessionToken
}

func (s *Service) InitSession() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.passwordSessionToken == "" {
		s.passwordSessionToken = s.GenerateRandomPassword(32)
	}
	return s.passwordSessionToken
}

func (s *Service) ClearSession() {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Clear the session token
	s.passwordSessionToken = ""
}

func (s *Service) ValidateSession(token string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.secretPassword == "" {
		return true
	}
	return s.passwordSessionToken != "" && token == s.passwordSessionToken
}

func (s *Service) GenerateRandomPassword(length int) string {
	const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	bytes := make([]byte, length)
	if _, err := rand.Read(bytes); err != nil {
		return "agy123"
	}
	for i, b := range bytes {
		bytes[i] = charset[b%byte(len(charset))]
	}
	return string(bytes)
}

func FindAgyPath() string {
	if p, err := exec.LookPath("agy"); err == nil {
		return p
	}

	homeDir, err := os.UserHomeDir()
	if err == nil {
		p := filepath.Join(homeDir, ".local", "bin", "agy")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}

	p := "/home/codespace/.local/bin/agy"
	if _, err := os.Stat(p); err == nil {
		return p
	}

	return "agy"
}

func (s *Service) CheckOAuthTokenExists() bool {
	s.mu.RLock()
	bypass := s.bypassDynamicAuthCheck
	s.mu.RUnlock()

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	tokenPath := filepath.Join(homeDir, ".gemini", "antigravity-cli", "antigravity-oauth-token")
	if _, err := os.Stat(tokenPath); err == nil {
		return true
	}

	if bypass {
		return false
	}

	agyPath := FindAgyPath()
	var runErr error
	useDirect := false

	if _, err := exec.LookPath("script"); err != nil {
		useDirect = true
	}

	if useDirect {
		cmdDirect := exec.Command(agyPath, "--print", "hello", "--dangerously-skip-permissions")
		doneDirect := make(chan error, 1)
		go func() {
			doneDirect <- cmdDirect.Run()
		}()
		select {
		case runErr = <-doneDirect:
		case <-time.After(8 * time.Second):
			if cmdDirect.Process != nil {
				_ = cmdDirect.Process.Kill()
			}
			runErr = fmt.Errorf("timeout")
		}
	} else {
		cmdStr := fmt.Sprintf("%s --print hello --dangerously-skip-permissions", agyPath)
		cmd := exec.Command("script", "-q", "-f", "-c", cmdStr, "/dev/null")
		done := make(chan error, 1)
		go func() {
			done <- cmd.Run()
		}()
		select {
		case runErr = <-done:
		case <-time.After(8 * time.Second):
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
			runErr = fmt.Errorf("timeout")
		}

		if runErr != nil {
			log.Printf("[AUTH] CheckOAuthTokenExists: 'script' failed with error: %v. Retrying by running agy directly...", runErr)
			cmdDirect := exec.Command(agyPath, "--print", "hello", "--dangerously-skip-permissions")
			doneDirect := make(chan error, 1)
			go func() {
				doneDirect <- cmdDirect.Run()
			}()
			select {
			case runErr = <-doneDirect:
			case <-time.After(8 * time.Second):
				if cmdDirect.Process != nil {
					_ = cmdDirect.Process.Kill()
				}
				runErr = fmt.Errorf("timeout")
			}
		}
	}

	if runErr == nil {
		tokenDir := filepath.Join(homeDir, ".gemini", "antigravity-cli")
		_ = os.MkdirAll(tokenDir, 0755)
		_ = os.WriteFile(tokenPath, []byte("keychain-authenticated-dummy-token"), 0600)
		log.Printf("[AUTH] Nemokake sesi keychain sing wis ana. Nggawe file dummy token.")
		return true
	}

	return false
}

func (s *Service) StartGoogleAuth(activeWorkspaceDir string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.CheckOAuthTokenExists() {
		return "already_authenticated", nil
	}

	if s.activeAuthCmd != nil && s.activeAuthCmd.Process != nil {
		_ = s.activeAuthCmd.Process.Kill()
	}

	agyPath := FindAgyPath()
	var cmd *exec.Cmd
	useDirect := false

	if _, err := exec.LookPath("script"); err != nil {
		log.Printf("[AUTH] 'script' utility not found. Using direct command execution.")
		useDirect = true
	}

	if useDirect {
		cmd = exec.Command(agyPath, "--print", "hello", "--dangerously-skip-permissions")
	} else {
		cmdStr := fmt.Sprintf("%s --print hello --dangerously-skip-permissions", agyPath)
		cmd = exec.Command("script", "-q", "-f", "-c", cmdStr, "/dev/null")
	}
	cmd.Dir = activeWorkspaceDir
	cmd.Env = os.Environ()

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		return "", fmt.Errorf("failed to create stdin pipe: %w", err)
	}

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("failed to create stdout pipe: %w", err)
	}
	cmd.Stderr = cmd.Stdout

	log.Printf("[AUTH START] starting command: %v in dir: %s", cmd.Args, cmd.Dir)

	if err := cmd.Start(); err != nil {
		log.Printf("[AUTH ERROR] failed to start command (useDirect=%v): %v", useDirect, err)
		if !useDirect {
			log.Printf("[AUTH] Retrying StartGoogleAuth using direct execution fallback...")
			cmd = exec.Command(agyPath, "--print", "hello", "--dangerously-skip-permissions")
			cmd.Dir = activeWorkspaceDir
			cmd.Env = os.Environ()
			stdinPipe, err = cmd.StdinPipe()
			if err != nil {
				return "", fmt.Errorf("failed to create stdin pipe: %w", err)
			}
			stdoutPipe, err = cmd.StdoutPipe()
			if err != nil {
				return "", fmt.Errorf("failed to create stdout pipe: %w", err)
			}
			cmd.Stderr = cmd.Stdout
			if err := cmd.Start(); err != nil {
				return "", fmt.Errorf("failed to start agy directly: %w", err)
			}
		} else {
			return "", fmt.Errorf("failed to start agy: %w", err)
		}
	}

	s.activeAuthCmd = cmd
	s.activeAuthStdin = stdinPipe
	s.activeAuthURL = ""

	// Read output in background to fetch login URL and respond to theme prompts
	go func() {
		buf := make([]byte, 1024)
		var output string
		for {
			n, err := stdoutPipe.Read(buf)
			if n > 0 {
				chunk := string(buf[:n])
				log.Printf("[AUTH READ CHUNK]: %q", chunk)
				output += chunk

				lowerOut := strings.ToLower(output)
				if strings.Contains(lowerOut, "select login method:") || strings.Contains(lowerOut, "select login method") {
					log.Printf("[AUTH] Prompt 'Select login method' detected. Sending '1\\n'...")
					_, _ = io.WriteString(stdinPipe, "1\n")
					output = ""
				} else if strings.Contains(lowerOut, "select theme") ||
					strings.Contains(lowerOut, "choose theme") ||
					strings.Contains(lowerOut, "select a theme") ||
					strings.Contains(lowerOut, "color theme") ||
					strings.Contains(lowerOut, "arrow keys to navigate") ||
					strings.Contains(lowerOut, "enter to select") ||
					strings.Contains(lowerOut, "shift+up/down") ||
					strings.Contains(lowerOut, "navigate") ||
					strings.Contains(lowerOut, "template") ||
					strings.Contains(lowerOut, "choose template") ||
					strings.Contains(lowerOut, "select template") ||
					strings.Contains(lowerOut, "[y/n]") ||
					strings.Contains(lowerOut, "[yes/no]") {
					log.Printf("[AUTH] Interactive prompt detected. Sending '\\n' to accept default...")
					_, _ = io.WriteString(stdinPipe, "\n")
					output = ""
				}

				s.mu.Lock()
				if s.activeAuthURL == "" {
					if idx := strings.Index(output, "https://accounts.google.com/o/oauth2/"); idx != -1 {
						urlPart := output[idx:]
						if endIdx := strings.IndexAny(urlPart, " \r\n\t\""); endIdx != -1 {
							s.activeAuthURL = urlPart[:endIdx]
							log.Printf("[AUTH FOUND URL]: %s", s.activeAuthURL)
						}
					}
				}
				s.mu.Unlock()
			}
			if err != nil {
				log.Printf("[AUTH READ EOF/ERROR]: %v", err)
				break
			}
		}
	}()

	// Wait up to 20 seconds for the URL
	for i := 0; i < 200; i++ {
		s.mu.RLock()
		url := s.activeAuthURL
		s.mu.RUnlock()
		if url != "" {
			return url, nil
		}
		time.Sleep(100 * time.Millisecond)
	}

	return "", fmt.Errorf("failed to get authentication URL from agy (timeout)")
}

func (s *Service) SubmitGoogleAuthCode(code string) error {
	s.mu.Lock()
	cmd := s.activeAuthCmd
	stdin := s.activeAuthStdin
	s.mu.Unlock()

	if cmd == nil || stdin == nil {
		return fmt.Errorf("no active authentication session running")
	}

	_, err := io.WriteString(stdin, code+"\n")
	if err != nil {
		return fmt.Errorf("failed to write code to stdin: %w", err)
	}

	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	select {
	case err := <-done:
		if err != nil {
			if s.CheckOAuthTokenExists() {
				return nil
			}
			return fmt.Errorf("agy authentication failed: %w", err)
		}
	case <-time.After(15 * time.Second):
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		return fmt.Errorf("agy authentication timeout (15s)")
	}

	if s.CheckOAuthTokenExists() {
		return nil
	}
	return fmt.Errorf("verification failed: token file not created")
}

func (s *Service) Logout() {
	homeDir, err := os.UserHomeDir()
	if err == nil {
		tokenPath := filepath.Join(homeDir, ".gemini", "antigravity-cli", "antigravity-oauth-token")
		_ = os.Remove(tokenPath)
	}
	s.ClearSession()
}

type SettingsStruct struct {
	GCP struct {
		Project  string `json:"project"`
		Location string `json:"location"`
	} `json:"gcp"`
}

func (s *Service) GetGCPProject() string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	settingsPath := filepath.Join(homeDir, ".gemini", "antigravity-cli", "settings.json")
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		return ""
	}
	var settings SettingsStruct
	if err := json.Unmarshal(data, &settings); err != nil {
		return ""
	}
	return settings.GCP.Project
}

func (s *Service) GetAuthenticatedEmail() string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	logDir := filepath.Join(homeDir, ".gemini", "antigravity-cli", "log")
	entries, err := os.ReadDir(logDir)
	if err != nil {
		return ""
	}

	// Sort files by name descending to get the newest first
	// Log files are named like cli-YYYYMMDD_HHMMSS.log
	for i := len(entries) - 1; i >= 0; i-- {
		entry := entries[i]
		if !entry.IsDir() && strings.HasPrefix(entry.Name(), "cli-") && strings.HasSuffix(entry.Name(), ".log") {
			latestFile := filepath.Join(logDir, entry.Name())
			data, err := os.ReadFile(latestFile)
			if err == nil {
				content := string(data)
				// Search for "OAuth: authenticated successfully as "
				const pattern = "OAuth: authenticated successfully as "
				if idx := strings.LastIndex(content, pattern); idx != -1 {
					sub := content[idx+len(pattern):]
					if endIdx := strings.IndexAny(sub, "\r\n\t "); endIdx != -1 {
						return sub[:endIdx]
					}
				}
				// Try another pattern: "applyAuthResult: email="
				const pattern2 = "applyAuthResult: email="
				if idx := strings.LastIndex(content, pattern2); idx != -1 {
					sub := content[idx+len(pattern2):]
					if endIdx := strings.IndexAny(sub, ",\r\n\t "); endIdx != -1 {
						return sub[:endIdx]
					}
				}
			}
		}
	}
	return ""
}

type QuotaGroup struct {
	GroupName         string  `json:"groupName"`
	GroupDescription  string  `json:"groupDescription"`
	RemainingFraction float32 `json:"remainingFraction"`
	ResetTime         string  `json:"resetTime"`
}

type QuotaSummaryResponse struct {
	Groups    []QuotaGroup `json:"groups"`
	Exhausted bool         `json:"exhausted"`
	Error     string       `json:"error,omitempty"`
}

func (s *Service) GetQuotaSummary() (*QuotaSummaryResponse, error) {
	if !s.CheckOAuthTokenExists() {
		return nil, fmt.Errorf("user is not authenticated")
	}

	val, err := keyring.Get("gemini", "antigravity")
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve credentials from keyring: %w", err)
	}

	var kt struct {
		Token struct {
			AccessToken string `json:"access_token"`
		} `json:"token"`
	}
	if err := json.Unmarshal([]byte(val), &kt); err != nil {
		return nil, fmt.Errorf("failed to parse keyring credentials: %w", err)
	}

	accessToken := kt.Token.AccessToken
	if accessToken == "" {
		return nil, fmt.Errorf("access token is empty in credentials")
	}

	project := s.GetGCPProject()

	var resp *http.Response
	var respBytes []byte

	projectsToTry := []string{project}
	if project != "" {
		projectsToTry = append(projectsToTry, "")
	}

	var lastErr error
	for _, proj := range projectsToTry {
		url := "https://daily-cloudcode-pa.googleapis.com/v1internal:retrieveUserQuotaSummary"
		bodyMap := map[string]string{
			"project": proj,
		}
		bodyBytes, _ := json.Marshal(bodyMap)

		req, err := http.NewRequest("POST", url, bytes.NewBuffer(bodyBytes))
		if err != nil {
			lastErr = err
			continue
		}

		req.Header.Set("Authorization", "Bearer "+accessToken)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "antigravity/cli/1.2.3")

		client := &http.Client{Timeout: 10 * time.Second}
		resp, err = client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}

		respBytes, err = io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			lastErr = err
			continue
		}

		if resp.StatusCode == http.StatusForbidden {
			lastErr = fmt.Errorf("status 403 forbidden: %s", string(respBytes))
			continue
		}

		// Success or other status code (like 429) found, break loop
		lastErr = nil
		break
	}

	if lastErr != nil && resp == nil {
		return nil, fmt.Errorf("failed to request quota summary: %w", lastErr)
	}

	if resp.StatusCode == http.StatusTooManyRequests {
		return &QuotaSummaryResponse{
			Groups:    []QuotaGroup{},
			Exhausted: true,
			Error:     "Resource has been exhausted (e.g. check quota).",
		}, nil
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("api returned status %d: %s", resp.StatusCode, string(respBytes))
	}

	type rawGroup struct {
		GroupName         string  `json:"groupName"`
		GroupNameSnake    string  `json:"group_name"`
		GroupDescription  string  `json:"groupDescription"`
		GroupDescSnake    string  `json:"group_description"`
		RemainingFraction float32 `json:"remainingFraction"`
		RemFractionSnake  float32 `json:"remaining_fraction"`
		ResetTime         string  `json:"resetTime"`
		ResetTimeSnake    string  `json:"reset_time"`
	}

	var quotaResp struct {
		Groups []rawGroup `json:"groups"`
	}
	if err := json.Unmarshal(respBytes, &quotaResp); err != nil {
		return nil, fmt.Errorf("failed to parse quota response: %w", err)
	}

	res := &QuotaSummaryResponse{
		Groups:    make([]QuotaGroup, len(quotaResp.Groups)),
		Exhausted: false,
	}
	for i, g := range quotaResp.Groups {
		name := g.GroupName
		if name == "" {
			name = g.GroupNameSnake
		}
		desc := g.GroupDescription
		if desc == "" {
			desc = g.GroupDescSnake
		}
		rem := g.RemainingFraction
		if rem == 0 && g.RemFractionSnake != 0 {
			rem = g.RemFractionSnake
		}
		reset := g.ResetTime
		if reset == "" {
			reset = g.ResetTimeSnake
		}
		res.Groups[i] = QuotaGroup{
			GroupName:         name,
			GroupDescription:  desc,
			RemainingFraction: rem,
			ResetTime:         reset,
		}
	}
	return res, nil
}

