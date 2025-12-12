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
	"time"
	"vpnbot/database"

	"gorm.io/gorm"
)

const ConfigPath = "/etc/sing-box/config.json"

type LogConfig struct {
	Level     string `json:"level"`
	Timestamp bool   `json:"timestamp"`
	Output    string `json:"output,omitempty"` // Добавляем файл логов для подсчета трафика
}

type InboundConfig struct {
	Type       string      `json:"type"`
	Tag        string      `json:"tag"`
	Listen     string      `json:"listen"`
	ListenPort int         `json:"listen_port"`
	Users      []VLessUser `json:"users,omitempty"`
	TLS        *TLSConfig  `json:"tls,omitempty"`
}

type VLessUser struct {
	Name string `json:"name"`
	UUID string `json:"uuid"`
	Flow string `json:"flow"`
}

type TLSConfig struct {
	Enabled    bool           `json:"enabled"`
	ServerName string         `json:"server_name"`
	Reality    *RealityConfig `json:"reality,omitempty"`
}

type RealityConfig struct {
	Enabled           bool     `json:"enabled"`
	Handshake         ServerEP `json:"handshake"`
	PrivateKey        string   `json:"private_key"`
	ShortID           []string `json:"short_id"`
	MaxTimeDifference string   `json:"max_time_difference,omitempty"`
}

type ServerEP struct {
	Server     string `json:"server"`
	ServerPort int    `json:"server_port"`
}

// Outbounds нужны, чтобы сервер знал, куда слать трафик (в интернет или в блок)
type OutboundConfig struct {
	Type string `json:"type"`
	Tag  string `json:"tag"`
}

// service/vpn.go

// Глобальная переменная для хранения предыдущего состояния соединений.
// Ключ - ID соединения (UUID от Sing-Box), Значение - сколько байт уже учтено.
var activeConnections = make(map[string]trafficData)

type trafficData struct {
	Up   int64
	Down int64
}

// ClashConnectionsResponse Структуры ответа Clash API (/connections)
type ClashConnectionsResponse struct {
	Connections []ClashConnection `json:"connections"`
}

type ClashConnection struct {
	ID       string        `json:"id"`
	Metadata ClashMetadata `json:"metadata"`
	Upload   int64         `json:"upload"`
	Download int64         `json:"download"`
}

type ClashMetadata struct {
	// В VLESS inbound Sing-Box обычно кладет имя юзера в поле User или Username
	// Проверьте поле "user" или "username" в JSON ответе, обычно это "username"
	Username string `json:"username"`
	User     string `json:"user"` // Иногда может быть здесь, зависит от версии
}

// SingBoxConfig 1. Обновляем главную структуру
type SingBoxConfig struct {
	Log          LogConfig           `json:"log"`
	Inbounds     []InboundConfig     `json:"inbounds"`
	Outbounds    []OutboundConfig    `json:"outbounds"`
	Experimental *ExperimentalConfig `json:"experimental,omitempty"` // <-- Добавлено
}

// ExperimentalConfig 2. Добавляем структуры для Experimental / Clash API
type ExperimentalConfig struct {
	ClashAPI ClashAPIConfig `json:"clash_api"`
}

type ClashAPIConfig struct {
	ExternalController string `json:"external_controller"` // например "127.0.0.1:9090"
	Secret             string `json:"secret"`              // Секретный токен для доступа
}

// --- Logic ---

func GenerateAndReload() error {
	var users []database.User
	// Берем всех активных (включая твоего админского юзера)
	database.DB.Where("status = ?", "active").Find(&users)

	var settings database.SystemSettings
	database.DB.First(&settings)

	// 1. Формируем список пользователей
	vlessUsers := []VLessUser{}
	for _, u := range users {
		vlessUsers = append(vlessUsers, VLessUser{
			Name: u.Username,
			UUID: u.UUID,
			Flow: "xtls-rprx-vision",
		})
	}

	// 2. Парсим ShortIDs
	var shortIDs []string
	if err := json.Unmarshal([]byte(settings.RealityShortIDs), &shortIDs); err != nil {
		// Фоллбэк, если JSON в базе побился
		shortIDs = []string{"207fc82a9f9e741f"}
	}

	// 3. Собираем конфиг
	cfg := SingBoxConfig{
		Log: LogConfig{
			Level:     "info",
			Timestamp: true,
			// Output больше не обязателен, если мы не читаем логи,
			// но можно оставить для отладки
			Output: "/etc/sing-box/access.log",
		},
		// Добавляем настройку API
		Experimental: &ExperimentalConfig{
			ClashAPI: ClashAPIConfig{
				ExternalController: "127.0.0.1:9090",   // Порт для управления
				Secret:             "MySecretToken123", // Придумайте сложный пароль
			},
		},
		Inbounds: []InboundConfig{
			{
				Type:       "vless",
				Tag:        "vless-in",
				Listen:     "::",
				ListenPort: settings.ListenPort,
				Users:      vlessUsers,
				TLS: &TLSConfig{
					Enabled:    true,
					ServerName: settings.ServerName,
					Reality: &RealityConfig{
						Enabled:    true,
						PrivateKey: settings.RealityPrivateKey,
						ShortID:    shortIDs,
						Handshake: ServerEP{
							Server:     settings.ServerName,
							ServerPort: 443,
						},
						MaxTimeDifference: "1m",
					},
				},
			},
		},
		// Добавляем Outbounds, иначе VPN подключится, но интернета не будет
		Outbounds: []OutboundConfig{
			{Type: "direct", Tag: "direct"},
			{Type: "block", Tag: "block"},
		},
	}

	// Генерируем JSON с отступами (красивый)
	file, _ := json.MarshalIndent(cfg, "", "  ")

	// Записываем в реальный файл конфига
	err := os.WriteFile(ConfigPath, file, 0644)
	if err != nil {
		log.Println("Error writing config file:", err)
		// Если прав нет (локальный тест), выведем в консоль
		fmt.Println(string(file))
	} else {
		return ReloadService()
	}
	return nil
}

