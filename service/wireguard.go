package service

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"vpnbot/database"

	"golang.org/x/crypto/curve25519"
)

const (
	WGInterfaceName = "wg0"
	WGConfigPath    = "/etc/wireguard/wg0.conf"
	WGServiceName   = "wg-quick@wg0"
)

// IsWireGuardEnabled true если WG-туннель должен быть активен (по флагу из БД).
func IsWireGuardEnabled() bool {
	cfg, err := GetWireGuardConfig()
	if err != nil {
		return false
	}
	return cfg.Enabled
}

// IsRuVDSEnabled — true если RuVDS sing-box зеркало должно запускаться.
// Привязано к WG: sing-box на RuVDS использует WG как outbound.
func IsRuVDSEnabled() bool {
	return IsWireGuardEnabled()
}

func GetWireGuardConfig() (database.WireGuardConfig, error) {
	var cfg database.WireGuardConfig
	if err := database.DB.First(&cfg).Error; err != nil {
		return cfg, err
	}
	return cfg, nil
}

// SetupWireGuard — полный цикл настройки WG на Hetzner. Идемпотентен.
func SetupWireGuard() error {
	cfg, err := ensureWGConfigRow()
	if err != nil {
		return err
	}

	if !cfg.Enabled {
		log.Println("wireguard: отключён, пропускаем настройку")
		return nil
	}

	// Гарантируем что wireguard-tools установлен ДО любых wg-операций.
	// (Ключи генерим в чистом Go, но wg-quick всё равно нужен.)
	if err := ensureWireGuardToolsInstalled(); err != nil {
		updateWGStatus("error", err.Error())
		return err
	}

	if cfg.HetznerPrivateKey == "" || cfg.HetznerPublicKey == "" {
		priv, pub, err := GenerateWGKeypair()
		if err != nil {
			updateWGStatus("error", "ключи Hetzner: "+err.Error())
			return err
		}
		cfg.HetznerPrivateKey = priv
		cfg.HetznerPublicKey = pub
	}
	if cfg.RuVDSPrivateKey == "" || cfg.RuVDSPublicKey == "" {
		priv, pub, err := GenerateWGKeypair()
		if err != nil {
			updateWGStatus("error", "ключи RuVDS: "+err.Error())
			return err
		}
		cfg.RuVDSPrivateKey = priv
		cfg.RuVDSPublicKey = pub
	}

	database.DB.Save(&cfg)

	if err := EnsureHetznerWG(cfg); err != nil {
		updateWGStatus("error", err.Error())
		return err
	}

	if IsFirewallConfigured() {
		if err := OpenFirewallPort(cfg.ListenPort, "udp", "WireGuard tunnel"); err != nil {
			log.Printf("wireguard: не удалось открыть порт %d/udp в Cloud Firewall: %v", cfg.ListenPort, err)
		}
	} else {
		ufwAllow(cfg.ListenPort, "udp")
	}

	updateWGStatus("active", "WG-туннель настроен")
	return nil
}

// ensureWGConfigRow гарантирует наличие синглтон-записи WireGuardConfig.
func ensureWGConfigRow() (database.WireGuardConfig, error) {
	var cfg database.WireGuardConfig
	if err := database.DB.First(&cfg).Error; err != nil {
		cfg = database.WireGuardConfig{
			Enabled:     false,
			ListenPort:  51820,
			HetznerWGIP: "10.8.0.1",
			RuVDSWGIP:   "10.8.0.2",
			MTU:         1408,
		}
		if err := database.DB.Create(&cfg).Error; err != nil {
			return cfg, err
		}
	}
	return cfg, nil
}

// GenerateWGKeypair генерирует пару WireGuard-ключей через curve25519 в чистом Go
// (не зависит от установленного бинаря wg). Формат — стандартный WG base64.
func GenerateWGKeypair() (private string, public string, err error) {
	privBytes := make([]byte, 32)
	if _, err := rand.Read(privBytes); err != nil {
		return "", "", fmt.Errorf("crypto/rand: %w", err)
	}
	// Clamp по curve25519/WG-спецификации
	privBytes[0] &= 248
	privBytes[31] &= 127
	privBytes[31] |= 64

	pubBytes, err := curve25519.X25519(privBytes, curve25519.Basepoint)
	if err != nil {
		return "", "", fmt.Errorf("curve25519: %w", err)
	}

	private = base64.StdEncoding.EncodeToString(privBytes)
	public = base64.StdEncoding.EncodeToString(pubBytes)
	return
}

