package handler

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"mobile-agy/internal/auth"
	"mobile-agy/internal/chat"
	"mobile-agy/internal/terminal"
	"mobile-agy/internal/workspace"
)

const AppVersion = "v1.2.7"
var versionRegex = regexp.MustCompile(`v1\.2\.[0-9]+`)

type EmbeddedHTML struct {
	IndexHTML    string
	LoginHTML    string
	LoginPwdHTML string
}

type Handler struct {
	workspaceSvc *workspace.Service
	authSvc      *auth.Service
	chatSvc      *chat.Service
	terminalSvc  *terminal.Service
	html         EmbeddedHTML
}

func NewHandler(
	workspaceSvc *workspace.Service,
	authSvc *auth.Service,
	chatSvc *chat.Service,
	terminalSvc *terminal.Service,
	html EmbeddedHTML,
) *Handler {
	return &Handler{
		workspaceSvc: workspaceSvc,
		authSvc:      authSvc,
		chatSvc:      chatSvc,
		terminalSvc:  terminalSvc,
		html:         html,
	}
}

func (h *Handler) enableCORS(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
}

// AuthMiddleware multi-layer authentication
func (h *Handler) AuthMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			h.enableCORS(w)
			return
		}

		isPublicAPI := r.URL.Path == "/api/auth/pwd"
		isPasswordPage := r.URL.Path == "/login-pwd"

		// 1. LAYER 1: Password authentication check
		isPasswordAuthPassed := false
		cookie, err := r.Cookie("session_password")
		if err == nil {
			isPasswordAuthPassed = h.authSvc.ValidateSession(cookie.Value)
		} else {
			// If no password is set, it counts as passed
			isPasswordAuthPassed = h.authSvc.VerifyPassword("")
		}

		if !isPasswordAuthPassed {
			if strings.HasPrefix(r.URL.Path, "/api/") && !isPublicAPI {
				h.enableCORS(w)
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}
			if !isPublicAPI && !isPasswordPage {
				http.Redirect(w, r, "/login-pwd", http.StatusFound)
				return
			}
		} else {
			if isPasswordPage {
				http.Redirect(w, r, "/", http.StatusFound)
				return
			}

			// 2. LAYER 2: Google Antigravity OAuth check
			isPublicGoogleAPI := r.URL.Path == "/api/auth/start" || r.URL.Path == "/api/auth/submit" || r.URL.Path == "/api/auth/status"
			isGoogleLoginPage := r.URL.Path == "/login"

			isGoogleAuthPassed := h.authSvc.CheckOAuthTokenExists()

			if !isGoogleAuthPassed {
				if strings.HasPrefix(r.URL.Path, "/api/") && !isPublicGoogleAPI && r.URL.Path != "/api/auth/logout" {
					h.enableCORS(w)
					http.Error(w, "Unauthorized", http.StatusUnauthorized)
					return
				}
				if !isPublicGoogleAPI && !isGoogleLoginPage {
					http.Redirect(w, r, "/login", http.StatusFound)
					return
				}
			} else {
				if isGoogleLoginPage {
					http.Redirect(w, r, "/", http.StatusFound)
					return
				}
			}
		}

		next(w, r)
	}
}

// HandleIndex serves the main IDE single-page application
func (h *Handler) HandleIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	var htmlContent string
	content, err := os.ReadFile(filepath.Join(h.workspaceSvc.ServerStartDir(), "index.html"))
	if err == nil {
		htmlContent = string(content)
	} else {
		htmlContent = h.html.IndexHTML
	}

	// Dynamically replace hardcoded version "v1.2.x" with current AppVersion
	htmlContent = versionRegex.ReplaceAllString(htmlContent, AppVersion)

	_, _ = w.Write([]byte(htmlContent))
}

// HandleLoginPage serves Google OAuth login page
func (h *Handler) HandleLoginPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	content, err := os.ReadFile(filepath.Join(h.workspaceSvc.ServerStartDir(), "login.html"))
	if err == nil {
		_, _ = w.Write(content)
		return
	}
	_, _ = w.Write([]byte(h.html.LoginHTML))
}

