package database

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// JSONStringArray хранится в SQLite как JSON-строка, но сериализуется в JSON как []string
type JSONStringArray []string

func (a JSONStringArray) Value() (driver.Value, error) {
	if a == nil {
		return "[]", nil
	}
	b, err := json.Marshal(a)
	return string(b), err
}

func (a *JSONStringArray) Scan(value interface{}) error {
	if value == nil {
		*a = []string{}
		return nil
	}
	s, ok := value.(string)
	if !ok {
		return fmt.Errorf("JSONStringArray: expected string, got %T", value)
	}
	return json.Unmarshal([]byte(s), a)
}

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
	ListenPort  int    `json:"listen_port"`
	TLSType     string `json:"tls_type"`    // "reality" | "certificate"
	SNI         string `json:"sni"`
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

	ServerAddress string `json:"server_address"` // Адрес для ссылок (домен или IP). Пусто = SERVER_IP

	// Reality keys (per-inbound)
	RealityPrivateKey string          `json:"reality_private_key"`
	RealityPublicKey  string          `json:"reality_public_key"`
	RealityShortIDs   JSONStringArray `json:"reality_short_ids" gorm:"type:text"`
	Fingerprint       string          `json:"fingerprint"`
}

// --- Init ---

func Init(path string) {
	var err error
	DB, err = gorm.Open(sqlite.Open(path), &gorm.Config{})
	if err != nil {
		log.Fatal("Failed to connect to database:", err)
	}

	// Миграция схемы
	err = DB.AutoMigrate(&User{}, &ConnectionLog{}, &InboundConfig{})
	if err != nil {
		log.Fatal("Migration failed:", err)
	}

	// Одноразовая миграция: перенос Reality-ключей из system_settings в inbound_configs
	migrateRealityKeysFromSettings()

	// 1. Инициализация твоего существующего юзера MRiaz
	var oldUser User
	if result := DB.Where("username = ?", "MRiaz").First(&oldUser); result.Error != nil {
		log.Println("Restoring user MRiaz...")
		DB.Create(&User{
			UUID:              "15986646-9dd8-45b8-b6d4-5c0cf9c8b784",
			Username:          "MRiaz",
			TelegramUsername:  "MRiaz",
			Status:            "active",
			TrafficLimit:      0,
			SubscriptionToken: GenerateToken(),
		})
	}

	// 2. Seed builtin inbound configs
	var inboundCount int64
	DB.Model(&InboundConfig{}).Count(&inboundCount)
	if inboundCount == 0 {
		log.Println("Seeding builtin inbound configs...")
		builtins := []InboundConfig{
			{
				Tag:               "vless-in",
				DisplayName:       "VLESS Reality (TCP)",
				Protocol:          "vless",
				ListenPort:        8444,
				TLSType:           "reality",
				SNI:               "rbc.ru",
				Transport:         "",
				UserType:          "legacy",
				Flow:              "xtls-rprx-vision",
				Multiplex:         false,
				Enabled:           true,
				IsBuiltin:         true,
				SortOrder:         0,
				RealityPrivateKey: "ONHN91OWFGFycHogYJY4X5i-Xn1qUs917dWIqnx4K04",
				RealityPublicKey:  "BgLsjp3u0Mjk3BqLs7kopcAOF6KOyx14lxHlP7e_yxo",
				RealityShortIDs:   JSONStringArray{"207fc82a9f9e741f"},
				Fingerprint:       "random",
			},
			{
				Tag:               "vless-in-h2",
				DisplayName:       "VLESS Reality (HTTP/2)",
				Protocol:          "vless",
				ListenPort:        2053,
				TLSType:           "reality",
				SNI:               "api.yandex.ru",
				Transport:         "http",
				UserType:          "new",
				Flow:              "",
				Multiplex:         true,
				Enabled:           true,
				IsBuiltin:         true,
				SortOrder:         1,
				RealityPrivateKey: "ONHN91OWFGFycHogYJY4X5i-Xn1qUs917dWIqnx4K04",
				RealityPublicKey:  "BgLsjp3u0Mjk3BqLs7kopcAOF6KOyx14lxHlP7e_yxo",
				RealityShortIDs:   JSONStringArray{"207fc82a9f9e741f"},
				Fingerprint:       "random",
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
				Tag:               "vless-in-grpc",
				DisplayName:       "VLESS Reality (gRPC)",
				Protocol:          "vless",
				ListenPort:        2054,
				TLSType:           "reality",
				SNI:               "tradingview.com",
				Transport:         "grpc",
				ServiceName:       "grpc-vpn",
				UserType:          "new",
				Flow:              "",
				Multiplex:         false,
				Enabled:           true,
				IsBuiltin:         true,
				SortOrder:         3,
				RealityPrivateKey: "ONHN91OWFGFycHogYJY4X5i-Xn1qUs917dWIqnx4K04",
				RealityPublicKey:  "BgLsjp3u0Mjk3BqLs7kopcAOF6KOyx14lxHlP7e_yxo",
				RealityShortIDs:   JSONStringArray{"207fc82a9f9e741f"},
				Fingerprint:       "random",
			},
		}
		for _, ib := range builtins {
			DB.Create(&ib)
		}
	}
}

// migrateRealityKeysFromSettings копирует Reality-ключи из таблицы system_settings
// в Reality-инбаунды, у которых ключи ещё не заполнены.
func migrateRealityKeysFromSettings() {
	// Проверяем, существует ли таблица system_settings
	if !DB.Migrator().HasTable("system_settings") {
		return
	}

	// Проверяем, есть ли Reality-инбаунды с пустым приватным ключом
	var count int64
	DB.Model(&InboundConfig{}).Where("tls_type = ? AND reality_private_key = ''", "reality").Count(&count)
	if count == 0 {
		return
	}

	// Читаем ключи из system_settings
	var result struct {
		RealityPrivateKey string
		RealityPublicKey  string
		RealityShortIDs   string
		Fingerprint       string
	}
	if err := DB.Table("system_settings").First(&result).Error; err != nil {
		log.Println("Migration: system_settings not found, skipping key migration")
		return
	}

	// Парсим short IDs
	var shortIDs JSONStringArray
	if err := json.Unmarshal([]byte(result.RealityShortIDs), &shortIDs); err != nil {
		shortIDs = JSONStringArray{"207fc82a9f9e741f"}
	}

	fingerprint := result.Fingerprint
	if fingerprint == "" {
		fingerprint = "random"
	}

	log.Println("Migration: copying Reality keys from system_settings to inbound_configs...")
	DB.Model(&InboundConfig{}).
		Where("tls_type = ? AND reality_private_key = ''", "reality").
		Updates(map[string]interface{}{
			"reality_private_key": result.RealityPrivateKey,
			"reality_public_key":  result.RealityPublicKey,
			"reality_short_ids":   shortIDs,
			"fingerprint":         fingerprint,
		})
}

// Helper: Создать токен
func GenerateToken() string {
	return uuid.New().String()
}
