package database

import (
	"crypto/ecdh"
	"crypto/rand"
	"database/sql/driver"
	"encoding/base64"
	"encoding/hex"
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

// TrafficDaily — трафик пользователя за сутки (MSK) на одном сервере.
// «За месяц» и история считаются агрегацией отсюда; users.traffic_used —
// накопительный счётчик за всё время (по нему работает лимит).
type TrafficDaily struct {
	ID       uint   `gorm:"primaryKey" json:"id"`
	UserID   uint   `gorm:"uniqueIndex:idx_td_user_day_srv" json:"user_id"`
	Day      string `gorm:"uniqueIndex:idx_td_user_day_srv;size:10" json:"day"`    // "2026-09-23"
	Server   string `gorm:"uniqueIndex:idx_td_user_day_srv;size:16" json:"server"` // "hetzner" | "ruvds"
	Upload   int64  `json:"upload"`
	Download int64  `json:"download"`
}

// InboundTrafficDaily — трафик подключения (inbound tag) за сутки на сервере.
// Нужен для анализа доступа: какие протоколы реально несут трафик.
type InboundTrafficDaily struct {
	ID       uint   `gorm:"primaryKey" json:"id"`
	Tag      string `gorm:"uniqueIndex:idx_itd_tag_day_srv;size:64" json:"tag"`
	Day      string `gorm:"uniqueIndex:idx_itd_tag_day_srv;size:10" json:"day"`
	Server   string `gorm:"uniqueIndex:idx_itd_tag_day_srv;size:16" json:"server"`
	Upload   int64  `json:"upload"`
	Download int64  `json:"download"`
}

type ConnectionLog struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	UserID    uint      `gorm:"index" json:"user_id"`
	ClientIP  string    `json:"client_ip"`
	Timestamp time.Time `gorm:"index" json:"timestamp"`
	Reason    string    `json:"reason"`
}