// HandleLoginPwdPage serves general password lock page
func (h *Handler) HandleLoginPwdPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	content, err := os.ReadFile(filepath.Join(h.workspaceSvc.ServerStartDir(), "login-pwd.html"))
	if err == nil {
		_, _ = w.Write(content)
		return
	}
	_, _ = w.Write([]byte(h.html.LoginPwdHTML))
}

// HandlePasswordAuth processes password login API
func (h *Handler) HandlePasswordAuth(w http.ResponseWriter, r *http.Request) {
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

	if !h.authSvc.VerifyPassword(pwd) {
		http.Error(w, "Sandi keamanan salah!", http.StatusUnauthorized)
		return
	}

	token := h.authSvc.InitSession()

	http.SetCookie(w, &http.Cookie{
		Name:     "session_password",
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		MaxAge:   86400 * 30, // 30 days
	})

	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("Sukses mlebu"))
}

// HandleAuthStatus gets Google OAuth authentication status
func (h *Handler) HandleAuthStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"authenticated": h.authSvc.CheckOAuthTokenExists(),
		"email":         h.authSvc.GetAuthenticatedEmail(),
		"project":       h.authSvc.GetGCPProject(),
		"version":       AppVersion,
	})
}

// HandleQuotaSummary retrieves user quota summary details
func (h *Handler) HandleQuotaSummary(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	quota, err := h.authSvc.GetQuotaSummary()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_ = json.NewEncoder(w).Encode(quota)
}


// HandleAuthStart initiates Google OAuth flow via agy
func (h *Handler) HandleAuthStart(w http.ResponseWriter, r *http.Request) {
	url, err := h.authSvc.StartGoogleAuth(h.workspaceSvc.ActiveWorkspaceDir())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"url": url,
	})
}

// HandleAuthSubmit submits verification code
func (h *Handler) HandleAuthSubmit(w http.ResponseWriter, r *http.Request) {
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

	err := h.authSvc.SubmitGoogleAuthCode(code)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("Sukses mlebu"))
}

// HandleLogout handles logging out
func (h *Handler) HandleLogout(w http.ResponseWriter, r *http.Request) {
	h.authSvc.Logout()

	http.SetCookie(w, &http.Cookie{
		Name:     "session_password",
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		MaxAge:   -1,
	})

	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("Sukses logout"))
}

// HandleWorkspacesGet lists recent and active workspaces
func (h *Handler) HandleWorkspacesGet(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(workspace.WorkspaceSettings{
		Active: h.workspaceSvc.ActiveWorkspaceDir(),
		List:   h.workspaceSvc.WorkspacesList(),
	})
}

// HandleWorkspaceSelect switches active workspace
func (h *Handler) HandleWorkspaceSelect(w http.ResponseWriter, r *http.Request) {
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

	err := h.workspaceSvc.Select(path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("Workspace aktif diubah"))
}

// HandleWorkspaceAdd creates/adds a workspace
func (h *Handler) HandleWorkspaceAdd(w http.ResponseWriter, r *http.Request) {
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

	err := h.workspaceSvc.Add(path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("Workspace ditambah lan dibukak"))
}

// HandleListFiles returns active workspace directory contents
func (h *Handler) HandleListFiles(w http.ResponseWriter, r *http.Request) {
	files, err := h.workspaceSvc.ListFiles()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(files)
}

