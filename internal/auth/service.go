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

var HomeDirOverride string

func getHomeDir() (string, error) {
	if HomeDirOverride != "" {
		return HomeDirOverride, nil
	}
	return os.UserHomeDir()
}

func FindAgyPath() string {
	if p := os.Getenv("AGY_PATH"); p != "" {
		return p
	}

	if p, err := exec.LookPath("agy"); err == nil {
		return p
	}

	homeDir, err := getHomeDir()
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

	homeDir, err := getHomeDir()
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

	// 1. Check active keyring slot
	val, err := keyring.Get("gemini", "antigravity")
	if err == nil && val != "" {
		_ = os.MkdirAll(filepath.Dir(tokenPath), 0755)
		_ = os.WriteFile(tokenPath, []byte("keychain-authenticated-dummy-token"), 0600)
		log.Printf("[AUTH] Active credentials found in keyring. Restored dummy token file.")
		return true
	}

	// 2. Check pool fallback
	pool, err := s.LoadAccountsPool()
	if err == nil && len(pool) > 0 {
		// If we have accounts in the pool, restore the first one to the active slot
		_ = keyring.Set("gemini", "antigravity", pool[0].KeyringValue)
		_ = os.MkdirAll(filepath.Dir(tokenPath), 0755)
		_ = os.WriteFile(tokenPath, []byte("keychain-authenticated-dummy-token"), 0600)
		log.Printf("[AUTH] Keyring was empty but pool has credentials. Restored first account from pool.")
		return true
	}

	return false
}

