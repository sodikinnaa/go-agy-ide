package main

import (
	"crypto/rand"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
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
var sessionToken string = ""

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

	// Routes
	http.HandleFunc("/", handleIndex)
	http.HandleFunc("/login", handleLoginPage)
	
	// Authentication APIs
	http.HandleFunc("/api/auth/start", handleAuthStart)
	http.HandleFunc("/api/auth/submit", handleAuthSubmit)
	http.HandleFunc("/api/auth/logout", handleLogout)
	
	// Workspace and project files APIs
	http.HandleFunc("/api/files", handleListFiles)
	http.HandleFunc("/api/file", handleFileOperations)
	http.HandleFunc("/api/file/create", handleCreateFileOrFolder)
	http.HandleFunc("/api/chat", handleChatStream)
	http.HandleFunc("/api/run", handleRunCommandStream)
	http.HandleFunc("/api/workspaces", handleWorkspacesGet)
	http.HandleFunc("/api/workspaces/select", handleWorkspaceSelect)
	http.HandleFunc("/api/workspaces/add", handleWorkspaceAdd)

	fmt.Printf("Mulai server Mobile IDE ing http://0.0.0.0:%s ...\n", port)
	fmt.Printf("Workspace root aktif: %s\n", activeWorkspaceDir)
	if err := http.ListenAndServe("0.0.0.0:"+port, nil); err != nil {
		fmt.Printf("Gagal nglakokake server: %v\n", err)
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

// Verifikasi auth (kudu nduweni token Google OAuth Antigravity aktif ing mesin)
func checkAuth(r *http.Request) bool {
	return checkOAuthTokenExists()
}

// Handler static html utama
func handleIndex(w http.ResponseWriter, r *http.Request) {
	if !checkAuth(r) {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	content, err := os.ReadFile(filepath.Join(serverStartDir, "index.html"))
	if err == nil {
		w.Write(content)
		return
	}
	w.Write([]byte(embeddedIndexHTML))
}

// Handler static html login
func handleLoginPage(w http.ResponseWriter, r *http.Request) {
	if checkAuth(r) {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	content, err := os.ReadFile(filepath.Join(serverStartDir, "login.html"))
	if err == nil {
		w.Write(content)
		return
	}
	w.Write([]byte(embeddedLoginHTML))
}

// API POST /api/auth/start - Mulai flow login Google resmi saka agy
func handleAuthStart(w http.ResponseWriter, r *http.Request) {
	enableCORS(w)
	if r.Method == http.MethodOptions {
		return
	}

	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Pateni auth process sadurunge yen isih mlaku
	if activeAuthCmd != nil && activeAuthCmd.Process != nil {
		activeAuthCmd.Process.Kill()
	}

	// Jalankan perintah agy sing memicu otentikasi login
	cmd := exec.Command("agy", "--print", "hello", "--dangerously-skip-permissions")
	cmd.Dir = activeWorkspaceDir

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
	cmd.Stderr = cmd.Stdout // gabungke stdout lan stderr

	if err := cmd.Start(); err != nil {
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
				output += string(buf[:n])
				if strings.Contains(output, "https://accounts.google.com/o/oauth2/auth") {
					lines := strings.Split(output, "\n")
					for _, line := range lines {
						line = strings.TrimSpace(line)
						if strings.HasPrefix(line, "https://accounts.google.com/o/oauth2/auth") {
							activeAuthURL = line
							break
						}
					}
				}
			}
			if err != nil {
				break
			}
		}
	}()

	// Enteni maksimal 5 detik kanggo entuk URL login Google
	for i := 0; i < 50; i++ {
		if activeAuthURL != "" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	if activeAuthURL == "" {
		http.Error(w, "Gagal entuk URL otentikasi saka agy (kemungkinan wis login)", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"url": activeAuthURL,
	})
}

// API POST /api/auth/submit - Ngirim Google Auth verification code menyang agy stdin
func handleAuthSubmit(w http.ResponseWriter, r *http.Request) {
	enableCORS(w)
	if r.Method == http.MethodOptions {
		return
	}

	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

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

	// Kirim kode verifikasi menyang stdin agy
	_, err := io.WriteString(activeAuthStdin, code+"\n")
	if err != nil {
		http.Error(w, "Gagal ngirim kode menyang agy: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Enteni agy ngrampungake proses verifikasi (maks 15 detik)
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

// API POST /api/auth/logout - Mbusak token Google agy ing server
func handleLogout(w http.ResponseWriter, r *http.Request) {
	enableCORS(w)
	if r.Method == http.MethodOptions {
		return
	}

	homeDir, err := os.UserHomeDir()
	if err == nil {
		tokenPath := filepath.Join(homeDir, ".gemini", "antigravity-cli", "antigravity-oauth-token")
		os.Remove(tokenPath) // Hapus file token resmi
	}

	w.WriteHeader(http.StatusOK)
	w.Write([]byte("Sukses logout"))
}

// GET /api/workspaces
func handleWorkspacesGet(w http.ResponseWriter, r *http.Request) {
	enableCORS(w)
	if r.Method == http.MethodOptions {
		return
	}
	if !checkAuth(r) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(WorkspaceSettings{
		Active: activeWorkspaceDir,
		List:   workspacesList,
	})
}

// POST /api/workspaces/select
func handleWorkspaceSelect(w http.ResponseWriter, r *http.Request) {
	enableCORS(w)
	if r.Method == http.MethodOptions {
		return
	}
	if !checkAuth(r) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

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
	enableCORS(w)
	if r.Method == http.MethodOptions {
		return
	}
	if !checkAuth(r) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

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
	enableCORS(w)
	if r.Method == http.MethodOptions {
		return
	}

	if !checkAuth(r) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

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
	enableCORS(w)
	if r.Method == http.MethodOptions {
		return
	}

	if !checkAuth(r) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

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
	enableCORS(w)
	if r.Method == http.MethodOptions {
		return
	}

	if !checkAuth(r) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

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
	enableCORS(w)
	if r.Method == http.MethodOptions {
		return
	}

	if !checkAuth(r) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

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
	enableCORS(w)
	if r.Method == http.MethodOptions {
		return
	}

	if !checkAuth(r) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

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
