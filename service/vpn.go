package service

import (
	"context"
	"crypto/tls"
	"encoding/base64"
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
	Inbounds     []any               `json:"inbounds"`
	Outbounds    []any               `json:"outbounds"`
	Route        *RouteConfig        `json:"route,omitempty"`
}

type RouteConfig struct {
	Rules []RouteRule `json:"rules,omitempty"`
	Final string      `json:"final,omitempty"`
}

type RouteRule struct {
	Inbound      []string `json:"inbound,omitempty"`
	DomainSuffix []string `json:"domain_suffix,omitempty"`
	IPCIDR       []string `json:"ip_cidr,omitempty"`
	Outbound     string   `json:"outbound"`
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
	Enabled    bool `json:"enabled"`
	Padding    bool `json:"padding,omitempty"`
	MaxStreams int  `json:"max_streams,omitempty"`
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
	// Поля для type="wireguard" (sing-box userspace WG outbound)
	Server        string   `json:"server,omitempty"`
	ServerPort    int      `json:"server_port,omitempty"`
	LocalAddress  []string `json:"local_address,omitempty"`
	PrivateKey    string   `json:"private_key,omitempty"`
	PeerPublicKey string   `json:"peer_public_key,omitempty"`
	MTU           int      `json:"mtu,omitempty"`
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

// buildInboundGroup возвращает 1+ sing-box inbound-объектов для одной DB-записи.
// Для vless/hysteria2 — 1 элемент (типизированный SingboxInbound).
// Для shadowtls — 2 элемента (shadowtls + inner shadowsocks).
func buildInboundGroup(ib database.InboundConfig, users []database.User) []any {
	if ib.Protocol == "shadowtls" {
		return buildShadowTLSGroup(ib, users)
	}
	return []any{buildSingboxInbound(ib, users)}
}

// buildShadowTLSGroup строит пару shadowtls+shadowsocks для одного inbound.
// ShadowTLS сам по себе не даёт proxy — оборачивает inner shadowsocks на loopback.
func buildShadowTLSGroup(ib database.InboundConfig, users []database.User) []any {
	innerTag := "ss-inner-" + ib.Tag

	// shadowtls-users: общий пароль внешней инкапсуляции.
	stlsUsers := []map[string]any{
		{"name": "default", "password": ib.ShadowTLSPassword},
	}
	shadowtls := map[string]any{
		"type":        "shadowtls",
		"tag":         ib.Tag,
		"listen":      "::",
		"listen_port": ib.ListenPort,
		"version":     ib.ShadowTLSVersion,
		"users":       stlsUsers,
		"handshake": map[string]any{
			"server":      ib.CoverDomain,
			"server_port": 443,
		},
		"detour": innerTag,
	}

	// shadowsocks-users: per-user (UUID как pre-shared уникальный per-user password).
	// Note: InnerPassword — общий inbound-password. Per-user password добавлен для future extension.
	innerUsers := []map[string]any{}
	for _, u := range users {
		innerUsers = append(innerUsers, map[string]any{
			"name":     u.Username,
			"password": ib.InnerPassword,
		})
	}
	shadowsocks := map[string]any{
		"type":        "shadowsocks",
		"tag":         innerTag,
		"listen":      "127.0.0.1",
		"listen_port": 0,
		"method":      ib.InnerMethod,
		"password":    ib.InnerPassword,
		"users":       innerUsers,
	}

	return []any{shadowtls, shadowsocks}
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
		sb.Multiplex = &MultiplexConfig{
			Enabled:    true,
			Padding:    ib.MuxPadding,
			MaxStreams: ib.MuxMaxStreams,
		}
	}

	return sb
}

// loadExtraOutbound читает JSON-файл, указанный в EXTRA_OUTBOUND_JSON_PATH.
// Возвращает (nil, nil) если env не задан. Возвращает ошибку если файл указан, но нечитаем.
func loadExtraOutbound() (map[string]any, error) {
	path := os.Getenv("EXTRA_OUTBOUND_JSON_PATH")
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read extra outbound: %w", err)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("parse extra outbound: %w", err)
	}
	return out, nil
}

// buildSingBoxConfig строит sing-box конфиг из inbound'ов + пользователей.
// extraOutbound (nil ok) инжектится в начало outbounds.
// finalTag (пусто ok) — тег route.final; если пусто, используется "direct".
func buildSingBoxConfig(inbounds []database.InboundConfig, users []database.User, extraOutbound map[string]any, finalTag string) SingBoxConfig {
	singboxInbounds := []any{}
	inboundTags := []string{}
	perInboundRules := []RouteRule{}
	for _, ib := range inbounds {
		group := buildInboundGroup(ib, users)
		singboxInbounds = append(singboxInbounds, group...)
		inboundTags = append(inboundTags, ib.Tag)
		if ib.ExitOutbound != "" {
			perInboundRules = append(perInboundRules, RouteRule{
				Inbound:  []string{ib.Tag},
				Outbound: ib.ExitOutbound,
			})
		}
	}

	outbounds := []any{}
	if extraOutbound != nil {
		outbounds = append(outbounds, extraOutbound)
	}
	outbounds = append(outbounds,
		OutboundConfig{Type: "direct", Tag: "direct"},
		OutboundConfig{Type: "block", Tag: "block"},
	)

	if finalTag == "" {
		finalTag = "direct"
	}

	bogonRule := RouteRule{
		IPCIDR: []string{
			"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16",
			"169.254.0.0/16", "224.0.0.0/4", "240.0.0.0/4",
			"0.0.0.0/8", "100.64.0.0/10",
			"fc00::/7", "fe80::/10", "ff00::/8", "::/128",
		},
		Outbound: "block",
	}
	rules := append(perInboundRules, bogonRule)

	return SingBoxConfig{
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
		Inbounds:  singboxInbounds,
		Outbounds: outbounds,
		Route: &RouteConfig{
			Rules: rules,
			Final: finalTag,
		},
	}
}

