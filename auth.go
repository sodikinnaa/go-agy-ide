package main

import (
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
	"time"
)

// Security and Authentication state variables
var secretPassword string
var passwordSessionToken string = ""

// Global variables for active Google OAuth authentication process
var activeAuthCmd *exec.Cmd
var activeAuthStdin io.WriteCloser
var activeAuthURL string
var bypassDynamicAuthCheck bool = false

// Load password saka environment variable utawa password.txt
func loadPassword() {
	secretPassword = os.Getenv("PASSWORD")
	if secretPassword != "" {
		log.Printf("[SECURITY] Sandi keamanan dimuat saka env variable PASSWORD\n")
		return
	}

	configPath := filepath.Join(serverStartDir, "password.txt")
	data, err := os.ReadFile(configPath)
	if err == nil {
		secretPassword = strings.TrimSpace(string(data))
		if secretPassword != "" {
			log.Printf("[SECURITY] Sandi keamanan dimuat saka %s\n", configPath)
			return
		}
	}

	// Generate random 8 character secure password
	secretPassword = generateRandomPassword(8)
	os.WriteFile(configPath, []byte(secretPassword), 0600)
	log.Printf("[SECURITY] Sandi keamanan login acak digawe: %s (disimpen ing password.txt)\n", secretPassword)
}

// Middleware Keamanan Pusat multi-layer (Password + Google OAuth)
func authMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Bypass CORS preflight requests
		if r.Method == http.MethodOptions {
			enableCORS(w)
			return
		}

		isPublicAPI := r.URL.Path == "/api/auth/pwd"
		isPasswordPage := r.URL.Path == "/login-pwd"

		// 1. LAYER 1: Verifikasi Sandi Keamanan
		isPasswordAuthPassed := false
		if secretPassword == "" {
			isPasswordAuthPassed = true
		} else {
			cookie, err := r.Cookie("session_password")
			if err == nil && passwordSessionToken != "" && cookie.Value == passwordSessionToken {
				isPasswordAuthPassed = true
			}
		}

		if !isPasswordAuthPassed {
			// Yen durung verifikasi sandi lan ngakses API -> bali 401
			if strings.HasPrefix(r.URL.Path, "/api/") && !isPublicAPI {
				enableCORS(w)
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}
			// Yen durung verifikasi sandi lan ngakses halaman -> redirect menyang /login-pwd
			if !isPublicAPI && !isPasswordPage {
				http.Redirect(w, r, "/login-pwd", http.StatusFound)
				return
			}
		} else {
			// Yen wis verifikasi sandi lan nyoba ngakses /login-pwd -> redirect menyang /
			if isPasswordPage {
				http.Redirect(w, r, "/", http.StatusFound)
				return
			}

			// 2. LAYER 2: Verifikasi Otentikasi Google Antigravity
			isPublicGoogleAPI := r.URL.Path == "/api/auth/start" || r.URL.Path == "/api/auth/submit" || r.URL.Path == "/api/auth/status"
			isGoogleLoginPage := r.URL.Path == "/login"

			isGoogleAuthPassed := checkOAuthTokenExists()

			if !isGoogleAuthPassed {
				// Yen durung login Google lan ngakses private API -> bali 401
				if strings.HasPrefix(r.URL.Path, "/api/") && !isPublicGoogleAPI && r.URL.Path != "/api/auth/logout" {
					enableCORS(w)
					http.Error(w, "Unauthorized", http.StatusUnauthorized)
					return
				}
				// Yen durung login Google lan ngakses private page -> redirect menyang /login
				if !isPublicGoogleAPI && !isGoogleLoginPage {
					http.Redirect(w, r, "/login", http.StatusFound)
					return
				}
			} else {
				// Yen wis login Google lan ngakses /login -> redirect menyang /
				if isGoogleLoginPage {
					http.Redirect(w, r, "/", http.StatusFound)
					return
				}
			}
		}

		next(w, r)
	}
}

