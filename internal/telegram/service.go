package telegram

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"mobile-agy/internal/chat"
	"mobile-agy/internal/workspace"
)

type Config struct {
	BotToken     string   `json:"botToken"`
	AllowedUsers []string `json:"allowedUsers"`
	Enabled      bool     `json:"enabled"`
}

type User struct {
	ID        int64  `json:"id"`
	FirstName string `json:"first_name"`
	Username  string `json:"username"`
}

type Chat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
}

type InlineKeyboardButton struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data"`
}

type InlineKeyboardMarkup struct {
	InlineKeyboard [][]InlineKeyboardButton `json:"inline_keyboard"`
}

type KeyboardButton struct {
	Text string `json:"text"`
}

type ReplyKeyboardMarkup struct {
	Keyboard              [][]KeyboardButton `json:"keyboard"`
	IsPersistent          bool               `json:"is_persistent"`
	ResizeKeyboard        bool               `json:"resize_keyboard"`
	InputFieldPlaceholder string             `json:"input_field_placeholder,omitempty"`
}

type Message struct {
	MessageID   int                   `json:"message_id"`
	From        *User                 `json:"from"`
	Chat        *Chat                 `json:"chat"`
	Text        string                `json:"text"`
	Date        int                   `json:"date"`
	ReplyMarkup *InlineKeyboardMarkup `json:"reply_markup,omitempty"`
}

type CallbackQuery struct {
	ID      string   `json:"id"`
	From    *User    `json:"from"`
	Message *Message `json:"message"`
	Data    string   `json:"data"`
}

type Update struct {
	UpdateID      int            `json:"update_id"`
	Message       *Message       `json:"message"`
	CallbackQuery *CallbackQuery `json:"callback_query"`
}

type BotCommand struct {
	Command     string `json:"command"`
	Description string `json:"description"`
}

type StreamToolCall struct {
	Name        string                 `json:"name"`
	ToolAction  string                 `json:"tool_action"`
	ToolSummary string                 `json:"tool_summary"`
	Args        map[string]interface{} `json:"args"`
}

type StreamData struct {
	Content   string           `json:"content"`
	Type      string           `json:"type"`
	ToolCalls []StreamToolCall `json:"tool_calls"`
}

type getUpdatesResponse struct {
	OK     bool     `json:"ok"`
	Result []Update `json:"result"`
}

type Service struct {
	mu           sync.RWMutex
	chatSvc      *chat.Service
	workspaceSvc *workspace.Service
	config       Config
	cancelFunc   context.CancelFunc
	userConvs    map[int64]string // Telegram ChatID -> ConversationID
	userModels   map[int64]string // Telegram ChatID -> Selected Model
	running      bool
	httpClient   *http.Client
	configPath   string
}

func NewService(chatSvc *chat.Service, workspaceSvc *workspace.Service) *Service {
	cfgPath := filepath.Join(workspaceSvc.ServerStartDir(), "telegram_config.json")
	svc := &Service{
		chatSvc:      chatSvc,
		workspaceSvc: workspaceSvc,
		userConvs:    make(map[int64]string),
		userModels:   make(map[int64]string),
		httpClient: &http.Client{
			Timeout: 40 * time.Second,
		},
		configPath: cfgPath,
	}

	svc.loadConfig()
	return svc
}

func (s *Service) loadConfig() {
	s.mu.Lock()
	defer s.mu.Unlock()

	// 1. Try reading from telegram_config.json
	data, err := os.ReadFile(s.configPath)
	if err == nil {
		var cfg Config
		if err := json.Unmarshal(data, &cfg); err == nil {
			s.config = cfg
			log.Printf("[TELEGRAM] Config dimuat saka %s", s.configPath)
			return
		}
	}

	// 2. Fallback to Environment Variables (.env)
	botToken := os.Getenv("TELEGRAM_BOT_TOKEN")
	allowedStr := os.Getenv("TELEGRAM_ALLOWED_USERS")
	enabled := botToken != ""

	var allowed []string
	if allowedStr != "" {
		parts := strings.Split(allowedStr, ",")
		for _, p := range parts {
			trimmed := strings.TrimSpace(p)
			if trimmed != "" {
				allowed = append(allowed, trimmed)
			}
		}
	}

	s.config = Config{
		BotToken:     botToken,
		AllowedUsers: allowed,
		Enabled:      enabled,
	}
}

