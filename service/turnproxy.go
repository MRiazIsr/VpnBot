package service

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
	"vpnbot/database"

	"github.com/google/uuid"
)

const (
	TurnProxyBinaryPath  = "/usr/local/bin/vk-turn-server"
	TurnProxyServiceName = "vk-turn-server"
	TurnProxyServicePath = "/etc/systemd/system/vk-turn-server.service"
	// Версия vk-turn-proxy для скачивания
	TurnProxyVersion = "v1.1.1"
	TurnProxyRepo    = "cacggghp/vk-turn-proxy"
)

// --- Установка и управление сервисом ---

// InstallTurnProxy скачивает бинарник vk-turn-proxy server с GitHub releases
func InstallTurnProxy() error {
	if _, err := os.Stat(TurnProxyBinaryPath); err == nil {
		log.Println("vk-turn-proxy server уже установлен:", TurnProxyBinaryPath)
		return nil
	}

	// Определяем архитектуру
	goarch := runtime.GOARCH
	goos := runtime.GOOS

	// vk-turn-proxy публикует бинарники как server_linux_amd64, server_linux_arm64 и т.д.
	arch := goarch
	if arch == "amd64" {
		arch = "amd64"
	} else if arch == "arm64" {
		arch = "arm64"
	}

	downloadURL := fmt.Sprintf(
		"https://github.com/%s/releases/download/%s/server-%s-%s",
		TurnProxyRepo, TurnProxyVersion, goos, arch,
	)
	log.Println("Скачиваем vk-turn-proxy server:", downloadURL)

	// Скачиваем бинарник (--content-on-error=off чтобы не сохранять 404 HTML)
	cmd := exec.Command("sh", "-c", fmt.Sprintf(
		"wget -qO '%s' --tries=2 '%s' && chmod +x '%s'",
		TurnProxyBinaryPath, downloadURL, TurnProxyBinaryPath,
	))
	if output, err := cmd.CombinedOutput(); err != nil {
		log.Println("Ошибка скачивания vk-turn-proxy:", err, string(output))
		return fmt.Errorf("ошибка установки vk-turn-proxy: %w", err)
	}

	log.Println("vk-turn-proxy server установлен:", TurnProxyBinaryPath)
	return nil
}

// EnsureTurnProxyService создаёт systemd unit для vk-turn-proxy server
func EnsureTurnProxyService(cfg database.TurnConfig) error {
	listenAddr := fmt.Sprintf("0.0.0.0:%d", cfg.TunnelPort)
	forwardPort := cfg.ForwardPort
	if forwardPort == 0 {
		forwardPort = 8444
	}
	connectAddr := fmt.Sprintf("127.0.0.1:%d", forwardPort)

	unit := fmt.Sprintf(`[Unit]
Description=VK TURN Proxy Server
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=%s -listen %s -connect %s
Restart=on-failure
RestartSec=5
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
`, TurnProxyBinaryPath, listenAddr, connectAddr)

	if err := os.WriteFile(TurnProxyServicePath, []byte(unit), 0644); err != nil {
		log.Println("Ошибка создания systemd unit vk-turn-server:", err)
		return err
	}

	cmd := exec.Command("systemctl", "daemon-reload")
	if err := cmd.Run(); err != nil {
		log.Println("Ошибка daemon-reload:", err)
		return err
	}

	log.Println("systemd unit vk-turn-server создан")
	return nil
}

// StartTurnProxy запускает сервис
func StartTurnProxy() error {
	cmd := exec.Command("systemctl", "start", TurnProxyServiceName)
	if err := cmd.Run(); err != nil {
		log.Println("Ошибка запуска vk-turn-server:", err)
		return err
	}
	exec.Command("systemctl", "enable", TurnProxyServiceName).Run()
	log.Println("vk-turn-server запущен")

	// Обновляем статус в БД
	updateTurnStatus("active", "Сервис запущен")
	return nil
}

