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
	Users    []string `json:"users"`
}

type LogConfig struct {
	Level     string `json:"level"`
	Timestamp bool   `json:"timestamp"`
	Output    string `json:"output,omitempty"`
}

type InboundConfig struct {
	Type       string           `json:"type"`
	Tag        string           `json:"tag"`
	Listen     string           `json:"listen"`
	ListenPort int              `json:"listen_port"`
	Users      []VLessUser      `json:"users,omitempty"`
	TLS        *TLSConfig       `json:"tls,omitempty"`
	Transport  *TransportConfig `json:"transport,omitempty"`
	Multiplex  *MultiplexConfig `json:"multiplex,omitempty"`
}

type TransportConfig struct {
	Type string `json:"type"`
}

type MultiplexConfig struct {
	Enabled bool `json:"enabled"`
}

type VLessUser struct {
	Name string `json:"name"`
	UUID string `json:"uuid"`
	Flow string `json:"flow,omitempty"`
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

	legacyUsers := []VLessUser{}
	newUsers := []VLessUser{}
	userNames := []string{}

	for _, u := range users {
		legacyUsers = append(legacyUsers, VLessUser{
			Name: u.Username,
			UUID: u.UUID,
			Flow: "xtls-rprx-vision",
		})
		newUsers = append(newUsers, VLessUser{
			Name: u.Username,
			UUID: u.UUID,
		})
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
		Experimental: &ExperimentalConfig{
			V2RayAPI: V2RayAPIConfig{
				Listen: ApiAddr,
				Stats: StatsConfig{
					Enabled:  true,
					Inbounds: []string{"vless-in", "vless-in-h2"},
					Users:    userNames,
				},
			},
		},
		Inbounds: []InboundConfig{
			{
				Type:       "vless",
				Tag:        "vless-in",
				Listen:     "::",
				ListenPort: settings.ListenPort,
				Users:      legacyUsers,
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
			{
				Type:       "vless",
				Tag:        "vless-in-h2",
				Listen:     "::",
				ListenPort: 2053,
				Users:      newUsers,
				TLS: &TLSConfig{
					Enabled:    true,
					ServerName: "api.yandex.ru",
					Reality: &RealityConfig{
						Enabled:    true,
						PrivateKey: settings.RealityPrivateKey,
						ShortID:    shortIDs,
						Handshake: ServerEP{
							Server:     "api.yandex.ru",
							ServerPort: 443,
						},
						MaxTimeDifference: "1m",
					},
				},
				Transport: &TransportConfig{Type: "http"},
				Multiplex: &MultiplexConfig{Enabled: true},
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

func GenerateLinkAntiCensorship(user database.User, settings database.SystemSettings, serverIP string) string {
	v := url.Values{}
	v.Add("security", "reality")
	v.Add("encryption", "none")
	v.Add("pbk", settings.RealityPublicKey)
	v.Add("fp", "chrome")
	v.Add("type", "http")
	v.Add("sni", "api.yandex.ru")

	var shortIDs []string
	json.Unmarshal([]byte(settings.RealityShortIDs), &shortIDs)
	if len(shortIDs) > 0 {
		v.Add("sid", shortIDs[0])
	}

	return fmt.Sprintf("vless://%s@%s:%d?%s#%s",
		user.UUID, serverIP, 2053, v.Encode(), url.QueryEscape(user.Username))
}

// --- API Traffic Logic (gRPC V2Ray) ---

var previousStats = make(map[string]int64)

func UpdateTrafficViaAPI() error {
	conn, err := grpc.Dial(ApiAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil
	}
	defer conn.Close()

	client := command.NewStatsServiceClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// Запрашиваем всё, но фильтруем в коде
	resp, err := client.QueryStats(ctx, &command.QueryStatsRequest{
		Pattern: "",
		Reset_:  false,
	})
	if err != nil {
		return err
	}

	userTrafficDelta := make(map[string]int64)
	currentStats := make(map[string]int64)

	for _, stat := range resp.Stat {
		// Формат имени: "type>>>name>>>metric>>>dimension"
		// Примеры:
		// "user>>>MRiaz>>>traffic>>>downlink" (Нам нужно это)
		// "inbound>>>vless-in>>>traffic>>>downlink" (Это вызывает ошибку!)

		parts := strings.Split(stat.Name, ">>>")
		if len(parts) < 4 {
			continue
		}

		// Фильтр: обрабатываем только статистику пользователей
		if parts[0] != "user" {
			continue
		}

		username := parts[1]
		direction := parts[3]

		key := fmt.Sprintf("%s_%s", username, direction)
		currentStats[key] = stat.Value

		prev := previousStats[key]
		delta := stat.Value - prev

		if delta < 0 {
			delta = stat.Value
		}

		if delta > 0 {
			userTrafficDelta[username] += delta
		}
	}

	for k, v := range currentStats {
		previousStats[k] = v
	}

	for username, newBytes := range userTrafficDelta {
		if newBytes > 0 {
			// Используем Transaction для надежности
			err := database.DB.Transaction(func(tx *gorm.DB) error {
				// Проверяем, существует ли юзер, чтобы избежать ошибки "record not found"
				var count int64
				tx.Model(&database.User{}).Where("username = ?", username).Count(&count)
				if count == 0 {
					return nil // Пропускаем несуществующих (на всякий случай)
				}

				return tx.Model(&database.User{}).
					Where("username = ?", username).
					Update("traffic_used", gorm.Expr("traffic_used + ?", newBytes)).Error
			})

			if err != nil {
				log.Printf("DB Error for %s: %v", username, err)
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
