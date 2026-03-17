package service

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"vpnbot/database"
)

const (
	TelemetBinaryPath  = "/usr/local/bin/telemt"
	TelemetConfigPath  = "/etc/telemt.toml"
	TelemetServicePath = "/etc/systemd/system/telemt.service"
)

// GenerateSecret генерирует 16 случайных байт → 32-hex строку
func GenerateSecret() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		log.Println("Ошибка генерации секрета:", err)
		return ""
	}
	return hex.EncodeToString(b)
}

// GenerateTelemetProxyLink строит tg://proxy ссылку для Fake TLS режима
func GenerateTelemetProxyLink(serverAddr string, port int, secret string, tlsDomain string) string {
	hexDomain := hex.EncodeToString([]byte(tlsDomain))
	return fmt.Sprintf("tg://proxy?server=%s&port=%d&secret=ee%s%s",
		serverAddr, port, secret, hexDomain)
}

// InstallTelemt скачивает бинарник telemt с GitHub releases если его нет
func InstallTelemt() error {
	if _, err := os.Stat(TelemetBinaryPath); err == nil {
		log.Println("telemt уже установлен:", TelemetBinaryPath)
		return nil
	}

	// Определяем архитектуру
	arch := runtime.GOARCH
	switch arch {
	case "amd64":
		arch = "x86_64"
	case "arm64":
		arch = "aarch64"
	}

	// Определяем libc (musl или gnu)
	libc := "gnu"
	out, err := exec.Command("ldd", "--version").CombinedOutput()
	if err != nil || strings.Contains(strings.ToLower(string(out)), "musl") {
		libc = "musl"
	}

	url := fmt.Sprintf("https://github.com/nickolaev/telemt/releases/latest/download/telemt-%s-linux-%s.tar.gz", arch, libc)
	log.Println("Скачиваем telemt:", url)

	// Скачиваем и распаковываем
	cmd := exec.Command("sh", "-c", fmt.Sprintf(
		"cd /tmp && curl -sL '%s' -o telemt.tar.gz && tar xzf telemt.tar.gz && mv telemt '%s' && chmod +x '%s' && rm -f telemt.tar.gz",
		url, TelemetBinaryPath, TelemetBinaryPath))
	if output, err := cmd.CombinedOutput(); err != nil {
		log.Println("Ошибка установки telemt:", err, string(output))
		return fmt.Errorf("ошибка установки telemt: %w", err)
	}

	log.Println("telemt успешно установлен:", TelemetBinaryPath)
	return nil
}

// GenerateTelemetConfig генерирует TOML-конфиг и записывает в /etc/telemt.toml
func GenerateTelemetConfig(cfg database.TelemetConfig) error {
	// Получаем всех telemetUser для этого конфига
	var telemetUsers []database.TelemetUser
	database.DB.Where("telemet_config_id = ?", cfg.ID).Find(&telemetUsers)

	var sb strings.Builder

	// [general]
	sb.WriteString("[general]\n")
	if cfg.ProxyTag != "" {
		sb.WriteString(fmt.Sprintf("ad_tag = \"%s\"\n", cfg.ProxyTag))
		sb.WriteString("use_middle_proxy = true\n")
	} else {
		sb.WriteString("use_middle_proxy = false\n")
	}
	sb.WriteString("\n")

	// [general.modes]
	sb.WriteString("[general.modes]\n")
	sb.WriteString("classic = false\n")
	sb.WriteString("secure = false\n")
	sb.WriteString("tls = true\n")
	sb.WriteString("\n")

	// [censorship]
	sb.WriteString("[censorship]\n")
	tlsDomain := cfg.TLSDomain
	if tlsDomain == "" {
		tlsDomain = "dl.google.com"
	}
	sb.WriteString(fmt.Sprintf("tls_domain = \"%s\"\n", tlsDomain))
	sb.WriteString("\n")

	// [access.users]
	sb.WriteString("[access.users]\n")
	for _, tu := range telemetUsers {
		sb.WriteString(fmt.Sprintf("%s = \"%s\"\n", tu.Label, tu.Secret))
	}

	err := os.WriteFile(TelemetConfigPath, []byte(sb.String()), 0644)
	if err != nil {
		log.Println("Ошибка записи конфига telemt:", err)
		return err
	}

	log.Println("Конфиг telemt записан:", TelemetConfigPath)
	return nil
}

