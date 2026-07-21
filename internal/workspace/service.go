package workspace

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

type Service struct {
	mu                 sync.RWMutex
	serverStartDir     string
	activeWorkspaceDir string
	workspacesList     []string
}

func NewService(serverStartDir string) *Service {
	s := &Service{
		serverStartDir:     serverStartDir,
		activeWorkspaceDir: serverStartDir,
		workspacesList:     []string{serverStartDir},
	}
	s.Load()
	return s
}

// ActiveWorkspaceDir returns the currently active workspace directory path.
func (s *Service) ActiveWorkspaceDir() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.activeWorkspaceDir
}

// ServerStartDir returns the directory the server was started from.
func (s *Service) ServerStartDir() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.serverStartDir
}

// WorkspacesList returns the list of known workspaces.
func (s *Service) WorkspacesList() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	// Return a copy to avoid data race
	list := make([]string, len(s.workspacesList))
	copy(list, s.workspacesList)
	return list
}

// Load workspaces from workspaces.json
func (s *Service) Load() {
	s.mu.Lock()
	defer s.mu.Unlock()

	configPath := filepath.Join(s.serverStartDir, "workspaces.json")
	file, err := os.ReadFile(configPath)
	if err != nil {
		s.activeWorkspaceDir = s.serverStartDir
		s.workspacesList = []string{s.serverStartDir}
		s.saveUnlocked()
		return
	}

	var ws WorkspaceSettings
	if err := json.Unmarshal(file, &ws); err != nil {
		s.activeWorkspaceDir = s.serverStartDir
		s.workspacesList = []string{s.serverStartDir}
		return
	}

	s.activeWorkspaceDir = ws.Active
	s.workspacesList = ws.List

	if _, err := os.Stat(s.activeWorkspaceDir); os.IsNotExist(err) {
		s.activeWorkspaceDir = s.serverStartDir
	}
}

// Save workspaces list to workspaces.json (expects lock to be held or not)
func (s *Service) Save() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.saveUnlocked()
}

func (s *Service) saveUnlocked() {
	configPath := filepath.Join(s.serverStartDir, "workspaces.json")
	ws := WorkspaceSettings{
		Active: s.activeWorkspaceDir,
		List:   s.workspacesList,
	}
	data, _ := json.MarshalIndent(ws, "", "  ")
	_ = os.WriteFile(configPath, data, 0644)
}

// Select active workspace
func (s *Service) Select(path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	absPath, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("invalid path: %w", err)
	}

	info, err := os.Stat(absPath)
	if os.IsNotExist(err) || !info.IsDir() {
		return fmt.Errorf("directory not found or is not a folder")
	}

	s.activeWorkspaceDir = absPath
	s.saveUnlocked()
	return nil
}

// Add workspace and select it
func (s *Service) Add(path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	absPath, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("invalid path: %w", err)
	}

	err = os.MkdirAll(absPath, 0755)
	if err != nil {
		return fmt.Errorf("failed to create folder: %w", err)
	}

	exists := false
	for _, item := range s.workspacesList {
		if item == absPath {
			exists = true
			break
		}
	}
	if !exists {
		s.workspacesList = append(s.workspacesList, absPath)
	}

	s.activeWorkspaceDir = absPath
	s.saveUnlocked()
	return nil
}

