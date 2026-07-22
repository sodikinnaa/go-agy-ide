package handler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mobile-agy/internal/auth"
	"mobile-agy/internal/chat"
	"mobile-agy/internal/terminal"
	"mobile-agy/internal/workspace"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const AppVersion = "v1.5.9"

var versionRegex = regexp.MustCompile(`v1\.\d+\.\d+`)

type EmbeddedHTML struct {
	IndexHTML        string
	LoginHTML        string
	LoginPwdHTML     string
	ManifestJSON     string
	ServiceWorkerJS  string
	Icon192          []byte
	Icon512          []byte
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

			// 2. LAYER 2: Google Antigravity OAuth check. OpenAI-compatible
			// settings are allowed after password auth so users can configure an
			// alternate AI provider without completing agy OAuth first.
			isPublicGoogleAPI := r.URL.Path == "/api/auth/start" ||
				r.URL.Path == "/api/auth/submit" ||
				r.URL.Path == "/api/auth/status" ||
				r.URL.Path == "/api/auth/pool" ||
				r.URL.Path == "/api/auth/pool/switch" ||
				r.URL.Path == "/api/auth/pool/delete" ||
				r.URL.Path == "/api/auth/google/clear" ||
				r.URL.Path == "/api/openai/settings" ||
				r.URL.Path == "/api/openai/models" ||
				r.URL.Path == "/api/auth/pwd/update" ||
				r.URL.Path == "/api/github/releases" ||
				r.URL.Path == "/api/update" ||
				r.URL.Path == "/api/browser/proxy"
			isGoogleLoginPage := r.URL.Path == "/login"
			isOpenAIConfigPage := r.URL.Path == "/"

			isGoogleAuthPassed := h.authSvc.CheckOAuthTokenExists() || os.Getenv("OPENAI_API_KEY") != "" || (auth.HomeDirOverride == "" && auth.FindAgyPath() != "")

			if !isGoogleAuthPassed {
				if strings.HasPrefix(r.URL.Path, "/api/") && !isPublicGoogleAPI && r.URL.Path != "/api/auth/logout" {
					h.enableCORS(w)
					http.Error(w, "Unauthorized", http.StatusUnauthorized)
					return
				}
				if !isPublicGoogleAPI && !isGoogleLoginPage && !isOpenAIConfigPage {
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

// HandlePasswordUpdate updates the security login password
func (h *Handler) HandlePasswordUpdate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	newPwd := r.FormValue("new_password")
	if newPwd == "" {
		var req struct {
			NewPassword string `json:"new_password"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err == nil {
			newPwd = req.NewPassword
		}
	}

	err := h.authSvc.SaveNewPassword(newPwd)
	if err != nil {
		http.Error(w, "Gagal nyimpen sandi anyar: "+err.Error(), http.StatusInternalServerError)
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
	_, _ = w.Write([]byte("Sandi keamanan kasil dianyari"))
}

// HandleAuthStatus gets Google OAuth authentication status
func (h *Handler) HandleAuthStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"authenticated": h.authSvc.CheckOAuthTokenExists(),
		"email":         auth.MaskEmail(h.authSvc.GetAuthenticatedEmail()),
		"project":       h.authSvc.GetGCPProject(),
		"version":       AppVersion,
	})
}

// HandleQuotaSummary retrieves user quota summary details
func (h *Handler) HandleQuotaSummary(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	quota, err := h.authSvc.GetQuotaSummary()
	if err != nil {
		log.Printf("[QUOTA WARN] Detail quota resmi agy ora tersedia: %v\n", err)
		_ = json.NewEncoder(w).Encode(auth.QuotaSummaryResponse{
			Groups:    []auth.QuotaGroup{},
			Exhausted: false,
			Error:     "Detail quota mung kasedhiya kanggo login Google Antigravity (`agy`). Yen sampeyan nganggo OpenAI-compatible provider, cek pemakaian/quota saka dashboard provider masing-masing.",
		})
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

	// Automatically sync new account to the pool
	_ = h.authSvc.SyncCurrentAccountToPool()

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

// HandleSearchWorkspace searches active workspace for filename or text content matches
func (h *Handler) HandleSearchWorkspace(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query().Get("q")
	results, err := h.workspaceSvc.SearchWorkspace(query)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(results)
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

	if req.Conversation == "" {
		req.Conversation = fmt.Sprintf("temp-%d", time.Now().UnixNano())
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

	if cmd != nil {
		go func() {
			_ = cmd.Wait()
			_ = stdoutPipe.Close()
		}()
	}

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

// HandleOpenAISettings reads or saves OpenAI-compatible provider settings.
func (h *Handler) HandleOpenAISettings(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	switch r.Method {
	case http.MethodGet:
		_ = json.NewEncoder(w).Encode(h.terminalSvc.GetOpenAISettings(false))
	case http.MethodPost:
		var req struct {
			APIKey      string `json:"apiKey"`
			APIBase     string `json:"apiBase"`
			Models      string `json:"models"`
			ClearAPIKey bool   `json:"clearApiKey"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		if err := h.terminalSvc.SaveOpenAISettings(req.APIKey, req.APIBase, req.Models, req.ClearAPIKey); err != nil {
			http.Error(w, "Gagal nyimpen setelan OpenAI: "+err.Error(), http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(h.terminalSvc.GetOpenAISettings(false))
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// HandleOpenAIModels fetches models available for the configured or provided key.
func (h *Handler) HandleOpenAIModels(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if r.Method != http.MethodPost && r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	apiKey := r.URL.Query().Get("apiKey")
	apiBase := r.URL.Query().Get("apiBase")
	if r.Method == http.MethodPost {
		var req struct {
			APIKey  string `json:"apiKey"`
			APIBase string `json:"apiBase"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err == nil {
			apiKey = req.APIKey
			apiBase = req.APIBase
		}
	}

	models, err := h.terminalSvc.FetchOpenAIModels(apiKey, apiBase)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
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

func writeMergedEnv(path string, updates map[string]string) error {
	existing := map[string]string{}
	order := []string{}

	if data, err := os.ReadFile(path); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			trimmed := strings.TrimSpace(line)
			if trimmed == "" || strings.HasPrefix(trimmed, "#") || !strings.Contains(line, "=") {
				continue
			}
			parts := strings.SplitN(line, "=", 2)
			key := strings.TrimSpace(parts[0])
			if key == "" {
				continue
			}
			if _, ok := existing[key]; !ok {
				order = append(order, key)
			}
			existing[key] = parts[1]
		}
	} else if !os.IsNotExist(err) {
		return err
	}

	for key, val := range updates {
		if _, ok := existing[key]; !ok {
			order = append(order, key)
		}
		existing[key] = strings.ReplaceAll(val, "\n", "")
	}

	var b strings.Builder
	for _, key := range order {
		b.WriteString(key)
		b.WriteString("=")
		b.WriteString(existing[key])
		b.WriteString("\n")
	}
	return os.WriteFile(path, []byte(b.String()), 0600)
}

// HandleSelfUpdate triggers a background update process
func (h *Handler) HandleSelfUpdate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	version := r.FormValue("version")
	if version == "" && strings.Contains(r.Header.Get("Content-Type"), "application/json") {
		var req struct {
			Version string `json:"version"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err == nil {
			version = strings.TrimSpace(req.Version)
		}
	}
	if version == "" {
		version = "latest"
	}
	if !regexp.MustCompile(`^[A-Za-z0-9._-]+$`).MatchString(version) {
		http.Error(w, "invalid version", http.StatusBadRequest)
		return
	}

	log.Printf("[UPDATE] Memulai pembaruan server otomatis menyang versi: %s...", version)

	// Extract active port, password and dbus address to preserve them during update
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	password := h.authSvc.GetPassword()
	dbusAddr := os.Getenv("DBUS_SESSION_BUS_ADDRESS")

	startDir := h.workspaceSvc.ServerStartDir()

	// Preserve all existing .env values, including OpenAI-compatible settings.
	envUpdates := map[string]string{
		"PORT":     port,
		"PASSWORD": password,
	}
	if dbusAddr != "" {
		envUpdates["DBUS_SESSION_BUS_ADDRESS"] = dbusAddr
	}
	_ = writeMergedEnv(filepath.Join(startDir, ".env"), envUpdates)

	mobileIdeDir := filepath.Join(startDir, "mobile-ide")
	_ = os.MkdirAll(mobileIdeDir, 0755)
	_ = writeMergedEnv(filepath.Join(mobileIdeDir, ".env"), envUpdates)

	go func() {
		// Tunggu 1 detik agar respon HTTP 200 OK sampai ke klien (HP) sebelum server mati/di-update
		time.Sleep(1 * time.Second)

		scriptPath := filepath.Join(startDir, "update.sh")
		var cmd *exec.Cmd
		if _, err := os.Stat(scriptPath); err == nil {
			cmd = exec.Command("bash", "-c", "nohup ./update.sh \"$UPDATE_VERSION\" > update.log 2>&1 &")
		} else {
			cmd = exec.Command("bash", "-c", "nohup bash -c 'set -e; installer_tmp=\"${TMPDIR:-/tmp}/mobile-agy-install.sh\"; curl -H \"Cache-Control: no-cache\" -fsSL https://raw.githubusercontent.com/sodikinnaa/go-agy-ide/main/install.sh -o \"$installer_tmp\"; env VERSION=\"$UPDATE_VERSION\" bash \"$installer_tmp\"' > update.log 2>&1 &")
		}
		cmd.Dir = startDir
		cmd.Env = append(os.Environ(), "UPDATE_VERSION="+version)
		_ = cmd.Run()
	}()

	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("Pembaruan dimulai menyang versi " + version + ". Server bakal di-restart otomatis."))
}

type SwitchAccountRequest struct {
	Email string `json:"email"`
}

// HandleGetAccountsPool returns the list of all pooled accounts and the active one
func (h *Handler) HandleGetAccountsPool(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	// Automatically sync current account to pool if logged in
	_ = h.authSvc.SyncCurrentAccountToPool()

	pool, err := h.authSvc.LoadAccountsPool()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	emails := make([]string, len(pool))
	for i, entry := range pool {
		emails[i] = entry.Email
	}

	activeEmail := h.authSvc.GetAuthenticatedEmail()

	_ = json.NewEncoder(w).Encode(map[string]any{
		"accounts": emails,
		"active":   activeEmail,
	})
}

// HandleSwitchAccount switches the active Google account
func (h *Handler) HandleSwitchAccount(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req SwitchAccountRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if err := h.authSvc.SwitchAccount(req.Email); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("Sukses ganti akun"))
}

// HandleDeleteAccount removes an account from the pool
func (h *Handler) HandleDeleteAccount(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req SwitchAccountRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if err := h.authSvc.DeleteAccount(req.Email); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("Sukses mbusak akun"))
}

// HandleClearGoogleAuth clears Google OAuth authentication only, leaving IDE session active
func (h *Handler) HandleClearGoogleAuth(w http.ResponseWriter, r *http.Request) {
	h.authSvc.Logout()
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("Sukses ngresiki Google auth"))
}

// HandleGithubReleases proxy fetches releases from GitHub API
func (h *Handler) HandleGithubReleases(w http.ResponseWriter, r *http.Request) {
	req, err := http.NewRequest("GET", "https://api.github.com/repos/sodikinnaa/go-agy-ide/releases?per_page=30", nil)
	if err != nil {
		http.Error(w, fmt.Sprintf("Gagal nggawe request: %v", err), http.StatusInternalServerError)
		return
	}
	req.Header.Set("User-Agent", "go-agy-ide")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		http.Error(w, fmt.Sprintf("Gagal nyambung menyang GitHub API: %v", err), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// HandleManifest serves the PWA manifest.json
func (h *Handler) HandleManifest(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	content, err := os.ReadFile(filepath.Join(h.workspaceSvc.ServerStartDir(), "manifest.json"))
	if err == nil {
		_, _ = w.Write(content)
		return
	}
	_, _ = w.Write([]byte(h.html.ManifestJSON))
}

// HandleServiceWorker serves the PWA service worker sw.js
func (h *Handler) HandleServiceWorker(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/javascript")
	content, err := os.ReadFile(filepath.Join(h.workspaceSvc.ServerStartDir(), "sw.js"))
	if err == nil {
		_, _ = w.Write(content)
		return
	}
	_, _ = w.Write([]byte(h.html.ServiceWorkerJS))
}

// HandleIcon192 serves the 192x192 PWA icon
func (h *Handler) HandleIcon192(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "image/png")
	content, err := os.ReadFile(filepath.Join(h.workspaceSvc.ServerStartDir(), "icon-192.png"))
	if err == nil {
		_, _ = w.Write(content)
		return
	}
	_, _ = w.Write(h.html.Icon192)
}

// HandleIcon512 serves the 512x512 PWA icon
func (h *Handler) HandleIcon512(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "image/png")
	content, err := os.ReadFile(filepath.Join(h.workspaceSvc.ServerStartDir(), "icon-512.png"))
	if err == nil {
		_, _ = w.Write(content)
		return
	}
	_, _ = w.Write(h.html.Icon512)
}

// HandleTerminalStream handles live streaming of the persistent terminal session
func (h *Handler) HandleTerminalStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	// Ensure the session is running
	err := h.terminalSvc.StartSession(h.workspaceSvc.ActiveWorkspaceDir())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	ch := make(chan []byte, 100)
	h.terminalSvc.RegisterClient(ch)
	defer h.terminalSvc.UnregisterClient(ch)

	// Flush initial headers
	flusher.Flush()

	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case data := <-ch:
			_, _ = w.Write(data)
			flusher.Flush()
		case <-ticker.C:
			// keep alive ping
			_, _ = w.Write([]byte(""))
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

// HandleTerminalInput writes raw input into the persistent terminal session
func (h *Handler) HandleTerminalInput(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Data string `json:"data"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	err := h.terminalSvc.WriteInput(req.Data)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

// HandlePorts handles requests to fetch active listening TCP ports
func (h *Handler) HandlePorts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ports, err := terminal.GetActivePorts()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(ports)
}

// HandleBrowserProxy proxies external & local applications for the embedded Live Browser engine,
// bypassing restrictive X-Frame-Options & CSP headers, while preserving CSRF tokens, cookies, and HTTP methods.
func (h *Handler) HandleBrowserProxy(w http.ResponseWriter, r *http.Request) {
	targetURL := r.URL.Query().Get("url")
	if targetURL == "" {
		http.Error(w, "URL parameter missing", http.StatusBadRequest)
		return
	}

	if !strings.HasPrefix(targetURL, "http://") && !strings.HasPrefix(targetURL, "https://") {
		targetURL = "http://" + targetURL
	}

	// Read request body into memory so it can be re-sent safely
	var reqBody []byte
	if r.Body != nil {
		reqBody, _ = io.ReadAll(r.Body)
		r.Body.Close()
	}

	// Create request with exact HTTP method (GET, POST, PUT, DELETE, etc.) and buffered body
	req, err := http.NewRequest(r.Method, targetURL, bytes.NewReader(reqBody))
	if err != nil {
		http.Error(w, "Invalid target URL: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Copy incoming request headers (Cookies, CSRF tokens, Content-Type, Authorization, etc.)
	for k, vv := range r.Header {
		lowerKey := strings.ToLower(k)
		if lowerKey == "host" || lowerKey == "accept-encoding" {
			continue
		}
		for _, v := range vv {
			req.Header.Add(k, v)
		}
	}

	// Set Origin & Referer headers to match target host to pass CSRF validation
	parsedTarget, parseErr := url.Parse(targetURL)
	if parseErr == nil && parsedTarget.Host != "" {
		req.Header.Set("Host", parsedTarget.Host)
		req.Header.Set("Origin", parsedTarget.Scheme+"://"+parsedTarget.Host)
		req.Header.Set("Referer", targetURL)
	}

	client := &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return http.ErrUseLastResponse
			}
			return nil
		},
	}

	resp, err := client.Do(req)
	if err != nil {
		http.Error(w, "Proxy request failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	// Process response headers
	for k, vv := range resp.Header {
		lowerKey := strings.ToLower(k)
		if lowerKey == "x-frame-options" || lowerKey == "content-security-policy" || lowerKey == "content-security-policy-report-only" {
			continue
		}
		if lowerKey == "set-cookie" {
			for _, v := range vv {
				cookieVal := v
				// Remove SameSite restrictions for iframe cookie persistence
				cookieVal = regexp.MustCompile(`(?i);\s*SameSite=[^;]+`).ReplaceAllString(cookieVal, "")
				// Remove Domain restriction
				cookieVal = regexp.MustCompile(`(?i);\s*Domain=[^;]+`).ReplaceAllString(cookieVal, "")
				// Remove Secure flag on HTTP so browsers accept cookies on localhost/HTTP origins
				if r.TLS == nil {
					cookieVal = regexp.MustCompile(`(?i);\s*Secure`).ReplaceAllString(cookieVal, "")
				}
				w.Header().Add("Set-Cookie", cookieVal)
			}
			continue
		}
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}

	contentType := resp.Header.Get("Content-Type")
	if strings.Contains(contentType, "text/html") {
		bodyBytes, err := io.ReadAll(resp.Body)
		if err == nil {
			htmlStr := string(bodyBytes)
			baseURL := parsedTarget.Scheme + "://" + parsedTarget.Host

			proxyScript := `<script>
(function() {
    function resolveProxyUrl(urlStr) {
        if (!urlStr || typeof urlStr !== 'string') return urlStr;
        if (urlStr.startsWith('/api/browser/proxy') || urlStr.startsWith('data:') || urlStr.startsWith('blob:') || urlStr.startsWith('javascript:')) return urlStr;
        try {
            var absoluteUrl = new URL(urlStr, document.baseURI).href;
            return '/api/browser/proxy?url=' + encodeURIComponent(absoluteUrl);
        } catch(e) {
            return urlStr;
        }
    }

    // 1. Intercept Form Submissions
    document.addEventListener('submit', function(e) {
        var form = e.target;
        if (form) {
            var rawAction = form.getAttribute('action') || form.action || window.location.href;
            form.action = resolveProxyUrl(rawAction);
        }
    }, true);

    // 2. Intercept Fetch API
    var origFetch = window.fetch;
    if (origFetch) {
        window.fetch = function(resource, init) {
            if (typeof resource === 'string') {
                resource = resolveProxyUrl(resource);
            } else if (resource && resource.url) {
                try {
                    resource = new Request(resolveProxyUrl(resource.url), resource);
                } catch(e) {}
            }
            return origFetch.call(this, resource, init);
        };
    }

    // 3. Intercept XMLHttpRequest
    var origOpen = XMLHttpRequest.prototype.open;
    if (origOpen) {
        XMLHttpRequest.prototype.open = function(method, url, async, user, pass) {
            if (url && typeof url === 'string') {
                url = resolveProxyUrl(url);
            }
            return origOpen.call(this, method, url, async, user, pass);
        };
    }
})();
</script>`

			// Inject base tag and form submission interceptor script
			if strings.Contains(htmlStr, "<head>") {
				injection := "<head><base href=\"" + baseURL + "/\">" + proxyScript
				htmlStr = strings.Replace(htmlStr, "<head>", injection, 1)
			}

			w.WriteHeader(resp.StatusCode)
			_, _ = w.Write([]byte(htmlStr))
			return
		}
	}

	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