func (s *Service) SaveConfig(cfg Config) error {
	s.mu.Lock()
	s.config = cfg
	s.mu.Unlock()

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}

	if err := os.WriteFile(s.configPath, data, 0600); err != nil {
		return fmt.Errorf("gagal nyimpen config telegram: %w", err)
	}

	// Restart service if config changed
	s.Stop()
	if cfg.Enabled && cfg.BotToken != "" {
		go s.Start()
	}
	return nil
}

func (s *Service) GetConfig() Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.config
}

func (s *Service) IsRunning() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.running
}

func (s *Service) Start() {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return
	}
	if s.config.BotToken == "" {
		s.mu.Unlock()
		log.Printf("[TELEGRAM] Bot Token kosong, service Telegram dibatalake.")
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	s.cancelFunc = cancel
	s.running = true
	token := s.config.BotToken
	s.mu.Unlock()

	log.Printf("[TELEGRAM] Nglakokake Telegram Bot Gateway listener...")

	// Auto-register Bot Commands for interactive Telegram UI
	_ = s.setMyCommands(token)

	go s.pollLoop(ctx, token)
}

func (s *Service) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.cancelFunc != nil {
		s.cancelFunc()
		s.cancelFunc = nil
	}
	s.running = false
	log.Printf("[TELEGRAM] Telegram Bot Gateway disetop.")
}

func (s *Service) setMyCommands(token string) error {
	commands := []BotCommand{
		{Command: "start", Description: "🤖 Menu Utama & Bantuan Bot"},
		{Command: "new", Description: "💬 Mulai Sesi Chat AI Baru"},
		{Command: "model", Description: "⚡ Pilih Model AI (Gemini, Claude, GPT)"},
		{Command: "status", Description: "🟢 Status Server & Workspace Aktif"},
		{Command: "workspace", Description: "📂 Informasi Path Workspace IDE"},
	}

	url := fmt.Sprintf("https://api.telegram.org/bot%s/setMyCommands", token)
	payload := map[string]interface{}{"commands": commands}
	body, _ := json.Marshal(payload)
	resp, err := s.httpClient.Post(url, "application/json", bytes.NewBuffer(body))
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	log.Printf("[TELEGRAM] Registered Telegram Bot commands menu successfully.")
	return nil
}

func (s *Service) pollLoop(ctx context.Context, token string) {
	offset := 0
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		updates, err := s.getUpdates(ctx, token, offset)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("[TELEGRAM ERROR] Gagal mriksa updates: %v", err)
			time.Sleep(3 * time.Second)
			continue
		}

		for _, update := range updates {
			if update.UpdateID >= offset {
				offset = update.UpdateID + 1
			}
			if update.Message != nil {
				go s.handleMessage(ctx, token, update.Message)
			}
			if update.CallbackQuery != nil {
				go s.handleCallbackQuery(ctx, token, update.CallbackQuery)
			}
		}
	}
}

func (s *Service) getUpdates(ctx context.Context, token string, offset int) ([]Update, error) {
	url := fmt.Sprintf("https://api.telegram.org/bot%s/getUpdates?offset=%d&timeout=25", token, offset)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("telegram API error (status %d): %s", resp.StatusCode, string(body))
	}

	var res getUpdatesResponse
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return nil, err
	}

	return res.Result, nil
}

func (s *Service) isUserAllowed(user *User) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if len(s.config.AllowedUsers) == 0 {
		return true
	}

	userIDStr := strconv.FormatInt(user.ID, 10)
	username := strings.ToLower(user.Username)

	for _, allowed := range s.config.AllowedUsers {
		allowedClean := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(allowed)), "@")
		if allowedClean == userIDStr || (username != "" && allowedClean == username) {
			return true
		}
	}
	return false
}

func (s *Service) handleMessage(ctx context.Context, token string, msg *Message) {
	if msg.From == nil || msg.Chat == nil {
		return
	}

	if !s.isUserAllowed(msg.From) {
		log.Printf("[TELEGRAM SECURITY] Pesan saka ID %d (@%s) diblokir", msg.From.ID, msg.From.Username)
		errMsg := fmt.Sprintf("⛔ *Akses Ditolak*\nID Telegram Anda (%d) belum diizinkan.\nTambahkan ID ini ke `TELEGRAM_ALLOWED_USERS` di IDE Mobile Antigravity.", msg.From.ID)
		_ = s.sendMessage(token, msg.Chat.ID, errMsg, "Markdown", nil)
		return
	}

	text := strings.TrimSpace(msg.Text)
	if text == "" {
		return
	}

	log.Printf("[TELEGRAM MESSAGE] Saka ID %d (@%s): %s", msg.From.ID, msg.From.Username, text)

	textLower := strings.ToLower(text)

	// Route slash commands or persistent keyboard buttons to command handler
	if strings.HasPrefix(text, "/") || isKeyboardButtonText(textLower) {
		s.handleCommand(ctx, token, msg, text)
		return
	}

	s.processAIChat(ctx, token, msg, text)
}

