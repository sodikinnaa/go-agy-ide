package main

import (
	"crypto/rand"
	_ "embed"
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

//go:embed index.html
var embeddedIndexHTML string

//go:embed login.html
var embeddedLoginHTML string

//go:embed login-pwd.html
var embeddedLoginPwdHTML string

type FileInfo struct {
	Path  string `json:"path"`
	Name  string `json:"name"`
	IsDir bool   `json:"isDir"`
	Size  int64  `json:"size"`
}

type ChatRequest struct {
	Prompt       string `json:"prompt"`
	Model        string `json:"model"`
	Continue     bool   `json:"continue"`
	Conversation string `json:"conversation"`
}

type WorkspaceSettings struct {
	Active string   `json:"active"`
	List   []string `json:"list"`
}

var serverStartDir string
var activeWorkspaceDir string
var workspacesList []string

// Security and Authentication state variables
var secretPassword string
var passwordSessionToken string = ""

// Global variables for active Google OAuth authentication process
var activeAuthCmd *exec.Cmd
var activeAuthStdin io.WriteCloser
var activeAuthURL string

func main() {
	var err error
	serverStartDir, err = filepath.Abs(".")
	if err != nil {
		fmt.Printf("Gagal mendapatkan path direktori saat ini: %v\n", err)
		os.Exit(1)
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	// Load workspaces
	loadWorkspaces()

	// Load or generate password for access control
	loadPassword()

	// Routes wrapped with authMiddleware
	http.HandleFunc("/", authMiddleware(handleIndex))
	http.HandleFunc("/login", authMiddleware(handleLoginPage))
	http.HandleFunc("/login-pwd", authMiddleware(handleLoginPwdPage))
	
	// Authentication APIs
	http.HandleFunc("/api/auth/start", authMiddleware(handleAuthStart))
	http.HandleFunc("/api/auth/submit", authMiddleware(handleAuthSubmit))
	http.HandleFunc("/api/auth/logout", authMiddleware(handleLogout))
	http.HandleFunc("/api/auth/status", authMiddleware(handleAuthStatus))
	http.HandleFunc("/api/auth/pwd", authMiddleware(handlePasswordAuth))
	
	// Workspace and project files APIs
	http.HandleFunc("/api/files", authMiddleware(handleListFiles))
	http.HandleFunc("/api/file", authMiddleware(handleFileOperations))
	http.HandleFunc("/api/file/create", authMiddleware(handleCreateFileOrFolder))
	http.HandleFunc("/api/chat", authMiddleware(handleChatStream))
	http.HandleFunc("/api/run", authMiddleware(handleRunCommandStream))
	http.HandleFunc("/api/workspaces", authMiddleware(handleWorkspacesGet))
	http.HandleFunc("/api/workspaces/select", authMiddleware(handleWorkspaceSelect))
	http.HandleFunc("/api/workspaces/add", authMiddleware(handleWorkspaceAdd))

	log.Printf("Mulai server Mobile IDE ing http://0.0.0.0:%s ...\n", port)
	log.Printf("Workspace root aktif: %s\n", activeWorkspaceDir)
	if err := http.ListenAndServe("0.0.0.0:"+port, nil); err != nil {
		log.Printf("Gagal nglakokake server: %v\n", err)
	}
}

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

// Load workspaces saka json
func loadWorkspaces() {
	configPath := filepath.Join(serverStartDir, "workspaces.json")
	file, err := os.ReadFile(configPath)
	if err != nil {
		activeWorkspaceDir = serverStartDir
		workspacesList = []string{serverStartDir}
		saveWorkspaces()
		return
	}

	var ws WorkspaceSettings
	if err := json.Unmarshal(file, &ws); err != nil {
		activeWorkspaceDir = serverStartDir
		workspacesList = []string{serverStartDir}
		return
	}

	activeWorkspaceDir = ws.Active
	workspacesList = ws.List

	if _, err := os.Stat(activeWorkspaceDir); os.IsNotExist(err) {
		activeWorkspaceDir = serverStartDir
	}
}

// Simpen workspaces
func saveWorkspaces() {
	configPath := filepath.Join(serverStartDir, "workspaces.json")
	ws := WorkspaceSettings{
		Active: activeWorkspaceDir,
		List:   workspacesList,
	}
	data, _ := json.MarshalIndent(ws, "", "  ")
	os.WriteFile(configPath, data, 0644)
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

// Cek apa token Google OAuth Antigravity wis ana ing server
func checkOAuthTokenExists() bool {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	tokenPath := filepath.Join(homeDir, ".gemini", "antigravity-cli", "antigravity-oauth-token")
	_, err = os.Stat(tokenPath)
	return err == nil
}

// Handler static html utama
func handleIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	content, err := os.ReadFile(filepath.Join(serverStartDir, "index.html"))
	if err == nil {
		w.Write(content)
		return
	}
	w.Write([]byte(embeddedIndexHTML))
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
	// Pateni auth process sadurunge yen isih mlaku
	if activeAuthCmd != nil && activeAuthCmd.Process != nil {
		activeAuthCmd.Process.Kill()
	}

	cmd := exec.Command("script", "-q", "-c", "agy --print hello --dangerously-skip-permissions", "/dev/null")
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

	// Woco output agy ing background kanggo golek Google OAuth URL
	go func() {
		buf := make([]byte, 1024)
		var output string
		for {
			n, err := stdoutPipe.Read(buf)
			if n > 0 {
				chunk := string(buf[:n])
				log.Printf("[AUTH READ CHUNK]: %q", chunk)
				output += chunk
				if idx := strings.Index(output, "https://accounts.google.com/o/oauth2/auth"); idx != -1 {
					urlPart := output[idx:]
					if endIdx := strings.IndexAny(urlPart, " \r\n\t"); endIdx != -1 {
						activeAuthURL = urlPart[:endIdx]
						log.Printf("[AUTH FOUND URL]: %s", activeAuthURL)
						break
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

// GET /api/workspaces
func handleWorkspacesGet(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(WorkspaceSettings{
		Active: activeWorkspaceDir,
		List:   workspacesList,
	})
}

// POST /api/workspaces/select
func handleWorkspaceSelect(w http.ResponseWriter, r *http.Request) {
	path := r.FormValue("path")
	if path == "" {
		var req struct {
			Path string `json:"path"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err == nil {
			path = req.Path
		}
	}

	if path == "" {
		http.Error(w, "path parameter missing", http.StatusBadRequest)
		return
	}

	absPath, err := filepath.Abs(path)
	if err != nil {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}

	info, err := os.Stat(absPath)
	if os.IsNotExist(err) || !info.IsDir() {
		http.Error(w, "direktori ora ketemu utawa dudu folder", http.StatusNotFound)
		return
	}

	activeWorkspaceDir = absPath
	saveWorkspaces()

	w.WriteHeader(http.StatusOK)
	w.Write([]byte("Workspace aktif diubah"))
}

// POST /api/workspaces/add
func handleWorkspaceAdd(w http.ResponseWriter, r *http.Request) {
	path := r.FormValue("path")
	if path == "" {
		var req struct {
			Path string `json:"path"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err == nil {
			path = req.Path
		}
	}

	if path == "" {
		http.Error(w, "path parameter missing", http.StatusBadRequest)
		return
	}

	absPath, err := filepath.Abs(path)
	if err != nil {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}

	err = os.MkdirAll(absPath, 0755)
	if err != nil {
		http.Error(w, fmt.Sprintf("Gagal nggawe folder anyar: %v", err), http.StatusInternalServerError)
		return
	}

	exists := false
	for _, item := range workspacesList {
		if item == absPath {
			exists = true
			break
		}
	}
	if !exists {
		workspacesList = append(workspacesList, absPath)
	}

	activeWorkspaceDir = absPath
	saveWorkspaces()

	w.WriteHeader(http.StatusOK)
	w.Write([]byte("Workspace ditambah lan dibukak"))
}

// Handler file list
func handleListFiles(w http.ResponseWriter, r *http.Request) {
	var files []FileInfo
	err := filepath.Walk(activeWorkspaceDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}

		rel, err := filepath.Rel(activeWorkspaceDir, path)
		if err != nil {
			return nil
		}

		if rel == "." {
			return nil
		}

		parts := strings.Split(rel, string(os.PathSeparator))
		for _, p := range parts {
			if strings.HasPrefix(p, ".") && p != "." && p != ".." {
				if info.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if p == "node_modules" {
				if info.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
		}

		if info.Name() == "mobile-agy" || info.Name() == "main" {
			return nil
		}

		files = append(files, FileInfo{
			Path:  rel,
			Name:  info.Name(),
			IsDir: info.IsDir(),
			Size:  info.Size(),
		})
		return nil
	})

	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(files)
}

// Handler file read, write, delete
func handleFileOperations(w http.ResponseWriter, r *http.Request) {
	pathParam := r.URL.Query().Get("path")
	if pathParam == "" {
		http.Error(w, "missing path parameter", http.StatusBadRequest)
		return
	}

	absPath := filepath.Join(activeWorkspaceDir, pathParam)
	if !strings.HasPrefix(absPath, activeWorkspaceDir) {
		http.Error(w, "Access Denied: Path traversal detected", http.StatusForbidden)
		return
	}

	switch r.Method {
	case http.MethodGet:
		content, err := os.ReadFile(absPath)
		if err != nil {
			http.Error(w, fmt.Sprintf("Gagal maca file: %v", err), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Write(content)

	case http.MethodPost:
		content, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, fmt.Sprintf("Gagal maca data body: %v", err), http.StatusBadRequest)
			return
		}
		defer r.Body.Close()

		err = os.WriteFile(absPath, content, 0644)
		if err != nil {
			http.Error(w, fmt.Sprintf("Gagal nulis file: %v", err), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("File sukses disimpen"))

	case http.MethodDelete:
		err := os.RemoveAll(absPath)
		if err != nil {
			http.Error(w, fmt.Sprintf("Gagal mbusak file: %v", err), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("Sukses mbusak file/folder"))

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// Create file or folder
func handleCreateFileOrFolder(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	pathParam := r.URL.Query().Get("path")
	isDir := r.URL.Query().Get("isDir") == "true"

	if pathParam == "" {
		http.Error(w, "missing path parameter", http.StatusBadRequest)
		return
	}

	absPath := filepath.Join(activeWorkspaceDir, pathParam)
	if !strings.HasPrefix(absPath, activeWorkspaceDir) {
		http.Error(w, "Access Denied: Path traversal detected", http.StatusForbidden)
		return
	}

	if isDir {
		err := os.MkdirAll(absPath, 0755)
		if err != nil {
			http.Error(w, fmt.Sprintf("Gagal nggawe folder: %v", err), http.StatusInternalServerError)
			return
		}
	} else {
		parent := filepath.Dir(absPath)
		err := os.MkdirAll(parent, 0755)
		if err != nil {
			http.Error(w, fmt.Sprintf("Gagal nggawe folder induk: %v", err), http.StatusInternalServerError)
			return
		}

		f, err := os.Create(absPath)
		if err != nil {
			http.Error(w, fmt.Sprintf("Gagal nggawe file: %v", err), http.StatusInternalServerError)
			return
		}
		f.Close()
	}

	w.WriteHeader(http.StatusCreated)
	w.Write([]byte("Sukses nggawe elemen baru"))
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

	cmd := exec.Command("agy", args...)
	cmd.Dir = activeWorkspaceDir
	cmd.Env = os.Environ()

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

// Handler terminal runner streaming
func handleRunCommandStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	command := r.FormValue("command")
	if command == "" {
		var req struct {
			Command string `json:"command"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err == nil {
			command = req.Command
		}
	}

	if command == "" {
		http.Error(w, "missing command", http.StatusBadRequest)
		return
	}

	cmd := exec.Command("bash", "-c", command)
	cmd.Dir = activeWorkspaceDir
	cmd.Env = os.Environ()

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

func enableCORS(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
}