func (s *Service) StartGoogleAuth(activeWorkspaceDir string) (string, error) {
	s.mu.Lock()
	locked := true
	defer func() {
		if locked {
			s.mu.Unlock()
		}
	}()

	// Backup current active keyring and dummy token file
	backupVal, backupErr := keyring.Get("gemini", "antigravity")
	if backupErr == nil {
		_ = keyring.Delete("gemini", "antigravity")
	}

	homeDir, _ := getHomeDir()
	tokenPath := filepath.Join(homeDir, ".gemini", "antigravity-cli", "antigravity-oauth-token")
	_ = os.Remove(tokenPath)

	if s.activeAuthCmd != nil && s.activeAuthCmd.Process != nil {
		_ = s.activeAuthCmd.Process.Kill()
	}

	agyPath := FindAgyPath()
	var cmd *exec.Cmd
	useDirect := false

	if _, err := exec.LookPath("script"); err != nil || os.Getenv("FORCE_DIRECT_AUTH") == "true" {
		log.Printf("[AUTH] 'script' utility not found or forced direct. Using direct command execution.")
		useDirect = true
	}

	if useDirect {
		cmd = exec.Command(agyPath, "--print", "hello", "--dangerously-skip-permissions")
	} else {
		cmdStr := fmt.Sprintf("%s --print hello --dangerously-skip-permissions", agyPath)
		cmd = exec.Command("script", "-q", "-f", "-c", cmdStr, "/dev/null")
	}
	cmd.Dir = activeWorkspaceDir
	cmd.Env = append(os.Environ(), "DISPLAY=", "BROWSER=false")

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		if backupErr == nil && backupVal != "" {
			_ = keyring.Set("gemini", "antigravity", backupVal)
			_ = os.WriteFile(tokenPath, []byte("keychain-authenticated-dummy-token"), 0600)
		}
		return "", fmt.Errorf("failed to create stdin pipe: %w", err)
	}

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		if backupErr == nil && backupVal != "" {
			_ = keyring.Set("gemini", "antigravity", backupVal)
			_ = os.WriteFile(tokenPath, []byte("keychain-authenticated-dummy-token"), 0600)
		}
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
			cmd.Env = append(os.Environ(), "DISPLAY=", "BROWSER=false")
			stdinPipe, err = cmd.StdinPipe()
			if err != nil {
				if backupErr == nil && backupVal != "" {
					_ = keyring.Set("gemini", "antigravity", backupVal)
					_ = os.WriteFile(tokenPath, []byte("keychain-authenticated-dummy-token"), 0600)
				}
				return "", fmt.Errorf("failed to create stdin pipe: %w", err)
			}
			stdoutPipe, err = cmd.StdoutPipe()
			if err != nil {
				if backupErr == nil && backupVal != "" {
					_ = keyring.Set("gemini", "antigravity", backupVal)
					_ = os.WriteFile(tokenPath, []byte("keychain-authenticated-dummy-token"), 0600)
				}
				return "", fmt.Errorf("failed to create stdout pipe: %w", err)
			}
			cmd.Stderr = cmd.Stdout
			if err := cmd.Start(); err != nil {
				if backupErr == nil && backupVal != "" {
					_ = keyring.Set("gemini", "antigravity", backupVal)
					_ = os.WriteFile(tokenPath, []byte("keychain-authenticated-dummy-token"), 0600)
				}
				return "", fmt.Errorf("failed to start agy directly: %w", err)
			}
		} else {
			if backupErr == nil && backupVal != "" {
				_ = keyring.Set("gemini", "antigravity", backupVal)
				_ = os.WriteFile(tokenPath, []byte("keychain-authenticated-dummy-token"), 0600)
			}
			return "", fmt.Errorf("failed to start agy: %w", err)
		}
	}

	s.activeAuthCmd = cmd
	s.activeAuthStdin = stdinPipe
	s.activeAuthURL = ""
	locked = false
	s.mu.Unlock()

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
					strings.Contains(lowerOut, "select project") ||
					strings.Contains(lowerOut, "select a project") ||
					strings.Contains(lowerOut, "choose project") ||
					strings.Contains(lowerOut, "gcp project") ||
					strings.Contains(lowerOut, "google cloud project") ||
					strings.Contains(lowerOut, "project id") ||
					strings.Contains(lowerOut, "which project") ||
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

							// Restore keyring immediately once URL is generated
							if backupErr == nil && backupVal != "" {
								_ = keyring.Set("gemini", "antigravity", backupVal)
								_ = os.WriteFile(tokenPath, []byte("keychain-authenticated-dummy-token"), 0600)
							}
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

	// Restore keyring if timeout reached and URL not found
	if backupErr == nil && backupVal != "" {
		_ = keyring.Set("gemini", "antigravity", backupVal)
		_ = os.WriteFile(tokenPath, []byte("keychain-authenticated-dummy-token"), 0600)
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

	// Backup current active keyring and dummy token file
	backupVal, backupErr := keyring.Get("gemini", "antigravity")
	if backupErr == nil {
		_ = keyring.Delete("gemini", "antigravity")
	}

	homeDir, _ := getHomeDir()
	tokenPath := filepath.Join(homeDir, ".gemini", "antigravity-cli", "antigravity-oauth-token")
	_ = os.Remove(tokenPath)

	_, err := io.WriteString(stdin, code+"\n")
	if err != nil {
		if backupErr == nil && backupVal != "" {
			_ = keyring.Set("gemini", "antigravity", backupVal)
			_ = os.WriteFile(tokenPath, []byte("keychain-authenticated-dummy-token"), 0600)
		}
		return fmt.Errorf("failed to write code to stdin: %w", err)
	}

	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	var waitErr error
	select {
	case err := <-done:
		if err != nil {
			if s.CheckOAuthTokenExists() {
				waitErr = nil
			} else {
				waitErr = fmt.Errorf("agy authentication failed: %w", err)
			}
		}
	case <-time.After(15 * time.Second):
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		waitErr = fmt.Errorf("agy authentication timeout (15s)")
	}

	if waitErr != nil {
		if backupErr == nil && backupVal != "" {
			_ = keyring.Set("gemini", "antigravity", backupVal)
			_ = os.WriteFile(tokenPath, []byte("keychain-authenticated-dummy-token"), 0600)
		}
		return waitErr
	}

	// Success! Read the newly generated token from the keyring
	newVal, err := keyring.Get("gemini", "antigravity")
	if err == nil && newVal != "" {
		// Sync this new token to the pool
		var kt struct {
			Token struct {
				AccessToken string `json:"access_token"`
			} `json:"token"`
		}
		if json.Unmarshal([]byte(newVal), &kt) == nil && kt.Token.AccessToken != "" {
			email, fetchErr := fetchEmailFromToken(kt.Token.AccessToken)
			if fetchErr != nil {
				email = s.GetAuthenticatedEmail()
			}
			if email == "" {
				email = "Unknown Account"
			}
			pool, loadErr := s.LoadAccountsPool()
			if loadErr == nil {
				found := false
				for i, entry := range pool {
					if entry.Email == email {
						pool[i].KeyringValue = newVal
						found = true
						break
					}
				}
				if !found {
					pool = append(pool, AccountEntry{
						Email:        email,
						KeyringValue: newVal,
					})
				}
				_ = s.SaveAccountsPool(pool)
			}
		}
	}

	// Restore original active keyring value!
	if backupErr == nil && backupVal != "" {
		_ = keyring.Set("gemini", "antigravity", backupVal)
		_ = os.WriteFile(tokenPath, []byte("keychain-authenticated-dummy-token"), 0600)
	} else {
		// If there was no original backup, keep the new token active
		if err == nil && newVal != "" {
			_ = keyring.Set("gemini", "antigravity", newVal)
			_ = os.WriteFile(tokenPath, []byte("keychain-authenticated-dummy-token"), 0600)
		}
	}

	return nil
}