// StopTurnProxy останавливает сервис
func StopTurnProxy() error {
	cmd := exec.Command("systemctl", "stop", TurnProxyServiceName)
	if err := cmd.Run(); err != nil {
		log.Println("Ошибка остановки vk-turn-server:", err)
		return err
	}
	log.Println("vk-turn-server остановлен")

	updateTurnStatus("inactive", "Сервис остановлен")
	return nil
}

// IsTurnProxyRunning проверяет запущен ли сервис
func IsTurnProxyRunning() bool {
	cmd := exec.Command("systemctl", "is-active", "--quiet", TurnProxyServiceName)
	return cmd.Run() == nil
}

// SetupTurnProxy — полный цикл: установка + создание сервиса + запуск
func SetupTurnProxy() error {
	var cfg database.TurnConfig
	if err := database.DB.First(&cfg).Error; err != nil {
		return fmt.Errorf("turn: конфиг не найден")
	}

	if !cfg.Enabled {
		log.Println("turn: выключен, пропускаем настройку")
		return nil
	}

	if err := InstallTurnProxy(); err != nil {
		updateTurnStatus("error", fmt.Sprintf("Ошибка установки: %v", err))
		return err
	}

	if err := EnsureTurnProxyService(cfg); err != nil {
		updateTurnStatus("error", fmt.Sprintf("Ошибка создания сервиса: %v", err))
		return err
	}

	// Открываем порт в firewall
	go func() {
		port := cfg.TunnelPort
		if port == 0 {
			port = 56000
		}
		if err := OpenFirewallPort(port, "udp", "VK TURN tunnel"); err != nil {
			log.Printf("turn: не удалось открыть порт %d в firewall: %v", port, err)
		}
	}()

	return StartTurnProxy()
}

// --- VK API: создание звонков ---

