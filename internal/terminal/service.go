package terminal

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mobile-agy/internal/auth"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

type Service struct{}

type OpenAISettings struct {
	APIBase          string   `json:"apiBase"`
	APIKey           string   `json:"apiKey,omitempty"`
	APIKeySet        bool     `json:"apiKeySet"`
	APIKeyMasked     string   `json:"apiKeyMasked,omitempty"`
	ConfiguredModels string   `json:"configuredModels"`
	AvailableModels  []string `json:"availableModels,omitempty"`
}

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

func defaultOpenAIBase() string {
	if apiBase := os.Getenv("OPENAI_API_BASE"); apiBase != "" {
		return apiBase
	}
	return "https://api.openai.com/v1"
}

func maskAPIKey(key string) string {
	if key == "" {
		return ""
	}
	if len(key) <= 8 {
		return "********"
	}
	return key[:4] + strings.Repeat("*", len(key)-8) + key[len(key)-4:]
}

func parseConfiguredOpenAIModels(modelsEnv string) []string {
	var models []string
	for _, p := range strings.Split(modelsEnv, ",") {
		trimmed := strings.TrimSpace(p)
		if trimmed != "" {
			models = append(models, "openai/"+trimmed)
		}
	}
	return models
}

func (s *Service) GetOpenAISettings(fetchModels bool) OpenAISettings {
	settings := OpenAISettings{
		APIBase:          defaultOpenAIBase(),
		APIKeySet:        os.Getenv("OPENAI_API_KEY") != "",
		APIKeyMasked:     maskAPIKey(os.Getenv("OPENAI_API_KEY")),
		ConfiguredModels: os.Getenv("OPENAI_MODELS"),
	}
	if fetchModels && settings.APIKeySet {
		models, err := s.FetchOpenAIModels("", "")
		if err == nil {
			settings.AvailableModels = models
		}
	}
	return settings
}

func (s *Service) SaveOpenAISettings(apiKey, apiBase, models string, clearAPIKey bool) error {
	apiKey = strings.TrimSpace(apiKey)
	apiBase = strings.TrimSpace(apiBase)
	models = strings.TrimSpace(models)

	if apiBase == "" {
		apiBase = "https://api.openai.com/v1"
	}

	updates := map[string]*string{
		"OPENAI_API_BASE": &apiBase,
		"OPENAI_MODELS":   &models,
	}

	if clearAPIKey {
		os.Unsetenv("OPENAI_API_KEY")
		updates["OPENAI_API_KEY"] = nil
	} else if apiKey != "" {
		os.Setenv("OPENAI_API_KEY", apiKey)
		updates["OPENAI_API_KEY"] = &apiKey
	}

	os.Setenv("OPENAI_API_BASE", apiBase)
	if models == "" {
		os.Unsetenv("OPENAI_MODELS")
	} else {
		os.Setenv("OPENAI_MODELS", models)
	}

	return updateEnvFile(".env", updates)
}

func updateEnvFile(path string, updates map[string]*string) error {
	seen := map[string]bool{}
	var lines []string

	file, err := os.Open(path)
	if err == nil {
		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			line := scanner.Text()
			trimmed := strings.TrimSpace(line)
			if trimmed == "" || strings.HasPrefix(trimmed, "#") || !strings.Contains(line, "=") {
				lines = append(lines, line)
				continue
			}
			key := strings.TrimSpace(strings.SplitN(line, "=", 2)[0])
			if val, ok := updates[key]; ok {
				seen[key] = true
				if val == nil {
					continue
				}
				lines = append(lines, key+"="+strconv.Quote(*val))
				continue
			}
			lines = append(lines, line)
		}
		if scanErr := scanner.Err(); scanErr != nil {
			_ = file.Close()
			return scanErr
		}
		_ = file.Close()
	} else if !os.IsNotExist(err) {
		return err
	}

	for key, val := range updates {
		if seen[key] || val == nil {
			continue
		}
		lines = append(lines, key+"="+strconv.Quote(*val))
	}

	content := strings.Join(lines, "\n")
	if content != "" {
		content += "\n"
	}
	return os.WriteFile(path, []byte(content), 0600)
}

func (s *Service) FetchOpenAIModels(apiKey, apiBase string) ([]string, error) {
	apiKey = strings.TrimSpace(apiKey)
	apiBase = strings.TrimSpace(apiBase)
	if apiKey == "" {
		apiKey = os.Getenv("OPENAI_API_KEY")
	}
	if apiBase == "" {
		apiBase = defaultOpenAIBase()
	}
	if apiKey == "" {
		return nil, fmt.Errorf("OPENAI_API_KEY is not configured")
	}

	url := strings.TrimSuffix(apiBase, "/") + "/models"
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("OpenAI models endpoint returned status %d: %s", resp.StatusCode, string(body))
	}

	var parsed struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, err
	}

	var models []string
	for _, item := range parsed.Data {
		id := strings.TrimSpace(item.ID)
		if id != "" {
			models = append(models, "openai/"+id)
		}
	}
	return models, nil
}

// GetModelsList fetches available models from agy CLI or falls back to defaults
func (s *Service) GetModelsList() ([]string, error) {
	agyPath := auth.FindAgyPath()
	var outputBytes []byte
	var err error

	useDirect := false
	if _, lookErr := exec.LookPath("script"); lookErr != nil {
		useDirect = true
	}

	if useDirect {
		cmdDirect := exec.Command(agyPath, "models")
		cmdDirect.Env = os.Environ()
		outputBytes, err = cmdDirect.Output()
	} else {
		cmdStr := fmt.Sprintf("%s models", agyPath)
		cmd := exec.Command("script", "-q", "-f", "-c", cmdStr, "/dev/null")
		cmd.Env = os.Environ()
		outputBytes, err = cmd.Output()

		if err != nil {
			cmdDirect := exec.Command(agyPath, "models")
			cmdDirect.Env = os.Environ()
			outputBytes, err = cmdDirect.Output()
		}
	}

	var models []string

	if err == nil {
		lines := strings.Split(string(outputBytes), "\n")
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

	// Append OpenAI-compatible models if configured. Prefer the live /models
	// endpoint so the dropdown reflects models available for the configured key.
	if os.Getenv("OPENAI_API_KEY") != "" {
		openAIModels, fetchErr := s.FetchOpenAIModels("", "")
		if fetchErr == nil && len(openAIModels) > 0 {
			models = append(models, openAIModels...)
		} else if configured := parseConfiguredOpenAIModels(os.Getenv("OPENAI_MODELS")); len(configured) > 0 {
			models = append(models, configured...)
		} else {
			models = append(models,
				"openai/gpt-4o",
				"openai/gpt-4o-mini",
				"openai/deepseek-chat",
				"openai/deepseek-reasoner",
			)
		}
	}

	return models, nil
}
