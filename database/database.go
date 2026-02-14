package database

import (
	"log"
	"time"

	"github.com/google/uuid"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

var DB *gorm.DB

// --- Models ---

type User struct {
	ID        uint           `gorm:"primaryKey" json:"id"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`

	UUID             string `gorm:"uniqueIndex;not null" json:"uuid"`
	Username         string `gorm:"uniqueIndex" json:"username"`    // Техническое имя для VLESS (user_123)
	TelegramUsername string `gorm:"index" json:"telegram_username"` // Реальный ник в Телеграм (@nick)
	TelegramID       int64  `gorm:"index" json:"telegram_id"`       // 0 если создан вручную

	Status string `gorm:"default:'active'" json:"status"` // active, banned, expired

	// Трафик
	TrafficLimit int64 `json:"traffic_limit"` // Байт. 0 = безлимит
	TrafficUsed  int64 `json:"traffic_used"`  // Байт.

	// Подписка
	ExpiryDate        *time.Time `json:"expiry_date"`
	SubscriptionToken string     `gorm:"uniqueIndex" json:"subscription_token"`
}

type SystemSettings struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	UpdatedAt time.Time `json:"updated_at"`

	ListenPort int `json:"listen_port"`

	// Reality
	RealityPrivateKey string `json:"reality_private_key"`
	RealityPublicKey  string `json:"reality_public_key"` // Нужно заполнить для ссылок!
	RealityShortIDs   string `json:"reality_short_ids"`
	ServerName        string `json:"server_name"`
	DestAddr          string `json:"dest_addr"`
	ServerDomain      string `json:"server_domain"`
	BypassDomain      string `json:"bypass_domain"`
	GrpcServerName    string `json:"grpc_server_name"`
	AlternativeSNIs   string `json:"alternative_snis"`
	Fingerprint       string `json:"fingerprint"`
}

type ConnectionLog struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	UserID    uint      `gorm:"index" json:"user_id"`
	ClientIP  string    `json:"client_ip"`
	Timestamp time.Time `gorm:"index" json:"timestamp"`
	Reason    string    `json:"reason"`
}

type InboundConfig struct {
	ID        uint           `gorm:"primaryKey" json:"id"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`

	Tag         string `gorm:"uniqueIndex;not null" json:"tag"`
	DisplayName string `json:"display_name"`
	Protocol    string `json:"protocol"`    // "vless" | "hysteria2"
	ListenPort  int    `json:"listen_port"` // 0 = settings.ListenPort
	TLSType     string `json:"tls_type"`    // "reality" | "certificate"
	SNI         string `json:"sni"`         // "" = fallback from settings
	CertPath    string `json:"cert_path"`
	KeyPath     string `json:"key_path"`
	Transport   string `json:"transport"`    // "" (tcp) | "http" | "grpc"
	ServiceName string `json:"service_name"`
	UserType    string `json:"user_type"` // "legacy" | "new" | "hy2"
	Flow        string `json:"flow"`      // "xtls-rprx-vision" | ""
	Multiplex   bool   `json:"multiplex"`
	Enabled     bool   `gorm:"default:true" json:"enabled"`
	IsBuiltin   bool   `gorm:"default:false" json:"is_builtin"`
	SortOrder   int    `gorm:"default:0" json:"sort_order"`
}

// --- Init ---