// TelemetConfig — настройки MTProto прокси (синглтон, одна запись)
type TelemetConfig struct {
	ID        uint           `gorm:"primaryKey" json:"id"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`

	Enabled bool `gorm:"default:false" json:"enabled"`
	// Port — порт, который telemt реально слушает на Hetzner.
	Port int `gorm:"default:9443" json:"port"`
	// LinkPort — порт, который попадает в ссылку tg://proxy. Ноль = брать Port.
	//
	// Разведено с Port намеренно. На RuVDS перед telemt стоит nginx с
	// ssl_preread: он принимает :443 и по SNI lk.rt.ru отправляет соединение
	// в тот же туннель, что и :9443. Секрет и tls_domain одни и те же, поэтому
	// оба входа равнозначны — но 443 куда охотнее пропускают мобильные
	// операторы, чем нестандартный 9443. Раньше поле было одно, и попытка
	// выдать пользователям 443 заставила бы telemt занять 443 на Hetzner,
	// где уже сидит sing-box.
	LinkPort      int    `gorm:"default:0" json:"link_port"`
	TLSDomain     string `json:"tls_domain"`
	ServerAddress string `json:"server_address"`                     // IP/домен для ссылок. Пусто = SERVER_IP
	ProxyTag      string `json:"proxy_tag"`                          // proxy tag от @MTProxyBot (32 hex chars)
	RuVDSEnabled  bool   `gorm:"default:false" json:"ruvds_enabled"` // Запустить telemt на RuVDS через SSH

	// ClassicEnabled / SecureEnabled — дополнительные режимы MTProto рядом с FakeTLS.
	//
	// Заводились как диагностика: у абонентов МегаФона FakeTLS-поток на наш адрес
	// умирал (TCP проходит, ClientHello уходит, дальше тишина и таймаут telemt через
	// минуту), и надо было понять, режут домен lk.rt.ru или сам адрес. Classic не
	// несёт ни TLS, ни SNI — то есть маскировать в нём нечего.
	//
	// Результат опыта 03.08.2026: у абонента МегаФона не заработал и classic, при
	// том что с того же телефона по Wi-Fi работают все три режима, а у абонентов
	// МТС, Tele2 и МГТС всё проходит и через мобильный. Значит МегаФон смотрит на
	// адрес назначения, а не на содержимое, и подменой SNI это не лечится. Из чужих
	// прокси у того же человека работает только hlebushek — а он стоит в Yandex
	// Cloud, то есть внутри периметра, который МегаФон пропускает.
	//
	// Сами режимы оставлены: они рабочие, дают запасной вход остальным операторам и
	// стоят одного флага. Но помнить, что classic не маскирован вовсе.
	ClassicEnabled bool `gorm:"default:false" json:"classic_enabled"`
	SecureEnabled  bool `gorm:"default:false" json:"secure_enabled"`
	// AltPort — порт в ссылках classic/secure. Ноль = брать Port.
	//
	// Отдельно от LinkPort, потому что 443 им не подходит: на RuVDS его слушает
	// nginx с ssl_preread, а у classic/secure нет ClientHello — nginx не увидит SNI
	// и отправит соединение на сайт-приманку. Им нужен вход без разбора имени,
	// то есть 9443 с его nft redirect в туннель.
	AltPort int `gorm:"default:0" json:"alt_port"`

	// AltTLSDomain — имя FakeTLS для второго инстанса telemt. Пусто = инстанс выключен.
	//
	// Смысл в том, чтобы подставлять клиенту НАШ сертификат на НАШЕ имя, а не
	// эмулировать чужой домен. Проверено 03.08.2026 на одном телефоне МегаФона
	// в течение одной минуты: вход на lk.rt.ru умирал за 0.3 с, отдав 206 байт,
	// а вход на cdn.moskva.live за две минуты прокачал больше трёх мегабайт.
	//
	// Объяснение: эмуляция lk.rt.ru у нас честная, с настоящим сертификатом
	// Ростелекома — и именно поэтому подозрительная. Настоящий сертификат чужого
	// домена, отданный с адреса, которому этот домен не принадлежит, выглядит как
	// MITM против него. А cdn.moskva.live указывает на наш же адрес, сертификат
	// на него наш собственный, и ответ совпадает с ответом сайта-приманки,
	// который у того же абонента открывался всё это время.
	AltTLSDomain string `json:"alt_tls_domain"`
	// AltInstancePort — порт второго инстанса на Hetzner. Ноль = 19443.
	// Слушает только loopback: снаружи в него ведёт туннель с RuVDS.
	AltInstancePort int `gorm:"default:0" json:"alt_instance_port"`
}

// HealthConfig — настройки HEALTH ALARM (синглтон, как TelemetConfig).
type HealthConfig struct {
	ID        uint           `gorm:"primaryKey" json:"id"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`

	Enabled        bool `gorm:"default:true" json:"enabled"`
	IntervalSec    int  `gorm:"default:60" json:"interval_sec"`
	DownHysteresis int  `gorm:"default:2" json:"down_hysteresis"`
}

// WireGuardConfig — настройки WG-туннеля RuVDS → Hetzner (синглтон).
// На Hetzner крутится kernel WG (wg-quick@wg0), на RuVDS — userspace WG
// внутри sing-box (тип outbound "wireguard"), kernel WG на RuVDS не нужен.
type WireGuardConfig struct {
	ID        uint           `gorm:"primaryKey" json:"id"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`

	Enabled           bool   `gorm:"default:false" json:"enabled"`
	HetznerPublicKey  string `json:"hetzner_public_key"`
	HetznerPrivateKey string `json:"-"` // не выдаём в JSON
	RuVDSPublicKey    string `json:"ruvds_public_key"`
	RuVDSPrivateKey   string `json:"-"` // не выдаём в JSON
	ListenPort        int    `gorm:"default:51820" json:"listen_port"`
	HetznerWGIP       string `gorm:"default:'10.8.0.1'" json:"hetzner_wg_ip"`
	RuVDSWGIP         string `gorm:"default:'10.8.0.2'" json:"ruvds_wg_ip"`
	MTU               int    `gorm:"default:1408" json:"mtu"`
	Status            string `gorm:"default:'inactive'" json:"status"`
	StatusMsg         string `json:"status_message"`
}

