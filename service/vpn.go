package service

import (
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"os"
	"os/exec"
	"vpnbot/database"
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
			Level:     "info", // info достаточно, debug слишком тяжелый для продакшена
			Timestamp: true,
			Output:    "access.log", // ВАЖНО: пишем логи в файл, чтобы бот мог считать трафик
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