func isKeyboardButtonText(textLower string) bool {
	switch textLower {
	case "💬 sesi baru", "🟢 status server", "📂 workspace", "📋 list file project", "⚡ pilih model ai", "ℹ️ bantuan":
		return true
	}
	return false
}

// Persistent Bottom Reply Keyboard for Telegram App
func buildPersistentReplyKeyboard() *ReplyKeyboardMarkup {
	return &ReplyKeyboardMarkup{
		Keyboard: [][]KeyboardButton{
			{
				{Text: "💬 Sesi Baru"},
				{Text: "🟢 Status Server"},
			},
			{
				{Text: "📂 Workspace"},
				{Text: "📋 List File Project"},
			},
			{
				{Text: "⚡ Pilih Model AI"},
				{Text: "ℹ️ Bantuan"},
			},
		},
		IsPersistent:          true,
		ResizeKeyboard:        true,
		InputFieldPlaceholder: "Ketik pesan atau pilih menu di bawah...",
	}
}

// Interactive Model Selection Keyboard
func buildModelMenuKeyboard() *InlineKeyboardMarkup {
	return &InlineKeyboardMarkup{
		InlineKeyboard: [][]InlineKeyboardButton{
			{
				{Text: "⚡ Gemini 3.5 Flash", CallbackData: "act:set_model:gemini-3.5-flash"},
				{Text: "🧠 Gemini 3.1 Pro", CallbackData: "act:set_model:gemini-3.1-pro"},
			},
			{
				{Text: "🎭 Claude Sonnet", CallbackData: "act:set_model:claude-sonnet"},
				{Text: "🤖 GPT-OSS", CallbackData: "act:set_model:gpt-oss"},
			},
			{
				{Text: "⬅️ Kembali ke Menu Utama", CallbackData: "act:main_menu"},
			},
		},
	}
}

func (s *Service) handleCommand(ctx context.Context, token string, msg *Message, text string) {
	textLower := strings.ToLower(strings.TrimSpace(text))

	switch {
	case strings.HasPrefix(textLower, "/start"), strings.HasPrefix(textLower, "/help"), textLower == "ℹ️ bantuan":
		s.sendMainMenu(token, msg.Chat.ID)

	case strings.HasPrefix(textLower, "/new"), strings.HasPrefix(textLower, "/reset"), textLower == "💬 sesi baru":
		s.mu.Lock()
		newConvID := fmt.Sprintf("tg-%d-%d", msg.Chat.ID, time.Now().UnixNano())
		s.userConvs[msg.Chat.ID] = newConvID
		s.mu.Unlock()

		_ = s.sendMessage(token, msg.Chat.ID, "🔄 *Sesi percakapan AI baru telah dimulai.*", "Markdown", buildPersistentReplyKeyboard())

	case strings.HasPrefix(textLower, "/status"), textLower == "🟢 status server":
		s.sendStatusMessage(token, msg.Chat.ID)

	case strings.HasPrefix(textLower, "/workspace"), textLower == "📂 workspace", strings.HasPrefix(textLower, "/cd"):
		parts := strings.Fields(text)
		if len(parts) > 1 {
			targetPath := strings.TrimSpace(parts[1])
			errAdd := s.workspaceSvc.Add(targetPath)
			if errAdd == nil {
				s.mu.Lock()
				s.userConvs[msg.Chat.ID] = fmt.Sprintf("tg-%d-%d", msg.Chat.ID, time.Now().UnixNano())
				s.mu.Unlock()
				folderName := filepath.Base(targetPath)
				_ = s.sendMessage(token, msg.Chat.ID, fmt.Sprintf("✅ *Berhasil Berpindah Workspace!*\n• *Nama*: *%s*\n• *Path*: ` %s `\n🔄 _Sesi percakapan AI baru telah diinisialisasi untuk workspace ini._", folderName, s.workspaceSvc.ActiveWorkspaceDir()), "Markdown", s.buildWorkspaceSwitcherKeyboard())
			} else {
				_ = s.sendMessage(token, msg.Chat.ID, fmt.Sprintf("❌ *Gagal Berpindah Workspace*:\n`%v`", errAdd), "Markdown", s.buildWorkspaceSwitcherKeyboard())
			}
		} else {
			s.sendWorkspaceMessage(token, msg.Chat.ID)
		}

	case strings.HasPrefix(textLower, "/files"), textLower == "📋 list file project":
		s.sendListFilesMessage(token, msg.Chat.ID)

	case strings.HasPrefix(textLower, "/model"), textLower == "⚡ pilih model ai":
		s.sendModelSelectionMenu(token, msg.Chat.ID)

	default:
		s.processAIChat(ctx, token, msg, text)
	}
}