// TelemetUser — секрет пользователя для MTProto прокси
type TelemetUser struct {
	ID        uint           `gorm:"primaryKey" json:"id"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`

	TelemetConfigID uint   `gorm:"uniqueIndex:idx_telemet_user_config" json:"telemet_config_id"`
	UserID          uint   `gorm:"uniqueIndex:idx_telemet_user_config" json:"user_id"`
	Label           string `json:"label"`  // имя в TOML-конфиге (= user.Username)
	Secret          string `json:"secret"` // 32-hex секрет

	User User `gorm:"foreignKey:UserID" json:"user,omitempty"`
}

type InboundConfig struct {
	ID        uint           `gorm:"primaryKey" json:"id"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`

	Tag           string `gorm:"uniqueIndex;not null" json:"tag"`
	DisplayName   string `json:"display_name"`
	Protocol      string `json:"protocol"` // "vless" | "hysteria2" | "shadowtls" | "mask" | "xdns"
	ListenPort    int    `json:"listen_port"`
	TLSType       string `json:"tls_type"` // "reality" | "certificate"
	SNI           string `json:"sni"`
	CertPath      string `json:"cert_path"`
	KeyPath       string `json:"key_path"`
	Transport     string `json:"transport"` // "" (tcp) | "http" | "grpc"
	ServiceName   string `json:"service_name"`
	UserType      string `json:"user_type"` // "legacy" | "new" | "hy2"
	Flow          string `json:"flow"`      // "xtls-rprx-vision" | ""
	Multiplex     bool   `json:"multiplex"`
	MuxPadding    bool   `json:"mux_padding"`
	MuxMaxStreams int    `gorm:"default:0" json:"mux_max_streams"`
	Enabled       bool   `gorm:"default:true" json:"enabled"`
	IsBuiltin     bool   `gorm:"default:false" json:"is_builtin"`
	SortOrder     int    `gorm:"default:0" json:"sort_order"`
	ExitOutbound  string `json:"exit_outbound"` // "" (=route.final) | "direct" | "wg-out"

	ServerAddress string `json:"server_address"` // Адрес для ссылок (домен или IP). Пусто = SERVER_IP

	// Phase 2: если true, sing-box на RuVDS тоже слушает этот inbound (зеркало)
	RuVDSEnabled bool `gorm:"default:false" json:"ruvds_enabled"`

	// Reality keys (per-inbound)
	RealityPrivateKey string          `json:"reality_private_key"`
	RealityPublicKey  string          `json:"reality_public_key"`
	RealityShortIDs   JSONStringArray `json:"reality_short_ids" gorm:"type:text"`
	Fingerprint       string          `json:"fingerprint"`

	// ShadowTLS fields (Protocol="shadowtls")
	ShadowTLSPassword string `json:"shadowtls_password"`
	ShadowTLSVersion  int    `gorm:"default:0" json:"shadowtls_version"`
	CoverDomain       string `json:"cover_domain"`
	InnerMethod       string `json:"inner_method"`
	InnerPassword     string `json:"inner_password"`

	// Finalmask-обёртка (Protocol="mask"): Xray на RuVDS снимает маску и
	// форвардит голый поток в VLESS-инбаунд sing-box с тегом MaskInnerTag
	// на 127.0.0.1:<его ListenPort>. MaskJSON — блок streamSettings.finalmask
	// Xray как есть; он же уходит в ссылку параметром fm.
	MaskInnerTag string `json:"mask_inner_tag"`
	MaskJSON     string `gorm:"type:text" json:"mask_json"`

	// XDNS-канал (Protocol="xdns"): отдельный Xray на Hetzner, VLESS+mKCP,
	// данные внутри DNS-запросов. Домен зоны + клиентские резолверы + пара
	// VLESS-шифрования (генерится xray vlessenc при setup).
	XDNSDomain     string `json:"xdns_domain"`                      // напр. "t.edgn.net:txt"
	XDNSResolvers  string `json:"xdns_resolvers"`                   // список через запятую: "t.edgn.net:txt+udp://1.2.3.4:53,..."
	XDNSDecryption string `gorm:"type:text" json:"xdns_decryption"` // серверный ключ (decryption)
	XDNSEncryption string `gorm:"type:text" json:"xdns_encryption"` // клиентский ключ (encryption), в ссылку
}

