package service

import (
	"bytes"
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
	TelemetBinaryPath  = "/bin/telemt"
	TelemetConfigDir   = "/etc/telemt"
	TelemetConfigPath  = "/etc/telemt/telemt.toml"
	TelemetServicePath = "/etc/systemd/system/telemt.service"
	TelemetWorkDir     = "/opt/telemt"
)

// GenerateSecret генерирует 16 случайных байт → 32-hex строку
func GenerateSecret() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand failure означает критическую проблему системы
		log.Fatalf("Критическая ошибка: не удалось сгенерировать секрет: %v", err)
	}
	return hex.EncodeToString(b)
}

// GenerateTelemetProxyLink строит tg://proxy ссылку для Fake TLS режима
func GenerateTelemetProxyLink(serverAddr string, port int, secret string, tlsDomain string) string {
	hexDomain := hex.EncodeToString([]byte(tlsDomain))
	return fmt.Sprintf("tg://proxy?server=%s&port=%d&secret=ee%s%s",
		serverAddr, port, secret, hexDomain)
}

// GenerateTelemetSecureLink строит ссылку для режима secure (обфускация без TLS).
// Секрет тот же, отличается только префикс dd — новых секретов заводить не нужно.
func GenerateTelemetSecureLink(serverAddr string, port int, secret string) string {
	return fmt.Sprintf("tg://proxy?server=%s&port=%d&secret=dd%s", serverAddr, port, secret)
}

// GenerateTelemetClassicLink строит ссылку для classic MTProto — без TLS, без SNI,
// голый обфусцированный поток. Маскировки нет вовсе, поэтому вариант заметный;
// держим его как диагностический и как запасной вход, см. TelemetConfig.ClassicEnabled.
func GenerateTelemetClassicLink(serverAddr string, port int, secret string) string {
	return fmt.Sprintf("tg://proxy?server=%s&port=%d&secret=%s", serverAddr, port, secret)
}

// InstallTelemt скачивает бинарник telemt с GitHub releases если его нет
func InstallTelemt() error {
	if _, err := os.Stat(TelemetBinaryPath); err == nil {
		log.Println("telemt уже установлен:", TelemetBinaryPath)
		return nil
	}

	// Определяем архитектуру через uname -m (x86_64 / aarch64)
	archOut, err := exec.Command("uname", "-m").Output()
	if err != nil {
		// fallback на Go runtime
		arch := runtime.GOARCH
		switch arch {
		case "amd64":
			archOut = []byte("x86_64")
		case "arm64":
			archOut = []byte("aarch64")
		default:
			archOut = []byte(arch)
		}
	}
	arch := strings.TrimSpace(string(archOut))

	// Определяем libc (musl или gnu)
	libc := "gnu"
	out, err := exec.Command("ldd", "--version").CombinedOutput()
	if err != nil || strings.Contains(strings.ToLower(string(out)), "musl") {
		libc = "musl"
	}

	url := fmt.Sprintf("https://github.com/telemt/telemt/releases/latest/download/telemt-%s-linux-%s.tar.gz", arch, libc)
	log.Println("Скачиваем telemt:", url)

	// Скачиваем и распаковываем (по документации telemt)
	installCmd := fmt.Sprintf(
		"wget -qO- '%s' | tar -xz && mv telemt '%s' && chmod +x '%s'",
		url, TelemetBinaryPath, TelemetBinaryPath)
	cmd := exec.Command("sh", "-c", installCmd)
	cmd.Dir = "/tmp"
	if output, err := cmd.CombinedOutput(); err != nil {
		log.Println("Ошибка установки telemt:", err, string(output))
		return fmt.Errorf("ошибка установки telemt: %w", err)
	}

	// Создаём пользователя telemt, директории для конфига и работы
	exec.Command("useradd", "--system", "--no-create-home", "--shell", "/usr/sbin/nologin", "telemt").Run()
	os.MkdirAll(TelemetConfigDir, 0755)
	os.MkdirAll(TelemetWorkDir, 0755)
	exec.Command("chown", "telemt:telemt", TelemetWorkDir).Run()

	log.Println("telemt успешно установлен:", TelemetBinaryPath)
	return nil
}

