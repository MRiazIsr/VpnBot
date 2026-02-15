package service

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log"
	"net"
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
	Inbounds     []SingboxInbound     `json:"inbounds"`
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

type SingboxInbound struct {
	Type       string           `json:"type"`
	Tag        string           `json:"tag"`
	Listen     string           `json:"listen"`
	ListenPort int              `json:"listen_port"`
	Users      interface{}      `json:"users,omitempty"`
	TLS        *TLSConfig       `json:"tls,omitempty"`
	Transport  *TransportConfig `json:"transport,omitempty"`
	Multiplex  *MultiplexConfig `json:"multiplex,omitempty"`
}

type TransportConfig struct {
	Type        string `json:"type"`
	ServiceName string `json:"service_name,omitempty"`
	Path        string `json:"path,omitempty"`
	Mode        string `json:"mode,omitempty"` // xhttp only: "auto" | "packet-up" | "stream-up" | "stream-one"
}

type MultiplexConfig struct {
	Enabled bool `json:"enabled"`
}

type Hysteria2User struct {
	Name     string `json:"name"`
	Password string `json:"password"`
}

type VLessUser struct {
	Name string `json:"name"`
	UUID string `json:"uuid"`
	Flow string `json:"flow,omitempty"`
}

type TLSConfig struct {
	Enabled         bool           `json:"enabled"`
	ServerName      string         `json:"server_name,omitempty"`
	Reality         *RealityConfig `json:"reality,omitempty"`
	CertificatePath string         `json:"certificate_path,omitempty"`
	KeyPath         string         `json:"key_path,omitempty"`
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

func buildLegacyUsers(users []database.User) []VLessUser {
	result := []VLessUser{}
	for _, u := range users {
		result = append(result, VLessUser{
			Name: u.Username,
			UUID: u.UUID,
			Flow: "xtls-rprx-vision",
		})
	}
	return result
}

func buildNewUsers(users []database.User) []VLessUser {
	result := []VLessUser{}
	for _, u := range users {
		result = append(result, VLessUser{
			Name: u.Username,
			UUID: u.UUID,
		})
	}
	return result
}

func buildHy2Users(users []database.User) []Hysteria2User {
	result := []Hysteria2User{}
	for _, u := range users {
		result = append(result, Hysteria2User{
			Name:     u.Username,
			Password: u.UUID,
		})
	}
	return result
}

func buildUserNames(users []database.User) []string {
	result := []string{}
	for _, u := range users {
		result = append(result, u.Username)
	}
	return result
}

func buildSingboxInbound(ib database.InboundConfig, users []database.User) SingboxInbound {
	var ibUsers interface{}
	switch ib.UserType {
	case "legacy":
		ibUsers = buildLegacyUsers(users)
	case "new":
		ibUsers = buildNewUsers(users)
	case "hy2":
		ibUsers = buildHy2Users(users)
	}

	sb := SingboxInbound{
		Type:       ib.Protocol,
		Tag:        ib.Tag,
		Listen:     "::",
		ListenPort: ib.ListenPort,
		Users:      ibUsers,
	}

	// TLS
	switch ib.TLSType {
	case "reality":
		sb.TLS = &TLSConfig{
			Enabled:    true,
			ServerName: ib.SNI,
			Reality: &RealityConfig{
				Enabled:    true,
				PrivateKey: ib.RealityPrivateKey,
				ShortID:    []string(ib.RealityShortIDs),
				Handshake: ServerEP{
					Server:     ib.SNI,
					ServerPort: 443,
				},
				MaxTimeDifference: "1m",
			},
		}
	case "certificate":
		sb.TLS = &TLSConfig{
			Enabled:         true,
			CertificatePath: ib.CertPath,
			KeyPath:         ib.KeyPath,
		}
	}

	// Transport
	if ib.Transport != "" {
		sb.Transport = &TransportConfig{Type: ib.Transport}
		switch ib.Transport {
		case "grpc":
			sb.Transport.ServiceName = ib.ServiceName
		case "httpupgrade", "ws", "xhttp":
			sb.Transport.Path = ib.ServiceName
			if ib.Transport == "xhttp" {
				sb.Transport.Mode = "auto"
			}
		}
	}

	// Multiplex
	if ib.Multiplex {
		sb.Multiplex = &MultiplexConfig{Enabled: true}
	}

	return sb
}

func GenerateAndReload() error {
	var users []database.User
	database.DB.Where("status = ?", "active").Find(&users)

	// Load enabled inbound configs from DB
	var inbounds []database.InboundConfig
	database.DB.Where("enabled = ?", true).Order("sort_order").Find(&inbounds)

	singboxInbounds := []SingboxInbound{}
	inboundTags := []string{}
	for _, ib := range inbounds {
		singboxInbounds = append(singboxInbounds, buildSingboxInbound(ib, users))
		inboundTags = append(inboundTags, ib.Tag)
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
					Inbounds: inboundTags,
					Users:    buildUserNames(users),
				},
			},
		},
		Inbounds: singboxInbounds,
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