func (s *Service) Logout() {
	homeDir, err := getHomeDir()
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
	homeDir, err := getHomeDir()
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
	homeDir, err := getHomeDir()
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

	type rawBucket struct {
		BucketID          string  `json:"bucketId"`
		DisplayName       string  `json:"displayName"`
		Window            string  `json:"window"`
		ResetTime         string  `json:"resetTime"`
		Description       string  `json:"description"`
		RemainingFraction float32 `json:"remainingFraction"`
	}

	type rawGroup struct {
		DisplayName string      `json:"displayName"`
		Description string      `json:"description"`
		Buckets     []rawBucket `json:"buckets"`
	}

	var quotaResp struct {
		Groups []rawGroup `json:"groups"`
	}
	if err := json.Unmarshal(respBytes, &quotaResp); err != nil {
		return nil, fmt.Errorf("failed to parse quota response: %w", err)
	}

	res := &QuotaSummaryResponse{
		Groups:    []QuotaGroup{},
		Exhausted: false,
	}
	for _, g := range quotaResp.Groups {
		for _, b := range g.Buckets {
			// Combine group display name and bucket display name for the UI label
			name := fmt.Sprintf("%s (%s)", g.DisplayName, b.DisplayName)
			res.Groups = append(res.Groups, QuotaGroup{
				GroupName:         name,
				GroupDescription:  b.Description,
				RemainingFraction: b.RemainingFraction,
				ResetTime:         b.ResetTime,
			})
		}
	}
	return res, nil
}

type AccountEntry struct {
	Email        string `json:"email"`
	KeyringValue string `json:"keyringValue"`
}

func fetchEmailFromToken(accessToken string) (string, error) {
	req, err := http.NewRequest("GET", "https://www.googleapis.com/oauth2/v2/userinfo", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("userinfo API returned status %d", resp.StatusCode)
	}
	var data struct {
		Email string `json:"email"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return "", err
	}
	return data.Email, nil
}

func (s *Service) GetAccountsPoolPath() string {
	homeDir, _ := getHomeDir()
	return filepath.Join(homeDir, ".gemini", "antigravity-cli", "accounts_pool.json")
}

func (s *Service) LoadAccountsPool() ([]AccountEntry, error) {
	path := s.GetAccountsPoolPath()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return []AccountEntry{}, nil
		}
		return nil, err
	}
	var pool []AccountEntry
	if err := json.Unmarshal(data, &pool); err != nil {
		return nil, err
	}
	return pool, nil
}

func (s *Service) SaveAccountsPool(pool []AccountEntry) error {
	path := s.GetAccountsPoolPath()
	_ = os.MkdirAll(filepath.Dir(path), 0755)
	data, err := json.MarshalIndent(pool, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

func (s *Service) SyncCurrentAccountToPool() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// 1. Get current keyring value
	val, err := keyring.Get("gemini", "antigravity")
	if err != nil {
		return nil // Not logged in yet
	}

	// 2. Parse token
	var kt struct {
		Token struct {
			AccessToken string `json:"access_token"`
		} `json:"token"`
	}
	if err := json.Unmarshal([]byte(val), &kt); err != nil {
		return err
	}

	if kt.Token.AccessToken == "" {
		return nil
	}

	// 3. Get email
	email, err := fetchEmailFromToken(kt.Token.AccessToken)
	if err != nil {
		// Fallback to log parsing
		email = s.GetAuthenticatedEmail()
	}

	if email == "" {
		email = "Unknown Account"
	}

	// 4. Load pool
	pool, err := s.LoadAccountsPool()
	if err != nil {
		pool = []AccountEntry{}
	}

	// 5. Update or add
	found := false
	for i, entry := range pool {
		if entry.Email == email {
			pool[i].KeyringValue = val
			found = true
			break
		}
	}
	if !found {
		pool = append(pool, AccountEntry{
			Email:        email,
			KeyringValue: val,
		})
	}

	return s.SaveAccountsPool(pool)
}

func (s *Service) SwitchAccount(email string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	pool, err := s.LoadAccountsPool()
	if err != nil {
		return err
	}

	var targetVal string
	for _, entry := range pool {
		if entry.Email == email {
			targetVal = entry.KeyringValue
			break
		}
	}

	if targetVal == "" {
		return fmt.Errorf("account %s not found in pool", email)
	}

	// Set active keyring
	err = keyring.Set("gemini", "antigravity", targetVal)
	if err != nil {
		return fmt.Errorf("failed to write to keyring: %w", err)
	}

	// Write dummy token file to signal we are logged in
	homeDir, err := getHomeDir()
	if err == nil {
		tokenPath := filepath.Join(homeDir, ".gemini", "antigravity-cli", "antigravity-oauth-token")
		_ = os.WriteFile(tokenPath, []byte("keychain-authenticated-dummy-token"), 0600)
	}

	return nil
}

func (s *Service) DeleteAccount(email string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	pool, err := s.LoadAccountsPool()
	if err != nil {
		return err
	}

	newPool := []AccountEntry{}
	for _, entry := range pool {
		if entry.Email != email {
			newPool = append(newPool, entry)
		}
	}

	err = s.SaveAccountsPool(newPool)
	if err != nil {
		return err
	}

	// If the deleted account is the active one, log out of it
	currentEmail := s.GetAuthenticatedEmail()
	if currentEmail == email {
		homeDir, err := getHomeDir()
		if err == nil {
			tokenPath := filepath.Join(homeDir, ".gemini", "antigravity-cli", "antigravity-oauth-token")
			_ = os.Remove(tokenPath)
		}
		_ = keyring.Delete("gemini", "antigravity")
	}

	return nil
}
