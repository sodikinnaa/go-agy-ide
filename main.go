package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"mobile-agy/internal/auth"
	"mobile-agy/internal/chat"
	"mobile-agy/internal/handler"
	"mobile-agy/internal/terminal"
	"mobile-agy/internal/workspace"
)

func main() {
	serverStartDir, err := filepath.Abs(".")
	if err != nil {
		fmt.Printf("Gagal mendapatkan path direktori saat ini: %v\n", err)
		os.Exit(1)
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	// Initialize modular services
	workspaceSvc := workspace.NewService(serverStartDir)
	authSvc := auth.NewService(serverStartDir)
	chatSvc := chat.NewService()
	terminalSvc := terminal.NewService()

	// Initialize HTML pages embedding
	htmlPages := handler.EmbeddedHTML{
		IndexHTML:    embeddedIndexHTML,
		LoginHTML:    embeddedLoginHTML,
		LoginPwdHTML: embeddedLoginPwdHTML,
	}

	// Initialize HTTP handler
	h := handler.NewHandler(workspaceSvc, authSvc, chatSvc, terminalSvc, htmlPages)

	// Routes wrapped with AuthMiddleware
	http.HandleFunc("/", h.AuthMiddleware(h.HandleIndex))
	http.HandleFunc("/login", h.AuthMiddleware(h.HandleLoginPage))
	http.HandleFunc("/login-pwd", h.AuthMiddleware(h.HandleLoginPwdPage))

	// Authentication APIs
	http.HandleFunc("/api/auth/start", h.AuthMiddleware(h.HandleAuthStart))
	http.HandleFunc("/api/auth/submit", h.AuthMiddleware(h.HandleAuthSubmit))
	http.HandleFunc("/api/auth/logout", h.AuthMiddleware(h.HandleLogout))
	http.HandleFunc("/api/auth/google/clear", h.AuthMiddleware(h.HandleClearGoogleAuth))
	http.HandleFunc("/api/auth/status", h.AuthMiddleware(h.HandleAuthStatus))
	http.HandleFunc("/api/auth/pwd", h.AuthMiddleware(h.HandlePasswordAuth))
	http.HandleFunc("/api/auth/pool", h.AuthMiddleware(h.HandleGetAccountsPool))
	http.HandleFunc("/api/auth/pool/switch", h.AuthMiddleware(h.HandleSwitchAccount))
	http.HandleFunc("/api/auth/pool/delete", h.AuthMiddleware(h.HandleDeleteAccount))
	http.HandleFunc("/api/quota", h.AuthMiddleware(h.HandleQuotaSummary))

	// Workspace and project files APIs
	http.HandleFunc("/api/files", h.AuthMiddleware(h.HandleListFiles))
	http.HandleFunc("/api/file", h.AuthMiddleware(h.HandleFileOperations))
	http.HandleFunc("/api/file/create", h.AuthMiddleware(h.HandleCreateFileOrFolder))
	http.HandleFunc("/api/chat", h.AuthMiddleware(h.HandleChatStream))
	http.HandleFunc("/api/chat/history", h.AuthMiddleware(h.HandleChatHistoryList))
	http.HandleFunc("/api/chat/history/detail", h.AuthMiddleware(h.HandleChatHistoryDetail))
	http.HandleFunc("/api/chat/stop", h.AuthMiddleware(h.HandleChatStop))
	http.HandleFunc("/api/chat/delete", h.AuthMiddleware(h.HandleChatDelete))
	http.HandleFunc("/api/run", h.AuthMiddleware(h.HandleRunCommandStream))
	http.HandleFunc("/api/workspaces", h.AuthMiddleware(h.HandleWorkspacesGet))
	http.HandleFunc("/api/workspaces/select", h.AuthMiddleware(h.HandleWorkspaceSelect))
	http.HandleFunc("/api/workspaces/add", h.AuthMiddleware(h.HandleWorkspaceAdd))
	http.HandleFunc("/api/models", h.AuthMiddleware(h.HandleModelsList))
	http.HandleFunc("/preview/", h.AuthMiddleware(h.HandlePreviewFile))
	http.HandleFunc("/api/webhook", h.HandleGithubWebhook)
	http.HandleFunc("/api/update", h.AuthMiddleware(h.HandleSelfUpdate))

	log.Printf("Mulai server Mobile IDE ing http://0.0.0.0:%s ...\n", port)
	log.Printf("Workspace root aktif: %s\n", workspaceSvc.ActiveWorkspaceDir())
	if err := http.ListenAndServe("0.0.0.0:"+port, nil); err != nil {
		log.Printf("Gagal nglakokake server: %v\n", err)
	}
}