// GenerateLinkForInbound generates a subscription link for a given inbound config
func GenerateLinkForInbound(ib database.InboundConfig, user database.User, serverAddr string) string {
	if ib.ServerAddress != "" {
		serverAddr = ib.ServerAddress
	}

	fingerprint := ib.Fingerprint
	if fingerprint == "" {
		fingerprint = "random"
	}

	switch ib.Protocol {
	case "vless":
		v := url.Values{}
		v.Add("encryption", "none")
		v.Add("fp", fingerprint)

		if ib.TLSType == "reality" {
			v.Add("security", "reality")
			v.Add("pbk", ib.RealityPublicKey)
			v.Add("sni", ib.SNI)
			if len(ib.RealityShortIDs) > 0 {
				v.Add("sid", ib.RealityShortIDs[0])
			}
		}

		if ib.Flow != "" {
			v.Add("flow", ib.Flow)
		}

		switch ib.Transport {
		case "http":
			v.Add("type", "http")
		case "grpc":
			v.Add("type", "grpc")
			if ib.ServiceName != "" {
				v.Add("serviceName", ib.ServiceName)
			}
		case "httpupgrade":
			v.Add("type", "httpupgrade")
			if ib.ServiceName != "" {
				v.Add("path", ib.ServiceName)
			}
		case "ws":
			v.Add("type", "ws")
			if ib.ServiceName != "" {
				v.Add("path", ib.ServiceName)
			}
		case "xhttp":
			v.Add("type", "xhttp")
			if ib.ServiceName != "" {
				v.Add("path", ib.ServiceName)
			}
			v.Add("mode", "auto")
		default:
			v.Add("type", "tcp")
		}

		fragment := url.QueryEscape(ib.DisplayName + "-" + user.Username)
		return fmt.Sprintf("vless://%s@%s:%d?%s#%s",
			user.UUID, serverAddr, ib.ListenPort, v.Encode(), fragment)

	case "hysteria2":
		v := url.Values{}
		v.Add("insecure", "1")

		fragment := url.QueryEscape(ib.DisplayName + "-" + user.Username)
		return fmt.Sprintf("hysteria2://%s@%s:%d?%s#%s",
			user.UUID, serverAddr, ib.ListenPort, v.Encode(), fragment)
	}

	return ""
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

func ValidateRealitySNI(domain string) bool {
	_, err := net.LookupHost(domain)
	if err != nil {
		return false
	}

	conn, err := net.DialTimeout("tcp", domain+":443", 3*time.Second)
	if err != nil {
		return false
	}
	defer conn.Close()

	tlsConn := tls.Client(conn, &tls.Config{
		ServerName:         domain,
		InsecureSkipVerify: true,
	})
	defer tlsConn.Close()

	tlsConn.SetDeadline(time.Now().Add(3 * time.Second))
	err = tlsConn.Handshake()
	return err == nil
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
			err := database.DB.Transaction(func(tx *gorm.DB) error {
				var count int64
				tx.Model(&database.User{}).Where("username = ?", username).Count(&count)
				if count == 0 {
					return nil
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