func (s *Service) sendMainMenu(token string, chatID int64) {
	helpText := `🤖 *Antigravity Mobile IDE - Remote Gateway*

Selamat datang! Anda dapat berinteraksi dengan AI Agent dan mengontrol IDE Antigravity langsung dari Telegram.

Gunakan menu tombol di bagian bawah keyboard aplikasi Telegram Anda atau kirimkan pesan langsung.`

	_ = s.sendMessage(token, chatID, helpText, "Markdown", buildPersistentReplyKeyboard())
}

func (s *Service) sendStatusMessage(token string, chatID int64) {
	activeDir := s.workspaceSvc.ActiveWorkspaceDir()

	s.mu.RLock()
	currentModel := s.userModels[chatID]
	if currentModel == "" {
		currentModel = "gemini-3.5-flash (default)"
	}
	s.mu.RUnlock()

	statusText := fmt.Sprintf(`🟢 *Status Mobile IDE Gateway*
• *Workspace Aktif*: `+"`%s`"+`
• *Port Server*: `+"`%s`"+`
• *Model AI Aktif*: `+"`%s`"+`
• *Status Bot*: Active & Listening`, activeDir, os.Getenv("PORT"), currentModel)

	_ = s.sendMessage(token, chatID, statusText, "Markdown", buildPersistentReplyKeyboard())
}

func (s *Service) buildWorkspaceSwitcherKeyboard() *InlineKeyboardMarkup {
	workspaces := s.workspaceSvc.WorkspacesList()
	activeDir := s.workspaceSvc.ActiveWorkspaceDir()

	var rows [][]InlineKeyboardButton
	for i, path := range workspaces {
		folderName := filepath.Base(path)
		if path == activeDir {
			folderName = "✅ " + folderName + " (Aktif)"
		} else {
			folderName = "📁 " + folderName
		}
		btn := InlineKeyboardButton{
			Text:         folderName,
			CallbackData: fmt.Sprintf("act:switch_ws:%d", i),
		}
		rows = append(rows, []InlineKeyboardButton{btn})
	}

	return &InlineKeyboardMarkup{
		InlineKeyboard: rows,
	}
}

func (s *Service) sendWorkspaceMessage(token string, chatID int64) {
	activeDir := s.workspaceSvc.ActiveWorkspaceDir()
	folderName := filepath.Base(activeDir)
	workspaces := s.workspaceSvc.WorkspacesList()

	wsText := fmt.Sprintf(`📂 *Kelola & Pindah Workspace*

• *Workspace Aktif*: *%s*
• *Path*: `+"`%s`"+`
• *Total Workspace Tersimpan*: %d

Klik salah satu tombol di bawah untuk berpindah workspace secara instan:`, folderName, activeDir, len(workspaces))

	_ = s.sendMessage(token, chatID, wsText, "Markdown", s.buildWorkspaceSwitcherKeyboard())
}

func (s *Service) sendListFilesMessage(token string, chatID int64) {
	files, err := s.workspaceSvc.ListFiles()
	var sb strings.Builder
	sb.WriteString("📋 *Daftar Berkas Workspace*\n\n")
	if err != nil || len(files) == 0 {
		sb.WriteString("_Belum ada berkas atau gagal membaca workspace._")
	} else {
		limit := 25
		for i, f := range files {
			if i >= limit {
				sb.WriteString(fmt.Sprintf("\n_...dan %d berkas lainnya._", len(files)-limit))
				break
			}
			icon := "📄"
			if f.IsDir {
				icon = "📁"
			}
			sb.WriteString(fmt.Sprintf("%s `%s`\n", icon, f.Name))
		}
	}
	_ = s.sendMessage(token, chatID, sb.String(), "Markdown", buildPersistentReplyKeyboard())
}