// --- Init ---

func Init(path string) {
	var err error
	DB, err = gorm.Open(sqlite.Open(path), &gorm.Config{})
	if err != nil {
		log.Fatal("Failed to connect to database:", err)
	}

	// Миграция схемы
	err = DB.AutoMigrate(&User{}, &ConnectionLog{}, &InboundConfig{}, &TelemetConfig{}, &TelemetUser{}, &WireGuardConfig{}, &HealthConfig{}, &TrafficDaily{}, &InboundTrafficDaily{})
	if err != nil {
		log.Fatal("Migration failed:", err)
	}

	// Одноразовая миграция: перенос Reality-ключей из system_settings в inbound_configs
	migrateRealityKeysFromSettings()

	// Идемпотентная миграция: uTLS-фингерпринт random -> chrome
	migrateFingerprintToChrome()

	// 1. Инициализация твоего существующего юзера MRiaz
	var oldUser User
	if result := DB.Where("username = ?", "MRiaz").First(&oldUser); result.Error != nil {
		log.Println("Restoring user MRiaz...")
		DB.Create(&User{
			UUID:              uuid.New().String(),
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
		// Ключи Reality генерируются при первом запуске — в коде их быть не должно.
		realityPriv, realityPub, err := GenerateRealityKeypair()
		if err != nil {
			log.Fatal("Failed to generate Reality keypair:", err)
		}
		realityShortIDs := JSONStringArray{GenerateRealityShortID()}
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
				RealityPrivateKey: realityPriv,
				RealityPublicKey:  realityPub,
				RealityShortIDs:   realityShortIDs,
				Fingerprint:       "chrome",
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
				RealityPrivateKey: realityPriv,
				RealityPublicKey:  realityPub,
				RealityShortIDs:   realityShortIDs,
				Fingerprint:       "chrome",
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
				RealityPrivateKey: realityPriv,
				RealityPublicKey:  realityPub,
				RealityShortIDs:   realityShortIDs,
				Fingerprint:       "chrome",
			},
		}
		for _, ib := range builtins {
			DB.Create(&ib)
		}

		// Direct-exit inbounds (RuVDS выход) — не builtin, placeholder Reality keys.
		directExits := []InboundConfig{
			{
				Tag:         "vless-direct-xhttp",
				DisplayName: "VLESS Direct-Exit (xhttp)",
				Protocol:    "vless",
				ListenPort:  2059,
				TLSType:     "reality",
				SNI:         "yastatic.net",
				Transport:   "xhttp",
				UserType:    "new",
				Flow:        "",
				Multiplex:   true,
				MuxPadding:  true,
				// MuxMaxStreams is intentionally 0 — sing-box rejects max_streams
				// on VLESS inbound side (it's only valid on outbound/client-side mux).
				Enabled:           false, // отключены до заполнения ключей
				IsBuiltin:         false,
				SortOrder:         10,
				ExitOutbound:      "direct",
				RealityPrivateKey: "REPLACE_ME_VIA_API",
				RealityPublicKey:  "REPLACE_ME_VIA_API",
				RealityShortIDs:   JSONStringArray{"REPLACE_ME"},
				Fingerprint:       "chrome",
			},
			{
				Tag:               "vless-direct-tcp",
				DisplayName:       "VLESS Direct-Exit (tcp)",
				Protocol:          "vless",
				ListenPort:        2060,
				TLSType:           "reality",
				SNI:               "yastatic.net",
				Transport:         "",
				UserType:          "legacy",
				Flow:              "xtls-rprx-vision",
				Multiplex:         false,
				Enabled:           false, // отключены до заполнения ключей
				IsBuiltin:         false,
				SortOrder:         11,
				ExitOutbound:      "direct",
				RealityPrivateKey: "REPLACE_ME_VIA_API",
				RealityPublicKey:  "REPLACE_ME_VIA_API",
				RealityShortIDs:   JSONStringArray{"REPLACE_ME"},
				Fingerprint:       "chrome",
			},
		}
		for _, ib := range directExits {
			DB.Create(&ib)
		}

		// ShadowTLS v3 direct-exit inbound — disabled до задания секретов через API.
		shadowtlsSeed := InboundConfig{
			Tag:               "RU-STLS",
			DisplayName:       "RU-STLS",
			Protocol:          "shadowtls",
			ListenPort:        8446,
			Enabled:           false,
			IsBuiltin:         false,
			SortOrder:         12,
			ExitOutbound:      "direct",
			ShadowTLSVersion:  3,
			ShadowTLSPassword: "REPLACE_ME_VIA_API",
			CoverDomain:       "gosuslugi.ru",
			InnerMethod:       "2022-blake3-aes-128-gcm",
			InnerPassword:     "REPLACE_ME_BASE64_16B",
		}
		DB.Create(&shadowtlsSeed)
	}

	// Mask-инбаунд (Xray finalmask sudoku перед vless-direct-tcp). Создаётся и
	// на уже существующей БД, но выключен: password маски задаётся через API.
	var maskCount int64
	DB.Model(&InboundConfig{}).Where("tag = ?", "RU-MASK").Count(&maskCount)
	if maskCount == 0 {
		DB.Create(&InboundConfig{
			Tag:          "RU-MASK",
			DisplayName:  "RU-MASK",
			Protocol:     "mask",
			ListenPort:   2071,
			Enabled:      false,
			IsBuiltin:    false,
			SortOrder:    13,
			MaskInnerTag: "vless-direct-tcp",
			MaskJSON: `{"tcp":[{"type":"sudoku","settings":{"password":"REPLACE_ME_VIA_API",` +
				`"ascii":"prefer_entropy","paddingMin":2,"paddingMax":7}}]}`,
		})
	}

	var hc HealthConfig
	if DB.First(&hc).Error != nil {
		DB.Create(&HealthConfig{Enabled: true, IntervalSec: 60, DownHysteresis: 2})
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
		shortIDs = JSONStringArray{GenerateRealityShortID()}
	}

	fingerprint := result.Fingerprint
	if fingerprint == "" {
		fingerprint = "chrome"
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

// migrateFingerprintToChrome переводит Reality-инбаунды с "random" (и с пустым
// значением) на "chrome". Идемпотентна: повторный запуск не находит строк.
//
// Правки сида на прод не влияют — сид выполняется только на пустой таблице, а
// боевые записи заведены давно. Поэтому значение меняется здесь.
//
// Fingerprint не попадает в конфиг sing-box, только в параметр fp= подписочной
// ссылки, поэтому правка не рвёт установленные соединения: клиенты подхватят
// её при очередном обновлении подписки.
func migrateFingerprintToChrome() {
	res := DB.Model(&InboundConfig{}).
		Where("tls_type = ? AND fingerprint IN ?", "reality", []string{"", "random"}).
		Update("fingerprint", "chrome")
	if res.Error != nil {
		log.Println("Migration: fingerprint -> chrome failed:", res.Error)
		return
	}
	if res.RowsAffected > 0 {
		log.Printf("Migration: fingerprint random -> chrome на %d инбаундах", res.RowsAffected)
	}
}

// Helper: Создать токен
func GenerateToken() string {
	return uuid.New().String()
}

// GenerateRealityKeypair возвращает пару X25519-ключей в формате sing-box/xray
// (base64 URL-safe без набивки) — эквивалент `sing-box generate reality-keypair`.
func GenerateRealityKeypair() (privateKey, publicKey string, err error) {
	raw := make([]byte, 32)
	if _, err = rand.Read(raw); err != nil {
		return "", "", err
	}
	// Clamping по RFC 7748, как это делает xray x25519.
	raw[0] &= 248
	raw[31] &= 127
	raw[31] |= 64

	priv, err := ecdh.X25519().NewPrivateKey(raw)
	if err != nil {
		return "", "", err
	}
	enc := base64.RawURLEncoding
	return enc.EncodeToString(priv.Bytes()), enc.EncodeToString(priv.PublicKey().Bytes()), nil
}

// GenerateRealityShortID возвращает случайный short_id для Reality (8 байт в hex).
func GenerateRealityShortID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		log.Fatal("crypto/rand failed:", err)
	}
	return hex.EncodeToString(b)
}