func GenerateAndReload() error {
	var users []database.User
	database.DB.Where("status = ?", "active").Find(&users)

	var inbounds []database.InboundConfig
	database.DB.Where("enabled = ?", true).Order("sort_order").Find(&inbounds)

	extraOutbound, err := loadExtraOutbound()
	if err != nil {
		log.Println("Warning: extra outbound not loaded:", err)
	}
	finalTag := os.Getenv("ROUTE_FINAL")

	cfg := buildSingBoxConfig(inbounds, users, extraOutbound, finalTag)

	file, _ := json.MarshalIndent(cfg, "", "  ")

	hetznerErr := os.WriteFile(ConfigPath, file, 0644)
	if hetznerErr != nil {
		log.Println("Error writing Hetzner config:", hetznerErr)
		fmt.Println(string(file))
	} else {
		if rerr := ReloadService(); rerr != nil {
			log.Println("Hetzner sing-box reload error:", rerr)
		}
	}

	// Зеркало конфига на RuVDS (если WG включён)
	if IsRuVDSEnabled() {
		if rerr := GenerateAndReloadRuVDS(); rerr != nil {
			log.Println("RuVDS sing-box reload error:", rerr)
		}
	}
	return hetznerErr
}

// GenerateRuVDSConfig строит JSON-конфиг sing-box для RuVDS-фронта:
// те же inbounds что и на Hetzner + WireGuard outbound на Hetzner.
// Маршрутизация: всё по умолчанию через wg-out (egress = Hetzner public IP);
// inbound'ы с ExitOutbound="direct" выходят локально с RuVDS-IP.
func GenerateRuVDSConfig() ([]byte, error) {
	wgCfg, err := GetWireGuardConfig()
	if err != nil {
		return nil, fmt.Errorf("WG config не найден: %w", err)
	}
	if !wgCfg.Enabled {
		return nil, fmt.Errorf("WireGuard выключен")
	}
	if wgCfg.RuVDSPrivateKey == "" || wgCfg.HetznerPublicKey == "" {
		return nil, fmt.Errorf("WG-ключи не сгенерированы — запустите SetupWireGuard сначала")
	}

	var users []database.User
	database.DB.Where("status = ?", "active").Find(&users)

	var inbounds []database.InboundConfig
	database.DB.Where("enabled = ?", true).Order("sort_order").Find(&inbounds)

	singboxInbounds := []any{}
	perInboundRules := []RouteRule{}
	for _, ib := range inbounds {
		group := buildInboundGroup(ib, users)
		singboxInbounds = append(singboxInbounds, group...)
		if ib.ExitOutbound != "" {
			perInboundRules = append(perInboundRules, RouteRule{
				Inbound:  []string{ib.Tag},
				Outbound: ib.ExitOutbound,
			})
		}
	}

	hetznerEndpoint := GetHetznerServerIP()
	wgOutbound := BuildWireguardOutbound(wgCfg, hetznerEndpoint)

	cfg := SingBoxConfig{
		Log: LogConfig{
			Level:     "info",
			Timestamp: true,
			Output:    "/etc/sing-box/access.log",
		},
		Inbounds: singboxInbounds,
		Outbounds: []any{
			wgOutbound,
			OutboundConfig{Type: "direct", Tag: "direct"},
			OutboundConfig{Type: "block", Tag: "block"},
		},
		Route: &RouteConfig{
			Rules: perInboundRules,
			Final: "wg-out",
		},
	}
	return json.MarshalIndent(cfg, "", "  ")
}

// GenerateAndReloadRuVDS — перегенерация + деплой config.json на RuVDS через SSH.
func GenerateAndReloadRuVDS() error {
	if !IsRuVDSEnabled() {
		return nil
	}
	cfgJSON, err := GenerateRuVDSConfig()
	if err != nil {
		return err
	}
	return DeploySingboxConfigRuVDS(cfgJSON)
}

// GenerateLinkForInbound generates a subscription link for a given inbound config
func GenerateLinkForInbound(ib database.InboundConfig, user database.User, serverAddr string) string {
	if ib.ServerAddress != "" {
		serverAddr = ib.ServerAddress
	}

	if ib.Protocol == "shadowtls" {
		return generateShadowTLSLink(ib, user, serverAddr)
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

// generateShadowTLSLink returns a Shadowsocks SIP002 URI with the shadow-tls
// plugin, which Hiddify / Nekoray / v2rayN / sing-box all parse.
// Format:
//
//	ss://<b64url(method:inner_password)>@server:port/?plugin=shadow-tls%3Bversion%3D3%3Bhost%3D...%3Bpassword%3D...#tag
func generateShadowTLSLink(ib database.InboundConfig, _ database.User, serverAddr string) string {
	userInfo := ib.InnerMethod + ":" + ib.InnerPassword
	b64 := base64.URLEncoding.WithPadding(base64.NoPadding).EncodeToString([]byte(userInfo))

	pluginOpts := fmt.Sprintf(
		"shadow-tls;version=%d;host=%s;password=%s",
		ib.ShadowTLSVersion,
		ib.CoverDomain,
		ib.ShadowTLSPassword,
	)
	q := url.Values{}
	q.Set("plugin", pluginOpts)

	u := url.URL{
		Scheme:   "ss",
		User:     url.User(b64),
		Host:     fmt.Sprintf("%s:%d", serverAddr, ib.ListenPort),
		Path:     "/",
		RawQuery: q.Encode(),
		Fragment: ib.Tag,
	}
	return u.String()
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
				GenerateAndReloadTelemet()
			}
		}
	}
}