func (s *Service) sendModelSelectionMenu(token string, chatID int64) {
	s.mu.RLock()
	currentModel := s.userModels[chatID]
	if currentModel == "" {
		currentModel = "gemini-3.5-flash"
	}
	s.mu.RUnlock()

	menuText := fmt.Sprintf("⚡ *Pilih Model AI*\nModel aktif saat ini: ` %s `\n\nKlik salah satu tombol di bawah untuk mengganti model AI secara instan:", currentModel)
	_ = s.sendMessage(token, chatID, menuText, "Markdown", buildModelMenuKeyboard())
}

// Handler for Interactive Inline Button Clicks (Callback Queries)
func (s *Service) handleCallbackQuery(ctx context.Context, token string, cb *CallbackQuery) {
	if cb.From == nil || cb.Message == nil {
		return
	}

	if !s.isUserAllowed(cb.From) {
		_ = s.answerCallbackQuery(token, cb.ID, "⛔ Akses Ditolak")
		return
	}

	data := cb.Data
	chatID := cb.Message.Chat.ID
	msgID := cb.Message.MessageID

	log.Printf("[TELEGRAM CALLBACK] User ID %d clicked button: %s", cb.From.ID, data)

	if data == "act:select_model_menu" {
		_ = s.answerCallbackQuery(token, cb.ID, "⚡ Pilih Model")
		s.mu.RLock()
		currentModel := s.userModels[chatID]
		if currentModel == "" {
			currentModel = "gemini-3.5-flash"
		}
		s.mu.RUnlock()
		menuText := fmt.Sprintf("⚡ *Pilih Model AI*\nModel aktif saat ini: ` %s `\n\nKlik salah satu tombol di bawah untuk mengganti model AI secara instan:", currentModel)
		_ = s.editMessageText(token, chatID, msgID, menuText, "Markdown", buildModelMenuKeyboard())

	} else if data == "act:main_menu" {
		_ = s.answerCallbackQuery(token, cb.ID, "Menu Utama")
		s.sendMainMenu(token, chatID)

	} else if strings.HasPrefix(data, "act:set_model:") {
		newModel := strings.TrimPrefix(data, "act:set_model:")
		s.mu.Lock()
		s.userModels[chatID] = newModel
		s.mu.Unlock()

		_ = s.answerCallbackQuery(token, cb.ID, fmt.Sprintf("✅ Model diubah ke: %s", newModel))
		confirmText := fmt.Sprintf("✅ *Model AI Berhasil Diubah!*\nModel aktif: ` %s `", newModel)
		_ = s.editMessageText(token, chatID, msgID, confirmText, "Markdown", buildModelMenuKeyboard())

	} else if strings.HasPrefix(data, "act:switch_ws:") {
		idxStr := strings.TrimPrefix(data, "act:switch_ws:")
		idx, err := strconv.Atoi(idxStr)
		workspaces := s.workspaceSvc.WorkspacesList()
		if err == nil && idx >= 0 && idx < len(workspaces) {
			targetPath := workspaces[idx]
			errSelect := s.workspaceSvc.Select(targetPath)
			if errSelect == nil {
				s.mu.Lock()
				s.userConvs[chatID] = fmt.Sprintf("tg-%d-%d", chatID, time.Now().UnixNano())
				s.mu.Unlock()
				folderName := filepath.Base(targetPath)
				_ = s.answerCallbackQuery(token, cb.ID, fmt.Sprintf("✅ Switched to: %s", folderName))
				confirmText := fmt.Sprintf("✅ *Workspace Berhasil Diubah!*\n• *Nama*: *%s*\n• *Path*: ` %s `\n🔄 _Sesi AI diinisialisasi ulang untuk workspace baru._\n\nPilih workspace lain di bawah ini jika ingin mengganti:", folderName, targetPath)
				_ = s.editMessageText(token, chatID, msgID, confirmText, "Markdown", s.buildWorkspaceSwitcherKeyboard())
			} else {
				_ = s.answerCallbackQuery(token, cb.ID, fmt.Sprintf("❌ Gagal: %v", errSelect))
			}
		}
	}
}

