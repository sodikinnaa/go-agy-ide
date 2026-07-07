package terminal

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"mobile-agy/internal/auth"
)

type Service struct{}

func NewService() *Service {
	return &Service{}
}

// StartCommand executes a bash command and returns its stdout/stderr reader
func (s *Service) StartCommand(ctx context.Context, command string, activeWorkspaceDir string) (*exec.Cmd, io.ReadCloser, error) {
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
		return nil, nil, err
	}
	cmd.Stderr = cmd.Stdout

	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}

	return cmd, stdoutPipe, nil
}

// GetModelsList fetches available models from agy CLI or falls back to defaults
func (s *Service) GetModelsList() ([]string, error) {
	agyPath := auth.FindAgyPath()
	cmdStr := fmt.Sprintf("%s models", agyPath)
	cmd := exec.Command("script", "-q", "-f", "-c", cmdStr, "/dev/null")
	cmd.Env = os.Environ()

	outputBytes, err := cmd.Output()
	if err != nil {
		cmdDirect := exec.Command(auth.FindAgyPath(), "models")
		cmdDirect.Env = os.Environ()
		outputBytes, err = cmdDirect.Output()
	}

	if err != nil {
		return nil, err
	}

	lines := strings.Split(string(outputBytes), "\n")
	var models []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.Contains(trimmed, "Fetching") || strings.Contains(trimmed, "⠋") || strings.Contains(trimmed, "⠙") || strings.Contains(trimmed, "⠹") || strings.Contains(trimmed, "⠸") || strings.Contains(trimmed, "⠼") || strings.Contains(trimmed, "⠴") || strings.Contains(trimmed, "⠦") || strings.Contains(trimmed, "⠧") || strings.Contains(trimmed, "⠇") || strings.Contains(trimmed, "⠏") {
			continue
		}
		models = append(models, trimmed)
	}

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

	return models, nil
}
