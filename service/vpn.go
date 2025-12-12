package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"
	"vpnbot/database"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"gorm.io/gorm"

	// Импортируем типы для V2Ray API (gRPC)
	"github.com/v2fly/v2ray-core/v4/app/stats/command"
)

const ConfigPath = "/etc/sing-box/config.json"
const ApiAddr = "127.0.0.1:10000" // Порт для gRPC API

// --- Config Structures ---

type SingBoxConfig struct {
	Log          LogConfig           `json:"log"`
	Experimental *ExperimentalConfig `json:"experimental,omitempty"`
	Inbounds     []InboundConfig     `json:"inbounds"`
	Outbounds    []OutboundConfig    `json:"outbounds"`
}

// Конфигурация V2Ray API (вместо Clash API)
type ExperimentalConfig struct {
	V2RayAPI V2RayAPIConfig `json:"v2ray_api"`
}

type V2RayAPIConfig struct {
	Listen string      `json:"listen"`
	Stats  StatsConfig `json:"stats"`
}

type StatsConfig struct {
	Enabled  bool     `json:"enabled"`
	Inbounds []string `json:"inbounds"`
	Users    []string `json:"users"` // Список юзеров для отслеживания
}

type LogConfig struct {
	Level     string `json:"level"`
	Timestamp bool   `json:"timestamp"`
	Output    string `json:"output,omitempty"`
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

type OutboundConfig struct {
	Type string `json:"type"`
	Tag  string `json:"tag"`
}

// --- Logic ---

func GenerateAndReload() error {
	var users []database.User
	database.DB.Where("status = ?", "active").Find(&users)

	var settings database.SystemSettings
	database.DB.First(&settings)

	vlessUsers := []VLessUser{}
	userNames := []string{} // Список имен для статистики

	for _, u := range users {
		vlessUsers = append(vlessUsers, VLessUser{
			Name: u.Username,
			UUID: u.UUID,
			Flow: "xtls-rprx-vision",
		})
		// Собираем имена, чтобы Sing-box знал, кого считать
		userNames = append(userNames, u.Username)
	}

	var shortIDs []string
	if err := json.Unmarshal([]byte(settings.RealityShortIDs), &shortIDs); err != nil {
		shortIDs = []string{"207fc82a9f9e741f"}
	}

	cfg := SingBoxConfig{
		Log: LogConfig{
			Level:     "info",
			Timestamp: true,
			Output:    "/etc/sing-box/access.log",
		},
		// Включаем V2Ray API (gRPC)
		Experimental: &ExperimentalConfig{
			V2RayAPI: V2RayAPIConfig{
				Listen: ApiAddr, // 127.0.0.1:10000
				Stats: StatsConfig{
					Enabled:  true,
					Inbounds: []string{"vless-in"},
					Users:    userNames, // <--- ВАЖНО: передаем список юзеров
				},
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
		Outbounds: []OutboundConfig{
			{Type: "direct", Tag: "direct"},
			{Type: "block", Tag: "block"},
		},
	}

	file, _ := json.MarshalIndent(cfg, "", "  ")

	err := os.WriteFile(ConfigPath, file, 0644)
	if err != nil {
		log.Println("Error writing config file:", err)
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

// --- API Traffic Logic (gRPC V2Ray) ---

// Храним последнее значение счетчика (Absolute value), чтобы считать разницу
var previousStats = make(map[string]int64)

func UpdateTrafficViaAPI() error {
	// Подключаемся к gRPC серверу Sing-box
	conn, err := grpc.Dial(ApiAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		// Sing-box выключен
		return nil
	}
	defer conn.Close()

	client := command.NewStatsServiceClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// Запрашиваем статистику по всем юзерам (pattern: "user>>>")
	// Reset_: false, чтобы не сбрасывать счетчики в sing-box (мы сами считаем дельту)
	resp, err := client.QueryStats(ctx, &command.QueryStatsRequest{
		Pattern: "user>>>",
		Reset_:  false,
	})
	if err != nil {
		return err
	}

	// Карта для накопления дельты за этот проход
	userTrafficDelta := make(map[string]int64)
	currentStats := make(map[string]int64)

	for _, stat := range resp.Stat {
		// Name format: "user>>>username>>>traffic>>>downlink"
		parts := strings.Split(stat.Name, ">>>")
		if len(parts) < 4 {
			continue
		}
		username := parts[1]
		direction := parts[3] // downlink or uplink

		// Сохраняем текущее абсолютное значение
		// Ключ: "MRiaz_downlink"
		key := fmt.Sprintf("%s_%s", username, direction)
		currentStats[key] = stat.Value

		// Считаем разницу с предыдущим замером
		prev := previousStats[key]
		delta := stat.Value - prev

		// Если Sing-box перезагрузился, stat.Value будет меньше prev (сброс)
		// В этом случае delta = stat.Value (считаем с нуля)
		if delta < 0 {
			delta = stat.Value
		}

		if delta > 0 {
			userTrafficDelta[username] += delta
		}
	}

	// Обновляем "предыдущие" значения
	for k, v := range currentStats {
		previousStats[k] = v
	}

	// Пишем в БД
	for username, newBytes := range userTrafficDelta {
		if newBytes > 0 {
			err := database.DB.Model(&database.User{}).
				Where("username = ?", username).
				Update("traffic_used", gorm.Expr("traffic_used + ?", newBytes)).Error

			if err != nil {
				log.Printf("DB Error: %v", err)
			} else {
				checkLimits(username)
			}
		}
	}

	return nil
}

func checkLimits(username string) {
	var user database.User
	if err := database.DB.Where("username = ?", username).First(&user).Error; err == nil {
		if user.TrafficLimit > 0 && user.TrafficUsed >= user.TrafficLimit {
			if user.Status == "active" {
				database.DB.Model(&user).Update("status", "expired")
				log.Printf("User %s expired due to traffic limit", username)
				GenerateAndReload()
			}
		}
	}
}