// EnsureTelemetService создаёт systemd unit для telemt
func EnsureTelemetService() error {
	unit := `[Unit]
Description=Telemt MTProto Proxy
After=network.target

[Service]
Type=simple
ExecStart=` + TelemetBinaryPath + ` -c ` + TelemetConfigPath + `
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
`
	err := os.WriteFile(TelemetServicePath, []byte(unit), 0644)
	if err != nil {
		log.Println("Ошибка создания systemd unit telemt:", err)
		return err
	}

	cmd := exec.Command("systemctl", "daemon-reload")
	if err := cmd.Run(); err != nil {
		log.Println("Ошибка daemon-reload:", err)
		return err
	}

	log.Println("systemd unit telemt создан")
	return nil
}

// ReloadTelemet перезапускает сервис telemt
func ReloadTelemet() error {
	cmd := exec.Command("systemctl", "restart", "telemt")
	if err := cmd.Run(); err != nil {
		log.Println("Ошибка перезапуска telemt:", err)
		return err
	}
	log.Println("telemt перезапущен")
	return nil
}

// StartTelemet запускает сервис telemt
func StartTelemet() error {
	cmd := exec.Command("systemctl", "start", "telemt")
	if err := cmd.Run(); err != nil {
		log.Println("Ошибка запуска telemt:", err)
		return err
	}

	// Включаем автозапуск
	exec.Command("systemctl", "enable", "telemt").Run()
	log.Println("telemt запущен")
	return nil
}

// StopTelemet останавливает сервис telemt
func StopTelemet() error {
	cmd := exec.Command("systemctl", "stop", "telemt")
	if err := cmd.Run(); err != nil {
		log.Println("Ошибка остановки telemt:", err)
		return err
	}
	log.Println("telemt остановлен")
	return nil
}

// IsTelemetRunning проверяет запущен ли сервис telemt
func IsTelemetRunning() bool {
	cmd := exec.Command("systemctl", "is-active", "--quiet", "telemt")
	return cmd.Run() == nil
}

// SetupTelemet — полный цикл настройки telemt
func SetupTelemet() error {
	var cfg database.TelemetConfig
	if err := database.DB.First(&cfg).Error; err != nil {
		log.Println("telemt: конфиг не найден, пропускаем настройку")
		return nil
	}

	if !cfg.Enabled {
		log.Println("telemt: выключен, пропускаем настройку")
		return nil
	}

	if err := InstallTelemt(); err != nil {
		return err
	}

	SyncTelemetUsers()

	if err := GenerateTelemetConfig(cfg); err != nil {
		return err
	}

	if err := EnsureTelemetService(); err != nil {
		return err
	}

	return StartTelemet()
}

// GenerateAndReloadTelemet перегенерирует конфиг и перезапускает telemt
func GenerateAndReloadTelemet() {
	var cfg database.TelemetConfig
	if err := database.DB.First(&cfg).Error; err != nil {
		return
	}

	if !cfg.Enabled {
		return
	}

	if err := GenerateTelemetConfig(cfg); err != nil {
		log.Println("Ошибка генерации конфига telemt:", err)
		return
	}

	ReloadTelemet()
}

// SyncTelemetUsers синхронизирует TelemetUser с активными юзерами
func SyncTelemetUsers() {
	var cfg database.TelemetConfig
	if err := database.DB.First(&cfg).Error; err != nil {
		return
	}

	// Получаем всех активных пользователей
	var activeUsers []database.User
	database.DB.Where("status = ?", "active").Find(&activeUsers)

	activeIDs := map[uint]bool{}
	for _, u := range activeUsers {
		activeIDs[u.ID] = true
	}

	// Получаем существующих telemetUser
	var existing []database.TelemetUser
	database.DB.Where("telemet_config_id = ?", cfg.ID).Find(&existing)

	existingByUserID := map[uint]bool{}
	for _, tu := range existing {
		existingByUserID[tu.UserID] = true
	}

	// Создаём TelemetUser для новых активных юзеров
	for _, u := range activeUsers {
		if !existingByUserID[u.ID] {
			tu := database.TelemetUser{
				TelemetConfigID: cfg.ID,
				UserID:          u.ID,
				Label:           u.Username,
				Secret:          GenerateSecret(),
			}
			database.DB.Create(&tu)
			log.Printf("telemt: создан секрет для пользователя %s", u.Username)
		}
	}

	// Удаляем TelemetUser для неактивных юзеров
	for _, tu := range existing {
		if !activeIDs[tu.UserID] {
			database.DB.Unscoped().Delete(&tu)
			log.Printf("telemt: удалён секрет для пользователя %s", tu.Label)
		}
	}
}