// Generate random secure string
func generateRandomPassword(length int) string {
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

// Goleki path agy binary sing bener
func findAgyPath() string {
	// 1. Cek yen wis ana ing PATH
	if p, err := exec.LookPath("agy"); err == nil {
		return p
	}

	// 2. Cek ing folder local bin pangguna
	homeDir, err := os.UserHomeDir()
	if err == nil {
		p := filepath.Join(homeDir, ".local", "bin", "agy")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}

	// 3. Cek folder default Codespace /home/codespace/.local/bin
	p := "/home/codespace/.local/bin/agy"
	if _, err := os.Stat(p); err == nil {
		return p
	}

	// 4. Balikake default "agy" yen ora ditemokake
	return "agy"
}

// Cek apa token Google OAuth Antigravity wis ada ing server
func checkOAuthTokenExists() bool {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	tokenPath := filepath.Join(homeDir, ".gemini", "antigravity-cli", "antigravity-oauth-token")
	if _, err := os.Stat(tokenPath); err == nil {
		return true
	}

	// Yen file token ora ana, cek apa agy bisa mlaku tanpa prompt (keychain/env auth)
	// Kita batasi nganggo timeout 8 detik lan nggunakake script
	if bypassDynamicAuthCheck {
		return false
	}
	agyPath := findAgyPath()
	cmdStr := fmt.Sprintf("%s --print hello --dangerously-skip-permissions", agyPath)
	cmd := exec.Command("script", "-q", "-f", "-c", cmdStr, "/dev/null")
	done := make(chan error, 1)
	go func() {
		done <- cmd.Run()
	}()

	select {
	case err := <-done:
		if err == nil {
			// Kasil! Gawe file dummy token
			tokenDir := filepath.Join(homeDir, ".gemini", "antigravity-cli")
			os.MkdirAll(tokenDir, 0755)
			os.WriteFile(tokenPath, []byte("keychain-authenticated-dummy-token"), 0600)
			log.Printf("[AUTH] Nemokake sesi keychain sing wis ana. Nggawe file dummy token.")
			return true
		}
	case <-time.After(8 * time.Second):
		// Timeout -> mateni proses
		if cmd.Process != nil {
			cmd.Process.Kill()
		}
	}

	return false
}

// Handler static html login Google
func handleLoginPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	content, err := os.ReadFile(filepath.Join(serverStartDir, "login.html"))
	if err == nil {
		w.Write(content)
		return
	}
	w.Write([]byte(embeddedLoginHTML))
}

// Handler static html login Sandi Keamanan
func handleLoginPwdPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	content, err := os.ReadFile(filepath.Join(serverStartDir, "login-pwd.html"))
	if err == nil {
		w.Write(content)
		return
	}
	w.Write([]byte(embeddedLoginPwdHTML))
}