// CreateVKCall создаёт VK-звонок через API и возвращает join_link
func CreateVKCall(vkToken string) (joinLink, callID string, err error) {
	if vkToken == "" {
		return "", "", fmt.Errorf("VK токен не задан")
	}

	apiURL := "https://api.vk.com/method/calls.start"
	data := url.Values{
		"access_token": {vkToken},
		"v":            {"5.264"},
	}

	resp, err := http.PostForm(apiURL, data)
	if err != nil {
		return "", "", fmt.Errorf("ошибка запроса VK API: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", "", fmt.Errorf("ошибка чтения ответа VK API: %w", err)
	}

	var result struct {
		Response struct {
			JoinLink string `json:"join_link"`
			CallID   string `json:"call_id"`
		} `json:"response"`
		Error struct {
			ErrorCode int    `json:"error_code"`
			ErrorMsg  string `json:"error_msg"`
		} `json:"error"`
	}

	if err := json.Unmarshal(body, &result); err != nil {
		return "", "", fmt.Errorf("ошибка парсинга ответа VK API: %w", err)
	}

	if result.Error.ErrorCode != 0 {
		return "", "", fmt.Errorf("VK API ошибка %d: %s", result.Error.ErrorCode, result.Error.ErrorMsg)
	}

	if result.Response.JoinLink == "" {
		return "", "", fmt.Errorf("VK API вернул пустую ссылку")
	}

	return result.Response.JoinLink, result.Response.CallID, nil
}

// --- VK TURN Credential Harvesting (6-шаговая анонимная цепочка) ---

const (
	vkClientID     = "6287487"
	vkClientSecret = "QbYic1K3lEV5kTGiqlq2"
	vkAppKey       = "CGMMEJLGDIHBABABA"
	vkUserAgent    = "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:144.0) Gecko/20100101 Firefox/144.0"
)

// doVKRequest выполняет POST-запрос и возвращает распарсенный JSON
func doVKRequest(requestURL string, data url.Values) (map[string]interface{}, error) {
	client := &http.Client{Timeout: 20 * time.Second}

	req, err := http.NewRequest("POST", requestURL, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", vkUserAgent)

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("ошибка парсинга JSON: %w (body: %s)", err, string(body))
	}

	return result, nil
}

// extractLinkID извлекает ID из ссылки VK-звонка
func extractLinkID(joinLink string) string {
	// Формат: https://vk.com/call/join/ABC123...
	parts := strings.Split(joinLink, "/")
	if len(parts) > 0 {
		return parts[len(parts)-1]
	}
	return joinLink
}

// getNestedString безопасно извлекает строку из вложенного map
func getNestedString(m map[string]interface{}, keys ...string) (string, error) {
	current := m
	for i, key := range keys {
		val, ok := current[key]
		if !ok {
			return "", fmt.Errorf("ключ '%s' не найден", key)
		}
		if i == len(keys)-1 {
			str, ok := val.(string)
			if !ok {
				return "", fmt.Errorf("значение ключа '%s' не строка", key)
			}
			return str, nil
		}
		current, ok = val.(map[string]interface{})
		if !ok {
			return "", fmt.Errorf("значение ключа '%s' не объект", key)
		}
	}
	return "", fmt.Errorf("пустой путь ключей")
}

// TestTurnCreds выполняет 6-шаговую анонимную цепочку для проверки ссылки VK-звонка
// Возвращает адрес TURN-сервера если ссылка рабочая
func TestTurnCreds(joinLink string) (turnServer string, err error) {
	linkID := extractLinkID(joinLink)
	if linkID == "" {
		return "", fmt.Errorf("не удалось извлечь ID из ссылки")
	}

	// Шаг 1: Получаем анонимный токен
	resp1, err := doVKRequest("https://login.vk.ru/?act=get_anonym_token", url.Values{
		"client_secret": {vkClientSecret},
		"client_id":     {vkClientID},
		"scopes":        {"audio_anonymous,video_anonymous,photos_anonymous,profile_anonymous"},
		"version":       {"1"},
		"app_id":        {vkClientID},
	})
	if err != nil {
		return "", fmt.Errorf("шаг 1 (anonym token): %w", err)
	}
	token1, err := getNestedString(resp1, "data", "access_token")
	if err != nil {
		return "", fmt.Errorf("шаг 1: %w", err)
	}

	// Шаг 2: Получаем payload
	resp2, err := doVKRequest(
		fmt.Sprintf("https://api.vk.ru/method/calls.getAnonymousAccessTokenPayload?v=5.264&client_id=%s", vkClientID),
		url.Values{"access_token": {token1}},
	)
	if err != nil {
		return "", fmt.Errorf("шаг 2 (payload): %w", err)
	}
	token2, err := getNestedString(resp2, "response", "payload")
	if err != nil {
		return "", fmt.Errorf("шаг 2: %w", err)
	}

	// Шаг 3: Получаем messages-scoped token
	resp3, err := doVKRequest("https://login.vk.ru/?act=get_anonym_token", url.Values{
		"client_id":     {vkClientID},
		"token_type":    {"messages"},
		"payload":       {token2},
		"client_secret": {vkClientSecret},
		"version":       {"1"},
		"app_id":        {vkClientID},
	})
	if err != nil {
		return "", fmt.Errorf("шаг 3 (messages token): %w", err)
	}
	token3, err := getNestedString(resp3, "data", "access_token")
	if err != nil {
		return "", fmt.Errorf("шаг 3: %w", err)
	}

	// Шаг 4: Получаем anonymous call token
	resp4, err := doVKRequest("https://api.vk.ru/method/calls.getAnonymousToken?v=5.264", url.Values{
		"vk_join_link": {joinLink},
		"name":         {"vpnbot"},
		"access_token": {token3},
	})
	if err != nil {
		return "", fmt.Errorf("шаг 4 (call token): %w", err)
	}
	token4, err := getNestedString(resp4, "response", "token")
	if err != nil {
		return "", fmt.Errorf("шаг 4: %w", err)
	}

	// Шаг 5: Создаём OK.ru анонимную сессию
	deviceID := uuid.New().String()
	sessionData := fmt.Sprintf(`{"version":2,"device_id":"%s","client_version":1.1,"client_type":"SDK_JS"}`, deviceID)
	resp5, err := doVKRequest("https://calls.okcdn.ru/fb.do", url.Values{
		"session_data":    {sessionData},
		"method":          {"auth.anonymLogin"},
		"format":          {"JSON"},
		"application_key": {vkAppKey},
	})
	if err != nil {
		return "", fmt.Errorf("шаг 5 (OK.ru session): %w", err)
	}
	token5, ok := resp5["session_key"].(string)
	if !ok {
		return "", fmt.Errorf("шаг 5: session_key не найден в ответе")
	}

	// Шаг 6: Присоединяемся к конференции и получаем TURN credentials
	resp6, err := doVKRequest("https://calls.okcdn.ru/fb.do", url.Values{
		"joinLink":        {linkID},
		"isVideo":         {"false"},
		"protocolVersion": {"5"},
		"anonymToken":     {token4},
		"method":          {"vchat.joinConversationByLink"},
		"format":          {"JSON"},
		"application_key": {vkAppKey},
		"session_key":     {token5},
	})
	if err != nil {
		return "", fmt.Errorf("шаг 6 (join conference): %w", err)
	}

	// Извлекаем TURN-сервер из ответа
	turnInfo, ok := resp6["turn_server"].(map[string]interface{})
	if !ok {
		// Пробуем найти в другом формате
		return "", fmt.Errorf("шаг 6: turn_server не найден в ответе (ссылка может быть неактивна)")
	}

	urls, ok := turnInfo["urls"].([]interface{})
	if !ok || len(urls) == 0 {
		return "", fmt.Errorf("шаг 6: turn URLs не найдены")
	}

	// Парсим первый TURN URL для получения адреса сервера
	turnURL := fmt.Sprintf("%v", urls[0])
	// Формат: turn:call6-7.vkuser.net:3478?transport=tcp
	turnServer = turnURL
	if strings.HasPrefix(turnURL, "turn:") {
		turnServer = strings.TrimPrefix(turnURL, "turn:")
		if idx := strings.Index(turnServer, "?"); idx != -1 {
			turnServer = turnServer[:idx]
		}
	}

	log.Printf("turn: credentials успешно получены, TURN сервер: %s", turnServer)
	return turnServer, nil
}

// --- Генерация клиентского конфига ---

// GenerateTurnClientInstruction генерирует инструкцию для пользователя
func GenerateTurnClientInstruction(serverIP string, cfg database.TurnConfig) string {
	tunnelPort := cfg.TunnelPort
	if tunnelPort == 0 {
		tunnelPort = 56000
	}
	streams := cfg.Streams
	if streams == 0 {
		streams = 16
	}

	peer := fmt.Sprintf("%s:%d", serverIP, tunnelPort)

	instruction := fmt.Sprintf(
		"🌐 *VK TURN Tunnel*\n\n"+
			"Этот режим маскирует VPN под VK-звонок\\. Трафик идёт через серверы VK и не может быть заблокирован\\.\n\n"+
			"*1\\. Скачайте клиент:*\n"+
			"[Windows](https://github.com/%s/releases/download/%s/client-windows-amd64.exe)\n"+
			"[Linux](https://github.com/%s/releases/download/%s/client-linux-amd64)\n"+
			"[macOS](https://github.com/%s/releases/download/%s/client-darwin-amd64)\n\n"+
			"*2\\. Запустите клиент:*\n"+
			"`./client -udp -peer %s -vk-link %s -n %d`\n\n"+
			"*3\\. Настройте Hiddify:*\n"+
			"Endpoint: `127.0.0.1:9000`\n"+
			"Используйте те же настройки подключения, но замените адрес сервера на `127.0.0.1:9000`",
		TurnProxyRepo, TurnProxyVersion,
		TurnProxyRepo, TurnProxyVersion,
		TurnProxyRepo, TurnProxyVersion,
		peer, cfg.VKJoinLink, streams,
	)

	return instruction
}

// --- Утилиты ---

func updateTurnStatus(status, msg string) {
	var cfg database.TurnConfig
	if err := database.DB.First(&cfg).Error; err != nil {
		return
	}
	cfg.Status = status
	cfg.StatusMsg = msg
	database.DB.Save(&cfg)
}