func (s *Service) processAIChat(ctx context.Context, token string, msg *Message, text string) {
	chatID := msg.Chat.ID

	s.mu.Lock()
	convID, exists := s.userConvs[chatID]
	if !exists {
		convID = fmt.Sprintf("tg-%d-%d", chatID, time.Now().UnixNano())
		s.userConvs[chatID] = convID
	}
	selectedModel := s.userModels[chatID]
	s.mu.Unlock()

	_ = s.sendChatAction(token, chatID, "typing")

	// Send initial status message on Telegram in Hermes Agent style
	statusMsgID, _ := s.sendMessageAndGetID(token, chatID, "🧠 `[THINKING]` *Menganalisis instruksi & perencanaan agent...*", "Markdown", nil)

	req := chat.ChatRequest{
		Prompt:       text,
		Conversation: convID,
		Model:        selectedModel,
		Continue:     true,
	}

	cmd, stdoutPipe, err := s.chatSvc.StartChat(ctx, req, s.workspaceSvc.ActiveWorkspaceDir())
	if err != nil {
		if statusMsgID != 0 {
			_ = s.deleteMessage(token, chatID, statusMsgID)
		}
		_ = s.sendMessage(token, chatID, fmt.Sprintf("❌ Error: %v", err), "", nil)
		return
	}
	defer s.chatSvc.CleanupChat(convID, cmd)

	if cmd != nil {
		go func() {
			_ = cmd.Wait()
			_ = stdoutPipe.Close()
		}()
	}

	reader := bufio.NewReader(stdoutPipe)
	var fullResponse strings.Builder
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	done := make(chan bool)
	go func() {
		for {
			select {
			case <-ticker.C:
				_ = s.sendChatAction(token, chatID, "typing")
			case <-done:
				return
			}
		}
	}()

	lastStatusText := ""
	lastStatusTime := time.Now()

	for {
		line, err := reader.ReadString('\n')
		if len(line) > 0 {
			toolType, toolText := parseToolStatus(line)
			if toolType != "" && toolText != lastStatusText {
				lastStatusText = toolText
				if time.Since(lastStatusTime) > 300*time.Millisecond && statusMsgID != 0 {
					_ = s.editMessageText(token, chatID, statusMsgID, toolText, "Markdown", nil)
					lastStatusTime = time.Now()
				}
			}

			clean := cleanSSELine(line)
			if clean != "" {
				fullResponse.WriteString(clean)
			}
		}
		if err != nil {
			break
		}
	}
	close(done)

	// Clean up temporary live status message
	if statusMsgID != 0 {
		_ = s.deleteMessage(token, chatID, statusMsgID)
	}

	resultText := strings.TrimSpace(fullResponse.String())
	if resultText == "" {
		resultText = "(Tidak ada respon dari AI agent)"
	}

	s.sendChunkedMessage(token, chatID, resultText)
}

func parseToolStatus(line string) (string, string) {
	line = strings.TrimSuffix(line, "\r\n")
	line = strings.TrimSuffix(line, "\n")
	line = strings.TrimSpace(line)

	if line == "" || line == "data: [DONE]" {
		return "", ""
	}

	dataStr := line
	if strings.HasPrefix(line, "data: ") {
		dataStr = strings.TrimPrefix(line, "data: ")
	}

	var sd StreamData
	if err := json.Unmarshal([]byte(dataStr), &sd); err == nil && len(sd.ToolCalls) > 0 {
		for _, tc := range sd.ToolCalls {
			statusType, statusText := formatToolStatus(tc.Name, tc.ToolAction, tc.Args)
			if statusType != "" {
				return statusType, statusText
			}
		}
	}

	// Fallback unmarshal as generic map
	var generic map[string]interface{}
	if errMap := json.Unmarshal([]byte(dataStr), &generic); errMap == nil {
		if tcList, ok := generic["tool_calls"].([]interface{}); ok && len(tcList) > 0 {
			for _, item := range tcList {
				if tcMap, ok := item.(map[string]interface{}); ok {
					name, _ := tcMap["name"].(string)
					action, _ := tcMap["tool_action"].(string)
					args, _ := tcMap["args"].(map[string]interface{})
					return formatToolStatus(name, action, args)
				}
			}
		}
	}

	return "", ""
}

