package service

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"vpnbot/database"

	"gorm.io/gorm"
)

const ConfigPath = "/etc/sing-box/config.json"

// --- Structures for Sing-box JSON ---
// Структура максимально приближена к твоему примеру
type SingBoxConfig struct {
	Log       LogConfig        `json:"log"`
	Inbounds  []InboundConfig  `json:"inbounds"`
	Outbounds []OutboundConfig `json:"outbounds"` // Обязательно нужно для работы интернета!
}

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
		// Фоллбэк если JSON в базе побился
		shortIDs = []string{"207fc82a9f9e741f"}
	}

	// 3. Собираем конфиг
	cfg := SingBoxConfig{
		Log: LogConfig{
			Level:     "info",
			Timestamp: true,
			Output:    "/etc/sing-box/access.log", // <--- ВАЖНО: Тот же самый абсолютный путь
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
var logRegex = regexp.MustCompile(`inbound/vless-in\[(.*?)\]: connection closed.*downlink: (\d+).*uplink: (\d+)`)

func UpdateTrafficStats() error {
	// ИСПОЛЬЗУЕМ АБСОЛЮТНЫЕ ПУТИ
	// Убедись, что путь совпадает с тем, что мы пропишем в конфиге ниже
	logFile := "/etc/sing-box/access.log"
	tempFile := "/etc/sing-box/access_log_processing.tmp"

	// 1. Проверяем, существует ли файл логов
	if _, err := os.Stat(logFile); os.IsNotExist(err) {
		// Добавь этот лог, чтобы видеть в консоли, если файла реально нет
		log.Println("Traffic log file not found at:", logFile)
		return nil
	}

	// 2. Переименовываем файл (ротация), чтобы Sing-box начал писать в новый
	err := os.Rename(logFile, tempFile)
	if err != nil {
		log.Println("Error renaming log file:", err)
		return err
	}

	// 3. Перезагружаем Sing-box, чтобы он пересоздал access.log
	// Важно: используем kill -HUP или reload, чтобы он закрыл старый дескриптор
	if err := ReloadService(); err != nil {
		log.Println("Error reloading service during log rotation:", err)
		// Если не удалось перезагрузить, лучше вернуть файл обратно, чтобы не терять логи (опционально)
	}

	// 4. Читаем временный файл
	file, err := os.Open(tempFile)
	if err != nil {
		return err
	}
	defer file.Close()
	defer os.Remove(tempFile) // Удаляем файл после обработки

	// Карта для накопления трафика за этот проход: map[username]Traffic
	type trafficDelta struct {
		Down int64
		Up   int64
	}
	stats := make(map[string]trafficDelta)

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()

		// Пример строки лога:
		// ... inbound/vless-in[user1]: connection closed ... downlink: 100, uplink: 200
		matches := logRegex.FindStringSubmatch(line)
		if len(matches) == 4 {
			username := matches[1]
			down, _ := strconv.ParseInt(matches[2], 10, 64)
			up, _ := strconv.ParseInt(matches[3], 10, 64)

			current := stats[username]
			current.Down += down
			current.Up += up
			stats[username] = current
		}
	}

	if err := scanner.Err(); err != nil {
		log.Println("Error scanning log file:", err)
	}

	// 5. Записываем накопленные данные в БД
	for username, traffic := range stats {
		totalBytes := traffic.Down + traffic.Up

		// Используем SQL update для атомарности (traffic_used = traffic_used + new_bytes)
		// Это безопаснее, чем читать-изменять-сохранять
		err := database.DB.Model(&database.User{}).
			Where("username = ?", username).
			Update("traffic_used", gorm.Expr("traffic_used + ?", totalBytes)).Error

		if err != nil {
			log.Printf("Failed to update traffic for user %s: %v", username, err)
		} else {
			// Опционально: проверка лимитов
			checkLimits(username)
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