func ReloadService() error {
	cmd := exec.Command("systemctl", "reload", "sing-box")
	if err := cmd.Run(); err != nil {
		log.Println("Warning: Failed to reload sing-box:", err)
		return err
	}
	log.Println("Sing-box config reloaded successfully")
	return nil
}

// GenerateLink создает VLESS ссылку для клиента
func GenerateLink(user database.User, settings database.SystemSettings, serverIP string) string {
	v := url.Values{}
	v.Add("security", "reality")
	v.Add("encryption", "none")
	v.Add("pbk", settings.RealityPublicKey)
	v.Add("fp", "chrome")
	v.Add("type", "tcp")
	v.Add("flow", "xtls-rprx-vision")
	v.Add("sni", settings.ServerName)

	var shortIDs []string
	json.Unmarshal([]byte(settings.RealityShortIDs), &shortIDs)
	if len(shortIDs) > 0 {
		v.Add("sid", shortIDs[0])
	}

	return fmt.Sprintf("vless://%s@%s:%d?%s#%s",
		user.UUID, serverIP, settings.ListenPort, v.Encode(), url.QueryEscape(user.Username))
}

// Регулярное выражение для парсинга логов Sing-box
// Ищет строки вида: inbound/vless-in[username] ... downlink: 1234, uplink: 5678

func UpdateTrafficViaAPI() error {
	apiURL := "http://127.0.0.1:9090/connections"
	apiSecret := "MySecretToken123" // Тот же, что в конфиге

	client := &http.Client{Timeout: 2 * time.Second}
	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+apiSecret)

	resp, err := client.Do(req)
	if err != nil {
		// Sing-Box может быть перезагружен или выключен
		return nil
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var data ClashConnectionsResponse
	if err := json.Unmarshal(body, &data); err != nil {
		return err
	}

	// Карта для накопления дельты (разницы) за этот опрос
	userTrafficDelta := make(map[string]trafficData)

	// Список активных ID в текущем опросе (для очистки памяти)
	currentActiveIDs := make(map[string]bool)

	for _, conn := range data.Connections {
		currentActiveIDs[conn.ID] = true

		// Определяем имя пользователя
		username := conn.Metadata.Username
		if username == "" {
			username = conn.Metadata.User
		}
		if username == "" {
			continue // Соединение без пользователя (системное или неизвестное)
		}

		// Считаем разницу (Delta)
		prevData := activeConnections[conn.ID]

		// Если соединение долго живет, bytes растут.
		// Новое - Старое = То, что набежало за последние 5 секунд.
		// Важно: если Sing-Box перезагрузился, ID сбросятся, так что коллизий не будет.
		deltaUp := conn.Upload - prevData.Up
		deltaDown := conn.Download - prevData.Down

		// Защита от странных скачков (если вдруг счетчик сбросился)
		if deltaUp < 0 {
			deltaUp = conn.Upload
		}
		if deltaDown < 0 {
			deltaDown = conn.Download
		}

		// Обновляем "предыдущее" состояние на текущее
		activeConnections[conn.ID] = trafficData{
			Up:   conn.Upload,
			Down: conn.Download,
		}

		// Добавляем к сумме по юзеру
		if deltaUp > 0 || deltaDown > 0 {
			stats := userTrafficDelta[username]
			stats.Up += deltaUp
			stats.Down += deltaDown
			userTrafficDelta[username] = stats
		}
	}

	// Очистка памяти: удаляем ID соединений, которых больше нет в API
	for id := range activeConnections {
		if !currentActiveIDs[id] {
			delete(activeConnections, id)
		}
	}

	// Запись в БД
	for username, traffic := range userTrafficDelta {
		total := traffic.Up + traffic.Down
		if total > 0 {
			err := database.DB.Model(&database.User{}).
				Where("username = ?", username).
				Update("traffic_used", gorm.Expr("traffic_used + ?", total)).Error

			if err != nil {
				log.Printf("DB Error update traffic for %s: %v", username, err)
			} else {
				// Проверка лимитов (ваша существующая функция)
				checkLimits(username)
			}
		}
	}

	return nil
}

// Вспомогательная функция для проверки лимитов (можно расширить позже)
func checkLimits(username string) {
	var user database.User
	if err := database.DB.Where("username = ?", username).First(&user).Error; err == nil {
		if user.TrafficLimit > 0 && user.TrafficUsed >= user.TrafficLimit {
			if user.Status == "active" {
				database.DB.Model(&user).Update("status", "expired")
				log.Printf("User %s expired due to traffic limit", username)
				// Тут можно вызвать GenerateAndReload(), чтобы отключить юзера немедленно
			}
		}
	}
}
