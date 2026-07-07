package auth

import (
	"crypto/rand"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
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
	cmdStr := fmt.Sprintf("%s --print hello --dangerously-skip-permissions", agyPath)
	cmd := exec.Command("script", "-q", "-f", "-c", cmdStr, "/dev/null")
	done := make(chan error, 1)
	go func() {
		done <- cmd.Run()
	}()

	select {
	case err := <-done:
		if err == nil {
			tokenDir := filepath.Join(homeDir, ".gemini", "antigravity-cli")
			_ = os.MkdirAll(tokenDir, 0755)
			_ = os.WriteFile(tokenPath, []byte("keychain-authenticated-dummy-token"), 0600)
			log.Printf("[AUTH] Nemokake sesi keychain sing wis ana. Nggawe file dummy token.")
			return true
		}
	case <-time.After(8 * time.Second):
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
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
	cmdStr := fmt.Sprintf("%s --print hello --dangerously-skip-permissions", agyPath)
	cmd := exec.Command("script", "-q", "-f", "-c", cmdStr, "/dev/null")
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
		log.Printf("[AUTH ERROR] failed to start command: %v", err)
		return "", fmt.Errorf("failed to start agy: %w", err)
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
					if idx := strings.Index(output, "https://accounts.google.com/o/oauth2/auth"); idx != -1 {
						urlPart := output[idx:]
						if endIdx := strings.IndexAny(urlPart, " \r\n\t"); endIdx != -1 {
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
