package main

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"mobile-agy/internal/auth"
	"mobile-agy/internal/chat"
	"mobile-agy/internal/handler"
	"mobile-agy/internal/terminal"
	"mobile-agy/internal/workspace"
)

func init() {
	if transport, ok := http.DefaultTransport.(*http.Transport); ok {
		dialer := &net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}
		transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			origNetwork := network
			origAddr := addr
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
			if err == nil && len(ips) > 0 {
				var hasIPv6 bool
				var ipToUse net.IP
				for _, ip := range ips {
					if ip.To4() == nil {
						hasIPv6 = true
						ipToUse = ip
						break
					}
				}
				if !hasIPv6 {
					for _, ip := range ips {
						if ipv4 := ip.To4(); ipv4 != nil {
							// Synthesize NAT64: 64:ff9b::IPv4
							nat64IP := make(net.IP, 16)
							nat64IP[0] = 0x00
							nat64IP[1] = 0x64
							nat64IP[2] = 0xff
							nat64IP[3] = 0x9b
							copy(nat64IP[12:], ipv4)
							ipToUse = nat64IP
							network = "tcp6"
							break
						}
					}
				}
				if ipToUse != nil {
					addr = net.JoinHostPort(ipToUse.String(), port)
				}
			}
			conn, err := dialer.DialContext(ctx, network, addr)
			if err == nil {
				return conn, nil
			}
			return dialer.DialContext(ctx, origNetwork, origAddr)
		}
	}
}

func main() {
	loadEnv()

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
		IndexHTML:       embeddedIndexHTML,
		LoginHTML:       embeddedLoginHTML,
		LoginPwdHTML:    embeddedLoginPwdHTML,
		ManifestJSON:    embeddedManifestJSON,
		ServiceWorkerJS: embeddedServiceWorkerJS,
		Icon192:         embeddedIcon192,
		Icon512:         embeddedIcon512,
	}

	// Initialize HTTP handler
	h := handler.NewHandler(workspaceSvc, authSvc, chatSvc, terminalSvc, htmlPages)

	// Public PWA routes
	http.HandleFunc("/manifest.json", h.HandleManifest)
	http.HandleFunc("/sw.js", h.HandleServiceWorker)
	http.HandleFunc("/icon-192.png", h.HandleIcon192)
	http.HandleFunc("/icon-512.png", h.HandleIcon512)

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
	http.HandleFunc("/api/auth/pwd/update", h.AuthMiddleware(h.HandlePasswordUpdate))
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
	http.HandleFunc("/api/openai/settings", h.AuthMiddleware(h.HandleOpenAISettings))
	http.HandleFunc("/api/openai/models", h.AuthMiddleware(h.HandleOpenAIModels))
	http.HandleFunc("/preview/", h.AuthMiddleware(h.HandlePreviewFile))
	http.HandleFunc("/api/webhook", h.HandleGithubWebhook)
	http.HandleFunc("/api/update", h.AuthMiddleware(h.HandleSelfUpdate))
	http.HandleFunc("/api/github/releases", h.AuthMiddleware(h.HandleGithubReleases))

	log.Printf("Mulai server Mobile IDE ing http://0.0.0.0:%s ...\n", port)
	log.Printf("Workspace root aktif: %s\n", workspaceSvc.ActiveWorkspaceDir())
	if err := http.ListenAndServe("0.0.0.0:"+port, nil); err != nil {
		log.Printf("Gagal nglakokake server: %v\n", err)
	}
}

func loadEnv() {
	file, err := os.Open(".env")
	if err != nil {
		return
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "#") || !strings.Contains(line, "=") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			key := strings.TrimSpace(parts[0])
			val := strings.TrimSpace(parts[1])
			if (strings.HasPrefix(val, "\"") && strings.HasSuffix(val, "\"")) ||
				(strings.HasPrefix(val, "'") && strings.HasSuffix(val, "'")) {
				val = val[1 : len(val)-1]
			}
			if os.Getenv(key) == "" {
				os.Setenv(key, val)
			}
		}
	}
}