func formatToolStatus(name, action string, args map[string]interface{}) (string, string) {
	toolName := strings.ToLower(name)
	target := ""
	if args != nil {
		if cmd, ok := args["CommandLine"].(string); ok && cmd != "" {
			if len(cmd) > 45 {
				cmd = cmd[:42] + "..."
			}
			target = cmd
		} else if file, ok := args["TargetFile"].(string); ok && file != "" {
			target = filepath.Base(file)
		} else if path, ok := args["AbsolutePath"].(string); ok && path != "" {
			target = filepath.Base(path)
		} else if path, ok := args["SearchPath"].(string); ok && path != "" {
			target = filepath.Base(path)
		}
	}

	switch toolName {
	case "run_command":
		if target != "" {
			return "terminal", fmt.Sprintf("🛠️ `[EXEC]` *Eksekusi terminal sandbox:*\n`%s`", target)
		}
		return "terminal", "🧪 `[SANDBOX]` *Mengeksekusi perintah di terminal sandbox...*"

	case "view_file":
		if target != "" {
			return "read", fmt.Sprintf("📖 `[READ]` *Membaca berkas:* `%s`", target)
		}
		return "read", "📖 `[READ]` *Membaca konten berkas workspace...*"

	case "replace_file_content", "multi_replace_file_content":
		if target != "" {
			return "write", fmt.Sprintf("📝 `[WRITE]` *Mengedit/mengubah berkas:* `%s`", target)
		}
		return "write", "📝 `[WRITE]` *Mengedit berkas workspace...*"

	case "write_to_file":
		if target != "" {
			return "create", fmt.Sprintf("➕ `[WRITE]` *Membuat berkas baru:* `%s`", target)
		}
		return "create", "➕ `[WRITE]` *Membuat berkas baru...*"

	case "grep_search", "list_dir":
		return "search", "🔍 `[SEARCH]` *Menggeledah berkas & direktori project...*"
	}

	if action != "" {
		return "action", fmt.Sprintf("⚙️ `[TOOL]` *Menjalankan tool:* %s", action)
	}

	return "", ""
}

func cleanSSELine(line string) string {
	line = strings.TrimSuffix(line, "\r\n")
	line = strings.TrimSuffix(line, "\n")

	if strings.HasPrefix(line, "data: ") {
		dataStr := strings.TrimPrefix(line, "data: ")
		if dataStr == "[DONE]" {
			return ""
		}
		var parsed map[string]interface{}
		if err := json.Unmarshal([]byte(dataStr), &parsed); err == nil {
			if content, ok := parsed["content"].(string); ok {
				return content
			}
		}
		return dataStr + "\n"
	}

	// Try parsing raw JSON line from agy output
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(line), &parsed); err == nil {
		if content, ok := parsed["content"].(string); ok {
			return content
		}
		// If it's a JSON event without content (like tool_calls log), ignore raw JSON string
		return ""
	}

	re := regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)
	cleaned := re.ReplaceAllString(line, "")
	if strings.TrimSpace(cleaned) == "" {
		return ""
	}
	return cleaned + "\n"
}

func (s *Service) sendChatAction(token string, chatID int64, action string) error {
	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendChatAction", token)
	payload := map[string]interface{}{
		"chat_id": chatID,
		"action":  action,
	}
	body, _ := json.Marshal(payload)
	resp, err := s.httpClient.Post(url, "application/json", bytes.NewBuffer(body))
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	return nil
}

func (s *Service) answerCallbackQuery(token string, callbackQueryID string, text string) error {
	url := fmt.Sprintf("https://api.telegram.org/bot%s/answerCallbackQuery", token)
	payload := map[string]interface{}{
		"callback_query_id": callbackQueryID,
		"text":              text,
		"show_alert":        false,
	}
	body, _ := json.Marshal(payload)
	resp, err := s.httpClient.Post(url, "application/json", bytes.NewBuffer(body))
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	return nil
}