// ensureWireGuardToolsInstalled — apt-get install wireguard-tools если бинаря wg нет.
// Нужен для wg-quick@wg0 (генерация ключей теперь чисто Go).
// `apt-get update` сделан non-fatal — поломанный сторонний репо (например Caddy/Cloudsmith
// с истёкшим GPG ключом) не должен блокировать установку из официального Debian.
func ensureWireGuardToolsInstalled() error {
	if exec.Command("sh", "-c", "command -v wg-quick >/dev/null 2>&1").Run() == nil {
		return nil
	}
	log.Println("wireguard: устанавливаем wireguard-tools...")

	// Шаг 1: update тихо, не падаем при NO_PUBKEY/сломанных репах.
	exec.Command("sh", "-c", "DEBIAN_FRONTEND=noninteractive apt-get update -qq 2>&1 || true").Run()

	// Шаг 2: установка. Если ещё нет cache — пробуем без update, но с фоллбэком на --fix-missing.
	out, err := exec.Command("sh", "-c",
		"DEBIAN_FRONTEND=noninteractive apt-get install -y wireguard wireguard-tools || "+
			"DEBIAN_FRONTEND=noninteractive apt-get install -y --fix-missing wireguard wireguard-tools").CombinedOutput()
	if err != nil {
		return fmt.Errorf("apt-get install wireguard-tools: %w: %s", err, string(out))
	}

	// Проверка пост-install.
	if exec.Command("sh", "-c", "command -v wg-quick >/dev/null 2>&1").Run() != nil {
		return fmt.Errorf("wg-quick всё ещё не найден после apt install — проверьте репозитории (`apt-get update` вручную): %s", string(out))
	}
	return nil
}

// detectDefaultIface — имя сетевого интерфейса для MASQUERADE на Hetzner.
func detectDefaultIface() string {
	out, err := exec.Command("sh", "-c",
		"ip -4 route show default | awk '{for(i=1;i<=NF;i++) if($i==\"dev\"){print $(i+1); exit}}'").Output()
	if err != nil {
		return "eth0"
	}
	iface := strings.TrimSpace(string(out))
	if iface == "" {
		return "eth0"
	}
	return iface
}

// EnsureHetznerWG настраивает wg-quick@wg0 на Hetzner локально. Идемпотентна.
func EnsureHetznerWG(cfg database.WireGuardConfig) error {
	if err := ensureWireGuardToolsInstalled(); err != nil {
		return err
	}

	iface := detectDefaultIface()
	wgIP := cfg.HetznerWGIP
	if wgIP == "" {
		wgIP = "10.8.0.1"
	}
	port := cfg.ListenPort
	if port == 0 {
		port = 51820
	}
	ruvdsWGIP := cfg.RuVDSWGIP
	if ruvdsWGIP == "" {
		ruvdsWGIP = "10.8.0.2"
	}

	wgConf := fmt.Sprintf(`# Managed by vpnbot, do not edit manually
[Interface]
Address = %s/24
ListenPort = %d
PrivateKey = %s
PostUp = iptables -t nat -A POSTROUTING -s 10.8.0.0/24 -o %s -j MASQUERADE
PostDown = iptables -t nat -D POSTROUTING -s 10.8.0.0/24 -o %s -j MASQUERADE

[Peer]
# RuVDS sing-box userspace WG client
PublicKey = %s
AllowedIPs = %s/32
`, wgIP, port, cfg.HetznerPrivateKey, iface, iface, cfg.RuVDSPublicKey, ruvdsWGIP)

	if err := os.MkdirAll("/etc/wireguard", 0700); err != nil {
		return fmt.Errorf("mkdir /etc/wireguard: %w", err)
	}
	if err := os.WriteFile(WGConfigPath, []byte(wgConf), 0600); err != nil {
		return fmt.Errorf("write %s: %w", WGConfigPath, err)
	}

	if err := os.WriteFile("/etc/sysctl.d/99-vpnbot-wg.conf",
		[]byte("net.ipv4.ip_forward=1\n"), 0644); err != nil {
		log.Printf("wireguard: warn sysctl persist: %v", err)
	}
	exec.Command("sysctl", "-w", "net.ipv4.ip_forward=1").Run()

	exec.Command("systemctl", "stop", WGServiceName).Run()
	if err := exec.Command("systemctl", "enable", WGServiceName).Run(); err != nil {
		log.Printf("wireguard: enable warn: %v", err)
	}
	if out, err := exec.Command("systemctl", "start", WGServiceName).CombinedOutput(); err != nil {
		return fmt.Errorf("systemctl start %s: %w: %s", WGServiceName, err, string(out))
	}

	log.Println("wireguard: wg-quick@wg0 запущен на Hetzner")
	return nil
}