func Init(path string) {
	var err error
	DB, err = gorm.Open(sqlite.Open(path), &gorm.Config{})
	if err != nil {
		log.Fatal("Failed to connect to database:", err)
	}

	// Миграция схемы
	// GORM автоматически добавит новую колонку, если её нет
	err = DB.AutoMigrate(&User{}, &SystemSettings{}, &ConnectionLog{}, &InboundConfig{})
	if err != nil {
		log.Fatal("Migration failed:", err)
	}

	// 1. Инициализация настроек (из твоего конфига)
	var settings SystemSettings
	if result := DB.First(&settings); result.Error != nil {
		log.Println("Settings not found, initializing from config...")
		// WARNING: These are default dev values. Override via admin panel or DB.
		DB.Create(&SystemSettings{
			ListenPort:        8443,
			RealityPrivateKey: "ONHN91OWFGFycHogYJY4X5i-Xn1qUs917dWIqnx4K04",
			RealityPublicKey:  "BgLsjp3u0Mjk3BqLs7kopcAOF6KOyx14lxHlP7e_yxo",
			RealityShortIDs:   `["207fc82a9f9e741f"]`,
			ServerName:        "rbc.ru",
			DestAddr:          "rbc.ru:443",
			ServerDomain:      "",
			BypassDomain:      "",
			GrpcServerName:    "tradingview.com",
			AlternativeSNIs:   `["rbc.ru","tradingview.com","sun6-21.userapi.com"]`,
			Fingerprint:       "random",
		})
	}

	// 2. Инициализация твоего существующего юзера MRiaz
	var oldUser User
	if result := DB.Where("username = ?", "MRiaz").First(&oldUser); result.Error != nil {
		log.Println("Restoring user MRiaz...")
		DB.Create(&User{
			UUID:              "15986646-9dd8-45b8-b6d4-5c0cf9c8b784",
			Username:          "MRiaz",
			TelegramUsername:  "MRiaz", // Добавляем вручную для админа
			Status:            "active",
			TrafficLimit:      0, // Безлимит для админа
			SubscriptionToken: GenerateToken(),
		})
	}

	// 3. Seed builtin inbound configs
	var inboundCount int64
	DB.Model(&InboundConfig{}).Count(&inboundCount)
	if inboundCount == 0 {
		log.Println("Seeding builtin inbound configs...")
		builtins := []InboundConfig{
			{
				Tag:         "vless-in",
				DisplayName: "VLESS Reality (TCP)",
				Protocol:    "vless",
				ListenPort:  0, // uses settings.ListenPort
				TLSType:     "reality",
				SNI:         "", // uses settings.ServerName
				Transport:   "",
				UserType:    "legacy",
				Flow:        "xtls-rprx-vision",
				Multiplex:   false,
				Enabled:     true,
				IsBuiltin:   true,
				SortOrder:   0,
			},
			{
				Tag:         "vless-in-h2",
				DisplayName: "VLESS Reality (HTTP/2)",
				Protocol:    "vless",
				ListenPort:  2053,
				TLSType:     "reality",
				SNI:         "api.yandex.ru",
				Transport:   "http",
				UserType:    "new",
				Flow:        "",
				Multiplex:   true,
				Enabled:     true,
				IsBuiltin:   true,
				SortOrder:   1,
			},
			{
				Tag:         "hy2-in",
				DisplayName: "Hysteria2",
				Protocol:    "hysteria2",
				ListenPort:  2055,
				TLSType:     "certificate",
				CertPath:    "/etc/sing-box/hy2-cert.pem",
				KeyPath:     "/etc/sing-box/hy2-key.pem",
				Transport:   "",
				UserType:    "hy2",
				Flow:        "",
				Multiplex:   false,
				Enabled:     true,
				IsBuiltin:   true,
				SortOrder:   2,
			},
			{
				Tag:         "vless-in-grpc",
				DisplayName: "VLESS Reality (gRPC)",
				Protocol:    "vless",
				ListenPort:  2054,
				TLSType:     "reality",
				SNI:         "", // uses settings.GrpcServerName
				Transport:   "grpc",
				ServiceName: "grpc-vpn",
				UserType:    "new",
				Flow:        "",
				Multiplex:   false,
				Enabled:     true,
				IsBuiltin:   true,
				SortOrder:   3,
			},
		}
		for _, ib := range builtins {
			DB.Create(&ib)
		}
	}
}

// Helper: Создать токен
func GenerateToken() string {
	return uuid.New().String()
}