// ListFiles recursively walks the active workspace and returns list of files
func (s *Service) ListFiles() ([]FileInfo, error) {
	s.mu.RLock()
	activeWS := s.activeWorkspaceDir
	s.mu.RUnlock()

	files := []FileInfo{}
	err := filepath.Walk(activeWS, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}

		rel, err := filepath.Rel(activeWS, path)
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
			if p == "node_modules" || 
			   p == "vendor" || 
			   p == "dist" || 
			   p == "build" || 
			   p == "venv" || 
			   p == ".venv" || 
			   p == "env" || 
			   p == "__pycache__" || 
			   p == ".next" || 
			   p == ".nuxt" || 
			   p == "tmp" || 
			   p == "temp" {
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

	return files, err
}

// ResolvePath checks for path traversal and returns the absolute path
func (s *Service) ResolvePath(pathParam string) (string, error) {
	s.mu.RLock()
	activeWS := s.activeWorkspaceDir
	s.mu.RUnlock()

	absPath := filepath.Join(activeWS, pathParam)
	if !strings.HasPrefix(absPath, activeWS) {
		return "", fmt.Errorf("Access Denied: Path traversal detected")
	}
	return absPath, nil
}

// ReadFile reads content from a workspace file
func (s *Service) ReadFile(pathParam string) ([]byte, error) {
	absPath, err := s.ResolvePath(pathParam)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(absPath)
}

// WriteFile writes content to a workspace file
func (s *Service) WriteFile(pathParam string, content []byte) error {
	absPath, err := s.ResolvePath(pathParam)
	if err != nil {
		return err
	}
	return os.WriteFile(absPath, content, 0644)
}

// DeleteFile deletes a file/folder in workspace
func (s *Service) DeleteFile(pathParam string) error {
	absPath, err := s.ResolvePath(pathParam)
	if err != nil {
		return err
	}
	return os.RemoveAll(absPath)
}

// CreateFileOrFolder creates a file or a folder inside the workspace
func (s *Service) CreateFileOrFolder(pathParam string, isDir bool) error {
	absPath, err := s.ResolvePath(pathParam)
	if err != nil {
		return err
	}

	if isDir {
		return os.MkdirAll(absPath, 0755)
	}

	parent := filepath.Dir(absPath)
	err = os.MkdirAll(parent, 0755)
	if err != nil {
		return err
	}

	f, err := os.Create(absPath)
	if err != nil {
		return err
	}
	return f.Close()
}

type SearchMatch struct {
	LineNumber int    `json:"lineNumber"`
	LineText   string `json:"lineText"`
}

type FileSearchResult struct {
	Path    string        `json:"path"`
	Name    string        `json:"name"`
	Matches []SearchMatch `json:"matches"`
}

func isBinaryExtension(ext string) bool {
	switch strings.ToLower(ext) {
	case ".png", ".jpg", ".jpeg", ".gif", ".ico", ".svg", ".webp",
		".exe", ".bin", ".zip", ".tar", ".gz", ".7z", ".rar",
		".pdf", ".mp4", ".mp3", ".wav", ".avi", ".mov",
		".woff", ".woff2", ".ttf", ".eot",
		".so", ".dylib", ".dll", ".a", ".o", ".pyc", ".db", ".sqlite":
		return true
	default:
		return false
	}
}

// SearchWorkspace searches for files matching filename or content query
func (s *Service) SearchWorkspace(query string) ([]FileSearchResult, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return []FileSearchResult{}, nil
	}

	queryLower := strings.ToLower(query)

	files, err := s.ListFiles()
	if err != nil {
		return nil, err
	}

	results := []FileSearchResult{}
	totalMatches := 0
	const maxTotalMatches = 200
	const maxMatchesPerFile = 20

	for _, file := range files {
		if file.IsDir {
			continue
		}

		if totalMatches >= maxTotalMatches {
			break
		}

		pathLower := strings.ToLower(file.Path)
		nameLower := strings.ToLower(file.Name)

		nameMatches := strings.Contains(pathLower, queryLower) || strings.Contains(nameLower, queryLower)

		var matches []SearchMatch

		if !isBinaryExtension(filepath.Ext(file.Name)) && file.Size < 2*1024*1024 {
			contentBytes, err := s.ReadFile(file.Path)
			if err == nil {
				isBinary := false
				checkLen := 512
				if len(contentBytes) < checkLen {
					checkLen = len(contentBytes)
				}
				for i := 0; i < checkLen; i++ {
					if contentBytes[i] == 0 {
						isBinary = true
						break
					}
				}

				if !isBinary {
					lines := strings.Split(string(contentBytes), "\n")
					for lineIdx, line := range lines {
						if strings.Contains(strings.ToLower(line), queryLower) {
							lineTrimmed := line
							if len(lineTrimmed) > 200 {
								lineTrimmed = lineTrimmed[:200] + "..."
							}
							matches = append(matches, SearchMatch{
								LineNumber: lineIdx + 1,
								LineText:   strings.TrimSpace(lineTrimmed),
							})
							totalMatches++
							if len(matches) >= maxMatchesPerFile || totalMatches >= maxTotalMatches {
								break
							}
						}
					}
				}
			}
		}

		if nameMatches || len(matches) > 0 {
			results = append(results, FileSearchResult{
				Path:    file.Path,
				Name:    file.Name,
				Matches: matches,
			})
		}
	}

	return results, nil
}