// API POST /api/auth/pwd - Verifikasi sandi keamanan
func handlePasswordAuth(w http.ResponseWriter, r *http.Request) {
	pwd := r.FormValue("password")
	if pwd == "" {
		var req struct {
			Password string `json:"password"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err == nil {
			pwd = req.Password
		}
	}

	if pwd == "" {
		http.Error(w, "Sandi ora oleh kosong", http.StatusBadRequest)
		return
	}

	if pwd != secretPassword {
		http.Error(w, "Sandi keamanan salah!", http.StatusUnauthorized)
		return
	}

	if passwordSessionToken == "" {
		passwordSessionToken = generateRandomPassword(32)
	}

	http.SetCookie(w, &http.Cookie{
		Name:     "session_password",
		Value:    passwordSessionToken,
		Path:     "/",
		HttpOnly: true,
		MaxAge:   86400 * 30, // 30 hari sesi
	})

	w.WriteHeader(http.StatusOK)
	w.Write([]byte("Sukses mlebu"))
}

// API GET /api/auth/status - Cek status otentikasi Google Antigravity ing server
func handleAuthStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{
		"authenticated": checkOAuthTokenExists(),
	})
}

// API POST /api/auth/start - Mulai flow login Google resmi saka agy
func handleAuthStart(w http.ResponseWriter, r *http.Request) {
	if checkOAuthTokenExists() {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"url": "already_authenticated",
		})
		return
	}

	// Pateni auth process sadurunge yen isih mlaku
	if activeAuthCmd != nil && activeAuthCmd.Process != nil {
		activeAuthCmd.Process.Kill()
	}

	agyPath := findAgyPath()
	cmdStr := fmt.Sprintf("%s --print hello --dangerously-skip-permissions", agyPath)
	cmd := exec.Command("script", "-q", "-f", "-c", cmdStr, "/dev/null")
	cmd.Dir = activeWorkspaceDir
	cmd.Env = os.Environ() // Propagasi environment variable lengkap (kaya PATH lan HOME)

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		http.Error(w, "Gagal nggawe stdin pipe: "+err.Error(), http.StatusInternalServerError)
		return
	}

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		http.Error(w, "Gagal nggawe stdout pipe: "+err.Error(), http.StatusInternalServerError)
		return
	}
	cmd.Stderr = cmd.Stdout

	log.Printf("[AUTH START] starting command: %v in dir: %s", cmd.Args, cmd.Dir)
	log.Printf("[AUTH START] PATH=%q HOME=%q", os.Getenv("PATH"), os.Getenv("HOME"))

	if err := cmd.Start(); err != nil {
		log.Printf("[AUTH ERROR] failed to start command: %v", err)
		http.Error(w, "Gagal nglakokake agy: "+err.Error(), http.StatusInternalServerError)
		return
	}

	activeAuthCmd = cmd
	activeAuthStdin = stdinPipe
	activeAuthURL = ""

	// Woco output agy ing background kanggo golek Google OAuth URL lan auto-respond prompts
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
					io.WriteString(stdinPipe, "1\n")
					output = "" // Reset buffer
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
					io.WriteString(stdinPipe, "\n")
					output = "" // Reset buffer
				}

				if activeAuthURL == "" {
					if idx := strings.Index(output, "https://accounts.google.com/o/oauth2/auth"); idx != -1 {
						urlPart := output[idx:]
						if endIdx := strings.IndexAny(urlPart, " \r\n\t"); endIdx != -1 {
							activeAuthURL = urlPart[:endIdx]
							log.Printf("[AUTH FOUND URL]: %s", activeAuthURL)
						}
					}
				}
			}
			if err != nil {
				log.Printf("[AUTH READ EOF/ERROR]: %v", err)
				break
			}
		}
		if activeAuthURL == "" {
			log.Printf("[AUTH ERROR] agy output was: %q\n", output)
		}
	}()

	// Enteni maksimal 20 detik kanggo entuk URL login Google (amarga agy kadhangkala butuh wektu kanggo inisialisasi)
	for i := 0; i < 200; i++ {
		if activeAuthURL != "" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	if activeAuthURL == "" {
		http.Error(w, "Gagal entuk URL otentikasi saka agy (kemungkinan timeout utawa wis login)", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"url": activeAuthURL,
	})
}

// API POST /api/auth/submit - Ngirim Google Auth verification code menyang agy stdin
func handleAuthSubmit(w http.ResponseWriter, r *http.Request) {
	code := r.FormValue("code")
	if code == "" {
		var req struct {
			Code string `json:"code"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err == nil {
			code = req.Code
		}
	}

	if code == "" {
		http.Error(w, "kode verifikasi ora oleh kosong", http.StatusBadRequest)
		return
	}

	if activeAuthCmd == nil || activeAuthStdin == nil {
		http.Error(w, "Ora ana sesi otentikasi sing mlaku", http.StatusBadRequest)
		return
	}

	_, err := io.WriteString(activeAuthStdin, code+"\n")
	if err != nil {
		http.Error(w, "Gagal ngirim kode menyang agy: "+err.Error(), http.StatusInternalServerError)
		return
	}

	done := make(chan error, 1)
	go func() {
		done <- activeAuthCmd.Wait()
	}()

	select {
	case err := <-done:
		if err != nil {
			if checkOAuthTokenExists() {
				w.WriteHeader(http.StatusOK)
				w.Write([]byte("Sukses mlebu"))
				return
			}
			http.Error(w, "Otentikasi agy gagal: "+err.Error(), http.StatusInternalServerError)
			return
		}
	case <-time.After(15 * time.Second):
		activeAuthCmd.Process.Kill()
		http.Error(w, "Otentikasi agy timeout (15s)", http.StatusRequestTimeout)
		return
	}

	if checkOAuthTokenExists() {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("Sukses mlebu"))
	} else {
		http.Error(w, "Verifikasi gagal: token Google ora kasil digawe", http.StatusInternalServerError)
	}
}

// API POST /api/auth/logout - Mbusak token Google agy ing server & cookie sandi
func handleLogout(w http.ResponseWriter, r *http.Request) {
	homeDir, err := os.UserHomeDir()
	if err == nil {
		tokenPath := filepath.Join(homeDir, ".gemini", "antigravity-cli", "antigravity-oauth-token")
		os.Remove(tokenPath) // Hapus file token resmi
	}

	// Hapus cookie sesi sandi keamanan
	http.SetCookie(w, &http.Cookie{
		Name:     "session_password",
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		MaxAge:   -1,
	})

	w.WriteHeader(http.StatusOK)
	w.Write([]byte("Sukses logout"))
}

func enableCORS(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
}

// POST /api/webhook
func handleGithubWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	log.Printf("[WEBHOOK] GitHub Webhook diterima, memulai git pull di background...")
	go func() {
		cmd := exec.Command("git", "pull")
		cmd.Dir = serverStartDir
		output, err := cmd.CombinedOutput()
		if err != nil {
			log.Printf("[WEBHOOK] Gagal menjalankan git pull: %v\nOutput: %s\n", err, string(output))
		} else {
			log.Printf("[WEBHOOK] Sukses git pull:\n%s\n", string(output))
		}
	}()

	w.WriteHeader(http.StatusOK)
	w.Write([]byte("Webhook received successfully. Pulling changes..."))
}
