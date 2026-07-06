package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

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

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		if _, err := exec.LookPath("bash"); err == nil {
			cmd = exec.Command("bash", "-c", command)
		} else if _, err := exec.LookPath("powershell"); err == nil {
			cmd = exec.Command("powershell", "-Command", command)
		} else {
			cmd = exec.Command("cmd", "/c", command)
		}
	} else {
		cmd = exec.Command("bash", "-c", command)
	}

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

	processDone := make(chan struct{})
	go func() {
		if cmd.Process != nil {
			cmd.Process.Wait()
		}
		close(processDone)
		// Close the pipe to force Read() to unblock if held open by background daemons
		stdoutPipe.Close()
	}()

	// Monitor client disconnection to clean up running processes
	go func() {
		select {
		case <-processDone:
			// Process exited normally
		case <-r.Context().Done():
			// Client disconnected, terminate process group/process to prevent leaks
			if cmd.Process != nil {
				cmd.Process.Kill()
			}
		}
	}()

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

	// Wait to clean up process resources
	cmd.Wait()
}

// API GET /api/models - Maca daftar model sing kasedhiya saka agy models
func handleModelsList(w http.ResponseWriter, r *http.Request) {
	agyPath := findAgyPath()
	cmdStr := fmt.Sprintf("%s models", agyPath)
	cmd := exec.Command("script", "-q", "-f", "-c", cmdStr, "/dev/null")
	cmd.Env = os.Environ()

	outputBytes, err := cmd.Output()
	if err != nil {
		// Fallback yen gagal nganggo script
		cmdDirect := exec.Command(findAgyPath(), "models")
		cmdDirect.Env = os.Environ()
		outputBytes, err = cmdDirect.Output()
	}

	if err != nil {
		http.Error(w, "Gagal ngakses daftar model: "+err.Error(), http.StatusInternalServerError)
		return
	}

	lines := strings.Split(string(outputBytes), "\n")
	var models []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		// Saring spinner utawa pesen inisialisasi "Fetching available models..."
		if strings.Contains(trimmed, "Fetching") || strings.Contains(trimmed, "⠋") || strings.Contains(trimmed, "⠙") || strings.Contains(trimmed, "⠹") || strings.Contains(trimmed, "⠸") || strings.Contains(trimmed, "⠼") || strings.Contains(trimmed, "⠴") || strings.Contains(trimmed, "⠦") || strings.Contains(trimmed, "⠧") || strings.Contains(trimmed, "⠇") || strings.Contains(trimmed, "⠏") {
			continue
		}
		models = append(models, trimmed)
	}

	// Fallback list yen asile kosong
	if len(models) == 0 {
		models = []string{
			"Gemini 3.5 Flash (Medium)",
			"Gemini 3.5 Flash (High)",
			"Gemini 3.5 Flash (Low)",
			"Gemini 3.1 Pro (Low)",
			"Gemini 3.1 Pro (High)",
			"Claude Sonnet 4.6 (Thinking)",
			"Claude Opus 4.6 (Thinking)",
			"GPT-OSS 120B (Medium)",
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(models)
}
