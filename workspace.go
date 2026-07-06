package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

type FileInfo struct {
	Path  string `json:"path"`
	Name  string `json:"name"`
	IsDir bool   `json:"isDir"`
	Size  int64  `json:"size"`
}

type WorkspaceSettings struct {
	Active string   `json:"active"`
	List   []string `json:"list"`
}

var serverStartDir string
var activeWorkspaceDir string
var workspacesList []string

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
	files := []FileInfo{}
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

// API GET /preview/* - Ngawula file statis saka workspace aktif kanggo preview
func handlePreviewFile(w http.ResponseWriter, r *http.Request) {
	relPath := strings.TrimPrefix(r.URL.Path, "/preview/")
	if relPath == "" {
		http.Error(w, "missing file path", http.StatusBadRequest)
		return
	}

	absPath := filepath.Join(activeWorkspaceDir, relPath)
	if !strings.HasPrefix(absPath, activeWorkspaceDir) {
		http.Error(w, "Access Denied: Path traversal detected", http.StatusForbidden)
		return
	}

	http.ServeFile(w, r, absPath)
}