// BuildTelemetConfigTOML строит TOML-конфиг telemt в виде байтов (без записи в файл).
// Используется обоими хостами: локальная запись на Hetzner и SSH-деплой на RuVDS.
func BuildTelemetConfigTOML(cfg database.TelemetConfig) []byte {
	var telemetUsers []database.TelemetUser
	database.DB.Where("telemet_config_id = ?", cfg.ID).Find(&telemetUsers)

	port := cfg.Port
	if port == 0 {
		port = 9443
	}
	tlsDomain := cfg.TLSDomain
	if tlsDomain == "" {
		tlsDomain = "lk.rt.ru"
	}

	var sb strings.Builder

	sb.WriteString("[general]\n")
	if cfg.ProxyTag != "" {
		sb.WriteString(fmt.Sprintf("ad_tag = \"%s\"\n", cfg.ProxyTag))
		sb.WriteString("use_middle_proxy = true\n")
	} else {
		sb.WriteString("use_middle_proxy = false\n")
	}
	sb.WriteString("\n")

	sb.WriteString("[general.modes]\n")
	sb.WriteString(fmt.Sprintf("classic = %t\n", cfg.ClassicEnabled))
	sb.WriteString(fmt.Sprintf("secure = %t\n", cfg.SecureEnabled))
	sb.WriteString("tls = true\n")
	sb.WriteString("\n")

	sb.WriteString("[server]\n")
	sb.WriteString(fmt.Sprintf("port = %d\n", port))
	sb.WriteString("\n")

	sb.WriteString("[server.api]\n")
	sb.WriteString("enabled = true\n")
	sb.WriteString("\n")

	// telemt 3.4.x требует хотя бы один listener-блок, иначе старт падает
	// с "No listeners. Exiting." (формат конфига изменился относительно 3.3.x).
	sb.WriteString("[[server.listeners]]\n")
	sb.WriteString("ip = \"0.0.0.0\"\n")
	sb.WriteString("\n")

	sb.WriteString("[censorship]\n")
	sb.WriteString(fmt.Sprintf("tls_domain = \"%s\"\n", tlsDomain))
	// unknown_sni_action=mask: на чужой/пустой SNI telemt прозрачно проксирует
	// хендшейк на tls_domain:443, поэтому эндпоинт байт-в-байт повторяет поведение
	// настоящего домена (matching SNI → его cert; чужой SNI → его же TLS-alert
	// unrecognized_name), а не выдаёт себя «чистым» TCP-EOF (дефолт telemt = Drop).
	// Проверено 26.07.2026: main:9443 и реальный lk.rt.ru на SNI=example.com дают
	// идентичный alert 112 (read 7 / written 1542 байт). Форвард-фейл на
	// egress-заблокированном хосте НЕ фатален (per-connection), безопасно для RuVDS.
	sb.WriteString("unknown_sni_action = \"mask\"\n")
	// mask/tls_emulation/tls_front_dir НЕ включаем сразу: tls_emulation требует
	// fetch'а реального сертификата с tls_domain, что на RuVDS через РКН-uplink
	// может блокироваться/таймаутить — это вгоняло сервис в restart loop с
	// "No listeners. Exiting" каждые ~3 минуты. Сначала добиваемся стабильности
	// минимального конфига, эмуляцию включаем когда подтвердим выход в lk.rt.ru:443.
	sb.WriteString("\n")

	sb.WriteString("[access.users]\n")
	for _, tu := range telemetUsers {
		sb.WriteString(fmt.Sprintf("%s = \"%s\"\n", tu.Label, tu.Secret))
	}

	return []byte(sb.String())
}

// GenerateTelemetConfig пишет TOML-конфиг telemt локально на Hetzner.
// Возвращает changed=true, если содержимое отличается от того, что уже на диске —
// по нему вызывающий решает, нужен ли перезапуск: telemt перечитывает конфиг только
// при старте, а лишний рестарт рвёт живые сессии пользователей.
func GenerateTelemetConfig(cfg database.TelemetConfig) (bool, error) {
	os.MkdirAll(TelemetConfigDir, 0755)

	next := BuildTelemetConfigTOML(cfg)
	prev, err := os.ReadFile(TelemetConfigPath)
	changed := err != nil || !bytes.Equal(prev, next)

	if err := os.WriteFile(TelemetConfigPath, next, 0644); err != nil {
		log.Println("Ошибка записи конфига telemt:", err)
		return false, err
	}
	if changed {
		log.Println("Конфиг telemt изменился, записан:", TelemetConfigPath)
	}
	return changed, nil
}

// EnsureTelemetService создаёт systemd unit для telemt (по документации telemt)
func EnsureTelemetService() error {
	unit := `[Unit]
Description=Telemt
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=telemt
Group=telemt
WorkingDirectory=` + TelemetWorkDir + `
ExecStart=` + TelemetBinaryPath + ` ` + TelemetConfigPath + `
Restart=on-failure
LimitNOFILE=65536
AmbientCapabilities=CAP_NET_BIND_SERVICE
CapabilityBoundingSet=CAP_NET_BIND_SERVICE
NoNewPrivileges=true

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

	changed, err := GenerateTelemetConfig(cfg)
	if err != nil {
		return err
	}

	if err := EnsureTelemetService(); err != nil {
		return err
	}

	// StartTelemet для уже запущенного сервиса — пустая операция, а telemt читает
	// конфиг только при старте. Поэтому правки в БД молча не доезжали: конфиг на
	// диске новый, процесс работает со старым. Так режимы classic/secure провисели
	// невключёнными, пока это не нашлось руками. Рестартуем только при реальном
	// изменении, чтобы не рвать сессии на каждом подъёме демона.
	if changed {
		return ReloadTelemet()
	}
	return StartTelemet()
}

// GenerateAndReloadTelemet перегенерирует конфиг и перезапускает telemt.
// Если включён RuVDS-вариант (TelemetConfig.RuVDSEnabled) — также деплоит на RuVDS через SSH.
func GenerateAndReloadTelemet() {
	var cfg database.TelemetConfig
	if err := database.DB.First(&cfg).Error; err != nil {
		return
	}

	if cfg.Enabled {
		if _, err := GenerateTelemetConfig(cfg); err != nil {
			log.Println("Ошибка генерации конфига telemt:", err)
		} else {
			ReloadTelemet()
		}
	}

	if cfg.RuVDSEnabled {
		if err := GenerateAndReloadTelemtRuVDS(); err != nil {
			log.Println("RuVDS telemt reload error:", err)
		}
	}
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
