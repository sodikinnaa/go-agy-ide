package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
)

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
	http.HandleFunc("/api/chat/history", authMiddleware(handleChatHistoryList))
	http.HandleFunc("/api/chat/history/detail", authMiddleware(handleChatHistoryDetail))
	http.HandleFunc("/api/chat/stop", authMiddleware(handleChatStop))
	http.HandleFunc("/api/chat/delete", authMiddleware(handleChatDelete))
	http.HandleFunc("/api/run", authMiddleware(handleRunCommandStream))
	http.HandleFunc("/api/workspaces", authMiddleware(handleWorkspacesGet))
	http.HandleFunc("/api/workspaces/select", authMiddleware(handleWorkspaceSelect))
	http.HandleFunc("/api/workspaces/add", authMiddleware(handleWorkspaceAdd))
	http.HandleFunc("/api/models", authMiddleware(handleModelsList))
	http.HandleFunc("/preview/", authMiddleware(handlePreviewFile))

	log.Printf("Mulai server Mobile IDE ing http://0.0.0.0:%s ...\n", port)
	log.Printf("Workspace root aktif: %s\n", activeWorkspaceDir)
	if err := http.ListenAndServe("0.0.0.0:"+port, nil); err != nil {
		log.Printf("Gagal nglakokake server: %v\n", err)
	}
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