// HandleFileOperations handles GET (read), POST (write), and DELETE (remove) for files
func (h *Handler) HandleFileOperations(w http.ResponseWriter, r *http.Request) {
	pathParam := r.URL.Query().Get("path")
	if pathParam == "" {
		http.Error(w, "missing path parameter", http.StatusBadRequest)
		return
	}

	switch r.Method {
	case http.MethodGet:
		content, err := h.workspaceSvc.ReadFile(pathParam)
		if err != nil {
			http.Error(w, fmt.Sprintf("Gagal maca file: %v", err), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write(content)

	case http.MethodPost:
		content, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, fmt.Sprintf("Gagal maca data body: %v", err), http.StatusBadRequest)
			return
		}
		defer r.Body.Close()

		err = h.workspaceSvc.WriteFile(pathParam, content)
		if err != nil {
			http.Error(w, fmt.Sprintf("Gagal nulis file: %v", err), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("File sukses disimpen"))

	case http.MethodDelete:
		err := h.workspaceSvc.DeleteFile(pathParam)
		if err != nil {
			http.Error(w, fmt.Sprintf("Gagal mbusak file: %v", err), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("Sukses mbusak file/folder"))

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// HandleCreateFileOrFolder creates file/folder elements
func (h *Handler) HandleCreateFileOrFolder(w http.ResponseWriter, r *http.Request) {
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

	err := h.workspaceSvc.CreateFileOrFolder(pathParam, isDir)
	if err != nil {
		http.Error(w, fmt.Sprintf("Gagal nggawe elemen baru: %v", err), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusCreated)
	_, _ = w.Write([]byte("Sukses nggawe elemen baru"))
}

// HandlePreviewFile serves static workspace file preview
func (h *Handler) HandlePreviewFile(w http.ResponseWriter, r *http.Request) {
	relPath := strings.TrimPrefix(r.URL.Path, "/preview/")
	if relPath == "" {
		http.Error(w, "missing file path", http.StatusBadRequest)
		return
	}

	absPath, err := h.workspaceSvc.ResolvePath(relPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}

	http.ServeFile(w, r, absPath)
}

// HandleChatHistoryList lists chat histories
func (h *Handler) HandleChatHistoryList(w http.ResponseWriter, r *http.Request) {
	list, err := h.chatSvc.GetHistory(h.workspaceSvc.ActiveWorkspaceDir())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(list)
}

// HandleChatHistoryDetail details one chat session
func (h *Handler) HandleChatHistoryDetail(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		http.Error(w, "missing id parameter", http.StatusBadRequest)
		return
	}

	detail, err := h.chatSvc.GetHistoryDetail(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(detail)
}

// HandleChatStream streams Antigravity chatbot interaction
func (h *Handler) HandleChatStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req chat.ChatRequest
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

	cmd, stdoutPipe, err := h.chatSvc.StartChat(r.Context(), req, h.workspaceSvc.ActiveWorkspaceDir())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	convID := req.Conversation
	defer h.chatSvc.CleanupChat(convID, cmd)

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	dataChan := make(chan []byte)
	errChan := make(chan error)

	go func() {
		buf := make([]byte, 1024)
		for {
			n, err := stdoutPipe.Read(buf)
			if n > 0 {
				tmp := make([]byte, n)
				copy(tmp, buf[:n])
				dataChan <- tmp
			}
			if err != nil {
				errChan <- err
				return
			}
		}
	}()

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

Loop:
	for {
		select {
		case data := <-dataChan:
			_, _ = w.Write(data)
			flusher.Flush()
		case err := <-errChan:
			if err != io.EOF {
				log.Printf("[STREAM ERROR] %v", err)
			}
			break Loop
		case <-ticker.C:
			_, _ = w.Write([]byte("<!-- keep-alive -->"))
			flusher.Flush()
		case <-r.Context().Done():
			break Loop
		}
	}

	_ = cmd.Wait()
}

// HandleChatStop stops running chat command
func (h *Handler) HandleChatStop(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		http.Error(w, "missing id parameter", http.StatusBadRequest)
		return
	}

	stopped := h.chatSvc.StopChat(id)
	w.WriteHeader(http.StatusOK)
	if stopped {
		_, _ = w.Write([]byte("Sukses mateni agen"))
	} else {
		_, _ = w.Write([]byte("Agen ora lagi mlaku"))
	}
}

// HandleChatDelete deletes conversation
func (h *Handler) HandleChatDelete(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		http.Error(w, "missing id parameter", http.StatusBadRequest)
		return
	}

	err := h.chatSvc.DeleteChat(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("Sukses mbusak agen"))
}

// HandleRunCommandStream runs shell scripts on workspace
func (h *Handler) HandleRunCommandStream(w http.ResponseWriter, r *http.Request) {
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

	cmd, stdoutPipe, err := h.terminalSvc.StartCommand(r.Context(), command, h.workspaceSvc.ActiveWorkspaceDir())
	if err != nil {
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

	processDone := make(chan struct{})
	go func() {
		if cmd.Process != nil {
			_, _ = cmd.Process.Wait()
		}
		close(processDone)
		_ = stdoutPipe.Close()
	}()

	go func() {
		select {
		case <-processDone:
		case <-r.Context().Done():
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
		}
	}()

	dataChan := make(chan []byte)
	errChan := make(chan error)

	go func() {
		buf := make([]byte, 1024)
		for {
			n, err := stdoutPipe.Read(buf)
			if n > 0 {
				tmp := make([]byte, n)
				copy(tmp, buf[:n])
				dataChan <- tmp
			}
			if err != nil {
				errChan <- err
				return
			}
		}
	}()

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

LoopCmd:
	for {
		select {
		case data := <-dataChan:
			_, _ = w.Write(data)
			flusher.Flush()
		case err := <-errChan:
			if err != io.EOF {
				log.Printf("[STREAM ERROR] %v", err)
			}
			break LoopCmd
		case <-ticker.C:
			_, _ = w.Write([]byte("<!-- keep-alive -->"))
			flusher.Flush()
		case <-r.Context().Done():
			break LoopCmd
		}
	}

	_ = cmd.Wait()
}

// HandleModelsList lists active models
func (h *Handler) HandleModelsList(w http.ResponseWriter, r *http.Request) {
	models, err := h.terminalSvc.GetModelsList()
	if err != nil {
		http.Error(w, "Gagal ngakses daftar model: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(models)
}

// HandleGithubWebhook runs webhook sync
func (h *Handler) HandleGithubWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	log.Printf("[WEBHOOK] GitHub Webhook diterima, memulai git pull di background...")
	serverStartDir := h.workspaceSvc.ServerStartDir()
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
	_, _ = w.Write([]byte("Webhook received successfully. Pulling changes..."))
}

// HandleSelfUpdate triggers a background update process
func (h *Handler) HandleSelfUpdate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	log.Printf("[UPDATE] Memulai pembaruan server otomatis...")

	// Extract active port and password to preserve them during update
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	password := h.authSvc.GetPassword()

	envContent := fmt.Sprintf("PORT=%s\nPASSWORD=%s\n", port, password)
	startDir := h.workspaceSvc.ServerStartDir()

	// Write to start directory .env
	_ = os.WriteFile(filepath.Join(startDir, ".env"), []byte(envContent), 0600)

	// Write to mobile-ide subdirectory .env
	mobileIdeDir := filepath.Join(startDir, "mobile-ide")
	_ = os.MkdirAll(mobileIdeDir, 0755)
	_ = os.WriteFile(filepath.Join(mobileIdeDir, ".env"), []byte(envContent), 0600)

	go func() {
		// Tunggu 1 detik agar respon HTTP 200 OK sampai ke klien (HP) sebelum server mati/di-update
		time.Sleep(1 * time.Second)

		scriptPath := filepath.Join(startDir, "update.sh")
		var cmd *exec.Cmd
		if _, err := os.Stat(scriptPath); err == nil {
			cmd = exec.Command("bash", "-c", "nohup ./update.sh > update.log 2>&1 &")
		} else {
			cmd = exec.Command("bash", "-c", "nohup curl -H \"Cache-Control: no-cache\" -fsSL https://raw.githubusercontent.com/sodikinnaa/go-agy-ide/main/install.sh | bash > update.log 2>&1 &")
		}
		cmd.Dir = startDir
		_ = cmd.Run()
	}()

	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("Pembaruan dimulai. Server bakal di-restart otomatis."))
}