func (s *Service) editMessageText(token string, chatID int64, messageID int, text string, parseMode string, keyboard *InlineKeyboardMarkup) error {
	url := fmt.Sprintf("https://api.telegram.org/bot%s/editMessageText", token)
	payload := map[string]interface{}{
		"chat_id":    chatID,
		"message_id": messageID,
		"text":       text,
	}
	if parseMode != "" {
		payload["parse_mode"] = parseMode
	}
	if keyboard != nil {
		payload["reply_markup"] = keyboard
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	resp, err := s.httpClient.Post(url, "application/json", bytes.NewBuffer(body))
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	return nil
}

func (s *Service) sendChunkedMessage(token string, chatID int64, text string) {
	htmlText := markdownToTelegramHTML(text)
	const maxLen = 3800
	runes := []rune(htmlText)

	if len(runes) == 0 {
		runes = []rune(text)
	}

	for len(runes) > 0 {
		chunkSize := len(runes)
		if chunkSize > maxLen {
			chunkSize = maxLen
			lastIdx := strings.LastIndex(string(runes[:chunkSize]), "\n")
			if lastIdx > 1000 {
				chunkSize = len([]rune(string(runes[:lastIdx])))
			}
		}
		chunk := string(runes[:chunkSize])
		runes = runes[chunkSize:]

		err := s.sendMessage(token, chatID, chunk, "HTML", buildPersistentReplyKeyboard())
		if err != nil {
			// Fallback to plain text if HTML rendering failed
			_ = s.sendMessage(token, chatID, chunk, "", buildPersistentReplyKeyboard())
		}
	}
}

func markdownToTelegramHTML(md string) string {
	if md == "" {
		return ""
	}

	lines := strings.Split(md, "\n")
	var out []string

	inCodeBlock := false
	var codeBlockLines []string

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		if strings.HasPrefix(trimmed, "```") {
			if inCodeBlock {
				codeText := strings.Join(codeBlockLines, "\n")
				out = append(out, "<pre><code>"+htmlEscape(codeText)+"</code></pre>")
				codeBlockLines = nil
				inCodeBlock = false
			} else {
				inCodeBlock = true
				codeBlockLines = nil
			}
			continue
		}

		if inCodeBlock {
			codeBlockLines = append(codeBlockLines, line)
			continue
		}

		if strings.HasPrefix(line, "#") {
			headerText := strings.TrimLeft(line, "#")
			headerText = strings.TrimSpace(headerText)
			headerText = formatInlineMarkdown(headerText)
			out = append(out, "\n<b>"+headerText+"</b>")
			continue
		}

		if trimmed == "---" || trimmed == "***" || trimmed == "___" {
			out = append(out, "────────────────────────")
			continue
		}

		if strings.HasPrefix(trimmed, "* ") || strings.HasPrefix(trimmed, "- ") {
			bulletText := strings.TrimPrefix(strings.TrimPrefix(trimmed, "* "), "- ")
			bulletText = formatInlineMarkdown(bulletText)
			out = append(out, "• "+bulletText)
			continue
		}

		formatted := formatInlineMarkdown(line)
		out = append(out, formatted)
	}

	if inCodeBlock && len(codeBlockLines) > 0 {
		codeText := strings.Join(codeBlockLines, "\n")
		out = append(out, "<pre><code>"+htmlEscape(codeText)+"</code></pre>")
	}

	result := strings.Join(out, "\n")
	for strings.Contains(result, "\n\n\n") {
		result = strings.ReplaceAll(result, "\n\n\n", "\n\n")
	}
	return result
}

func htmlEscape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}

func formatInlineMarkdown(s string) string {
	s = htmlEscape(s)

	reCode := regexp.MustCompile("`([^`]+)`")
	s = reCode.ReplaceAllString(s, "<code>$1</code>")

	reBold := regexp.MustCompile(`\*\*([^*]+)\*\*`)
	s = reBold.ReplaceAllString(s, "<b>$1</b>")

	reItalic := regexp.MustCompile(`\*([^*]+)\*`)
	s = reItalic.ReplaceAllString(s, "<i>$1</i>")

	return s
}

func (s *Service) sendMessage(token string, chatID int64, text string, parseMode string, keyboard interface{}) error {
	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", token)
	payload := map[string]interface{}{
		"chat_id": chatID,
		"text":    text,
	}
	if parseMode != "" {
		payload["parse_mode"] = parseMode
	}
	if keyboard != nil {
		payload["reply_markup"] = keyboard
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	resp, err := s.httpClient.Post(url, "application/json", bytes.NewBuffer(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("telegram sendMessage error (%d): %s", resp.StatusCode, string(respBody))
	}
	return nil
}

type sendMessageResponse struct {
	OK     bool    `json:"ok"`
	Result Message `json:"result"`
}

func (s *Service) sendMessageAndGetID(token string, chatID int64, text string, parseMode string, keyboard *InlineKeyboardMarkup) (int, error) {
	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", token)
	payload := map[string]interface{}{
		"chat_id": chatID,
		"text":    text,
	}
	if parseMode != "" {
		payload["parse_mode"] = parseMode
	}
	if keyboard != nil {
		payload["reply_markup"] = keyboard
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return 0, err
	}

	resp, err := s.httpClient.Post(url, "application/json", bytes.NewBuffer(body))
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	var res sendMessageResponse
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return 0, err
	}

	return res.Result.MessageID, nil
}

func (s *Service) deleteMessage(token string, chatID int64, messageID int) error {
	if messageID == 0 {
		return nil
	}
	url := fmt.Sprintf("https://api.telegram.org/bot%s/deleteMessage", token)
	payload := map[string]interface{}{
		"chat_id":    chatID,
		"message_id": messageID,
	}
	body, _ := json.Marshal(payload)
	resp, err := s.httpClient.Post(url, "application/json", bytes.NewBuffer(body))
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	return nil
}