func RestartHetznerWG() error {
	if out, err := exec.Command("systemctl", "restart", WGServiceName).CombinedOutput(); err != nil {
		return fmt.Errorf("systemctl restart: %w: %s", err, string(out))
	}
	return nil
}

func StopHetznerWG() error {
	exec.Command("systemctl", "stop", WGServiceName).Run()
	exec.Command("systemctl", "disable", WGServiceName).Run()
	updateWGStatus("inactive", "WG остановлен")
	return nil
}

func IsHetznerWGRunning() bool {
	return exec.Command("systemctl", "is-active", "--quiet", WGServiceName).Run() == nil
}

type WGStatus struct {
	Running         bool   `json:"running"`
	Interface       string `json:"interface"`
	ListenPort      int    `json:"listen_port"`
	PeerPublicKey   string `json:"peer_public_key"`
	Endpoint        string `json:"endpoint"`
	AllowedIPs      string `json:"allowed_ips"`
	LatestHandshake string `json:"latest_handshake"`
	Transfer        string `json:"transfer"`
}

func GetWGStatus() (*WGStatus, error) {
	if !IsHetznerWGRunning() {
		return &WGStatus{Running: false}, nil
	}
	out, err := exec.Command("wg", "show", "wg0").CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("wg show: %w: %s", err, string(out))
	}
	st := &WGStatus{Running: true, Interface: "wg0"}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "listening port:"):
			fmt.Sscanf(line, "listening port: %d", &st.ListenPort)
		case strings.HasPrefix(line, "peer:"):
			st.PeerPublicKey = strings.TrimSpace(strings.TrimPrefix(line, "peer:"))
		case strings.HasPrefix(line, "endpoint:"):
			st.Endpoint = strings.TrimSpace(strings.TrimPrefix(line, "endpoint:"))
		case strings.HasPrefix(line, "allowed ips:"):
			st.AllowedIPs = strings.TrimSpace(strings.TrimPrefix(line, "allowed ips:"))
		case strings.HasPrefix(line, "latest handshake:"):
			st.LatestHandshake = strings.TrimSpace(strings.TrimPrefix(line, "latest handshake:"))
		case strings.HasPrefix(line, "transfer:"):
			st.Transfer = strings.TrimSpace(strings.TrimPrefix(line, "transfer:"))
		}
	}
	return st, nil
}

// BuildWireguardOutbound — sing-box outbound для туннеля на Hetzner.
func BuildWireguardOutbound(cfg database.WireGuardConfig, hetznerEndpoint string) OutboundConfig {
	return OutboundConfig{
		Type:          "wireguard",
		Tag:           "wg-out",
		Server:        hetznerEndpoint,
		ServerPort:    cfg.ListenPort,
		LocalAddress:  []string{cfg.RuVDSWGIP + "/32"},
		PrivateKey:    cfg.RuVDSPrivateKey,
		PeerPublicKey: cfg.HetznerPublicKey,
		MTU:           cfg.MTU,
	}
}

func updateWGStatus(status, msg string) {
	var cfg database.WireGuardConfig
	if err := database.DB.First(&cfg).Error; err != nil {
		return
	}
	cfg.Status = status
	cfg.StatusMsg = msg
	database.DB.Save(&cfg)
}
