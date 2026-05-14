package service

import (
	"fmt"
	"log"
	"strings"
	"vpnbot/database"
)

// telemt на RuVDS: те же пути и константы что и в telemt.go.
// Управление через SSH (паттерн из turnproxy.go).

// TelemtPinnedVersion — версия, на которую UpgradeTelemtRuVDS обновляет бинарник.
// Пиним явно вместо releases/latest: issue telemt/telemt#628 показал, что
// после обновления 3.3.22 → 3.3.35 пришлось откатываться. 3.4.11 (10 мая 2026)
// — текущая стабильная с фиксами по TLS-fronting и quota persistence.
// Внимание: тег без префикса "v" — релизы лежат под "3.4.11", не "v3.4.11".
const TelemtPinnedVersion = "3.4.11"

// InstallTelemtRuVDS — скачивает и устанавливает telemt на RuVDS.
// Идемпотентна.
func InstallTelemtRuVDS() error {
	client, err := sshConnect()
	if err != nil {
		return fmt.Errorf("SSH: %w", err)
	}
	defer client.Close()

	out, _ := runSSH(client, fmt.Sprintf("test -x %s && echo exists", TelemetBinaryPath))
	if strings.TrimSpace(out) == "exists" {
		log.Println("telemt уже установлен на RuVDS")
		return nil
	}

	// Определяем архитектуру RuVDS
	archOut, err := runSSH(client, "uname -m")
	if err != nil {
		return fmt.Errorf("uname: %w", err)
	}
	arch := strings.TrimSpace(archOut)

	// Всегда берём musl-сборку: она статически линкована и не зависит от системного
	// glibc. На RuVDS типично старая Ubuntu/Debian с glibc 2.31, а gnu-сборки
	// telemt 3.4.x требуют GLIBC_2.34 — на старых системах падают сразу при старте
	// с "version `GLIBC_2.34' not found".
	libc := "musl"

	url := fmt.Sprintf(
		"https://github.com/telemt/telemt/releases/latest/download/telemt-%s-linux-%s.tar.gz",
		arch, libc)

	install := fmt.Sprintf(
		"cd /tmp && wget -qO- '%s' | tar -xz && mv telemt %s && chmod +x %s && "+
			"useradd --system --no-create-home --shell /usr/sbin/nologin telemt 2>/dev/null; "+
			"mkdir -p %s %s && chown telemt:telemt %s",
		url, TelemetBinaryPath, TelemetBinaryPath,
		TelemetConfigDir, TelemetWorkDir, TelemetWorkDir)

	if output, err := runSSH(client, install); err != nil {
		return fmt.Errorf("установка telemt на RuVDS: %w: %s", err, output)
	}
	log.Println("telemt установлен на RuVDS:", TelemetBinaryPath)
	return nil
}

// DeployTelemtConfigRuVDS пишет /etc/telemt/telemt.toml на RuVDS.
func DeployTelemtConfigRuVDS(tomlBytes []byte) error {
	client, err := sshConnect()
	if err != nil {
		return fmt.Errorf("SSH: %w", err)
	}
	defer client.Close()

	cmd := fmt.Sprintf("mkdir -p %s && cat > %s << 'TOMLEOF'\n%s\nTOMLEOF",
		TelemetConfigDir, TelemetConfigPath, string(tomlBytes))
	if out, err := runSSH(client, cmd); err != nil {
		return fmt.Errorf("запись telemt.toml: %w: %s", err, out)
	}
	return nil
}

// EnsureTelemtRuVDSService создаёт systemd unit для telemt на RuVDS.
func EnsureTelemtRuVDSService() error {
	client, err := sshConnect()
	if err != nil {
		return fmt.Errorf("SSH: %w", err)
	}
	defer client.Close()

	unit := fmt.Sprintf(`[Unit]
Description=Telemt (managed by vpnbot)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=telemt
Group=telemt
WorkingDirectory=%s
ExecStart=%s %s
Restart=on-failure
LimitNOFILE=65536
AmbientCapabilities=CAP_NET_BIND_SERVICE
CapabilityBoundingSet=CAP_NET_BIND_SERVICE
NoNewPrivileges=true

[Install]
WantedBy=multi-user.target
`, TelemetWorkDir, TelemetBinaryPath, TelemetConfigPath)

	cmd := fmt.Sprintf("cat > %s << 'UNITEOF'\n%sUNITEOF", TelemetServicePath, unit)
	if _, err := runSSH(client, cmd); err != nil {
		return fmt.Errorf("запись systemd unit: %w", err)
	}
	if _, err := runSSH(client, "systemctl daemon-reload"); err != nil {
		return fmt.Errorf("daemon-reload: %w", err)
	}
	return nil
}

func StartTelemtRuVDS() error {
	client, err := sshConnect()
	if err != nil {
		return fmt.Errorf("SSH: %w", err)
	}
	defer client.Close()
	if _, err := runSSH(client, "systemctl start telemt"); err != nil {
		return fmt.Errorf("start: %w", err)
	}
	runSSH(client, "systemctl enable telemt")
	return nil
}

func ReloadTelemtRuVDS() error {
	client, err := sshConnect()
	if err != nil {
		return fmt.Errorf("SSH: %w", err)
	}
	defer client.Close()
	_, err = runSSH(client, "systemctl restart telemt")
	return err
}

func StopTelemtRuVDS() error {
	client, err := sshConnect()
	if err != nil {
		return fmt.Errorf("SSH: %w", err)
	}
	defer client.Close()
	_, err = runSSH(client, "systemctl stop telemt")
	return err
}

func IsTelemtRuVDSRunning() bool {
	client, err := sshConnect()
	if err != nil {
		return false
	}
	defer client.Close()
	out, _ := runSSH(client, "systemctl is-active telemt 2>/dev/null; true")
	return strings.TrimSpace(out) == "active"
}

// TelemtRuVDSLogs — последние N строк журнала telemt на RuVDS.
func TelemtRuVDSLogs(lines int) (string, error) {
	if lines <= 0 {
		lines = 50
	}
	client, err := sshConnect()
	if err != nil {
		return "", fmt.Errorf("SSH: %w", err)
	}
	defer client.Close()
	out, err := runSSH(client, fmt.Sprintf(
		"journalctl -u telemt -n %d --no-pager 2>&1; echo '---'; systemctl status telemt --no-pager 2>&1; "+
			"echo '--- /etc/telemt/telemt.toml ---'; cat /etc/telemt/telemt.toml 2>&1; true", lines))
	if err != nil {
		return out, fmt.Errorf("journalctl: %w", err)
	}
	return out, nil
}

// TelemtRuVDSDiagnostics собирает структурированный отчёт по состоянию telemt на RuVDS.
// Используется для расследования жалоб клиентов: версия бинарника + конфиг +
// сэмпл логов + активные соединения на порту telemt + проверка исходящего трафика.
type TelemtRuVDSDiagnostics struct {
	Version          string `json:"version"`
	PinnedVersion    string `json:"pinned_version"`
	NeedsUpgrade     bool   `json:"needs_upgrade"`
	ConfigTOML       string `json:"config_toml"`
	ServiceStatus    string `json:"service_status"`
	JournalTail      string `json:"journal_tail"`
	HandshakeTimeout int    `json:"handshake_timeout_count"` // кол-во "handshake timeout" за последние логи
	ActiveConns      string `json:"active_connections"`
	EgressTelegram   string `json:"egress_telegram"` // TCP-проба до Telegram DC
}

func GetTelemtRuVDSDiagnostics() (*TelemtRuVDSDiagnostics, error) {
	client, err := sshConnect()
	if err != nil {
		return nil, fmt.Errorf("SSH: %w", err)
	}
	defer client.Close()

	var cfg database.TelemetConfig
	database.DB.First(&cfg)
	port := cfg.Port
	if port == 0 {
		port = 9443
	}

	d := &TelemtRuVDSDiagnostics{PinnedVersion: TelemtPinnedVersion}

	versionOut, _ := runSSH(client, fmt.Sprintf("%s --version 2>&1 | head -1; true", TelemetBinaryPath))
	d.Version = strings.TrimSpace(versionOut)
	d.NeedsUpgrade = !strings.Contains(d.Version, strings.TrimPrefix(TelemtPinnedVersion, "v"))

	configOut, _ := runSSH(client, fmt.Sprintf("cat %s 2>&1; true", TelemetConfigPath))
	d.ConfigTOML = configOut

	statusOut, _ := runSSH(client, "systemctl status telemt --no-pager 2>&1 | head -20; true")
	d.ServiceStatus = statusOut

	journalOut, _ := runSSH(client, "journalctl -u telemt -n 200 --no-pager 2>&1; true")
	d.JournalTail = journalOut
	d.HandshakeTimeout = strings.Count(strings.ToLower(journalOut), "handshake timeout")

	connsOut, _ := runSSH(client, fmt.Sprintf("ss -tn state established '( sport = :%d )' 2>&1 | wc -l; true", port))
	d.ActiveConns = strings.TrimSpace(connsOut)

	// 149.154.167.50:443 — Telegram middle proxy DC (стабильный IP, который telemt
	// дёргает через DC nonce при handshake). Если у TCP-пробы таймаут — egress блокирован.
	egressOut, _ := runSSH(client,
		"timeout 5 bash -c '</dev/tcp/149.154.167.50/443' 2>&1 && echo OK || echo FAIL")
	d.EgressTelegram = strings.TrimSpace(egressOut)

	return d, nil
}

// UpgradeTelemtRuVDS форсит переустановку telemt на RuVDS на TelemtPinnedVersion.
// Шаги: stop → backup старого бинарника → download → replace → start.
// Безопасно: при падении download/replace оставляем старый бинарник в .bak.
func UpgradeTelemtRuVDS() error {
	client, err := sshConnect()
	if err != nil {
		return fmt.Errorf("SSH: %w", err)
	}
	defer client.Close()

	archOut, err := runSSH(client, "uname -m")
	if err != nil {
		return fmt.Errorf("uname: %w", err)
	}
	arch := strings.TrimSpace(archOut)

	// см. InstallTelemtRuVDS — всегда musl ради переносимости поверх старых glibc.
	libc := "musl"

	url := fmt.Sprintf(
		"https://github.com/telemt/telemt/releases/download/%s/telemt-%s-linux-%s.tar.gz",
		TelemtPinnedVersion, arch, libc)

	// stop сервис, скачиваем во временное место, проверяем что бинарник работает (--version),
	// только потом замещаем. Если что-то пошло не так — откатываем из .bak.
	cmd := fmt.Sprintf(`set -e
		systemctl stop telemt 2>/dev/null || true
		cp -a %s %s.bak 2>/dev/null || true
		cd /tmp && rm -f telemt && wget -qO- '%s' | tar -xz
		test -x /tmp/telemt
		/tmp/telemt --version >/dev/null 2>&1 || { echo "new binary --version failed"; exit 1; }
		mv /tmp/telemt %s
		chmod +x %s
		systemctl start telemt
		sleep 1
		systemctl is-active --quiet telemt || { echo "service failed to start, rolling back"; mv %s.bak %s; systemctl start telemt; exit 2; }
		rm -f %s.bak
		echo upgraded
	`, TelemetBinaryPath, TelemetBinaryPath, url,
		TelemetBinaryPath, TelemetBinaryPath,
		TelemetBinaryPath, TelemetBinaryPath, TelemetBinaryPath)

	if output, err := runSSH(client, cmd); err != nil {
		return fmt.Errorf("upgrade %s: %w: %s", TelemtPinnedVersion, err, output)
	}
	log.Printf("telemt RuVDS обновлён до %s", TelemtPinnedVersion)
	return nil
}

// SetupTelemtRuVDS — полный цикл установки telemt на RuVDS.
func SetupTelemtRuVDS() error {
	var cfg database.TelemetConfig
	if err := database.DB.First(&cfg).Error; err != nil {
		log.Println("telemt RuVDS: TelemetConfig не найден, пропускаем")
		return nil
	}
	if !cfg.RuVDSEnabled {
		log.Println("telemt RuVDS: отключён (RuVDSEnabled=false), пропускаем")
		return nil
	}
	if !IsPortForwardConfigured() {
		return fmt.Errorf("telemt RuVDS: RUVDS_IP не задан")
	}

	if err := InstallTelemtRuVDS(); err != nil {
		return err
	}

	// Используем общие секреты пользователей из БД
	SyncTelemetUsers()

	if err := DeployTelemtConfigRuVDS(BuildTelemetConfigTOML(cfg)); err != nil {
		return err
	}
	if err := EnsureTelemtRuVDSService(); err != nil {
		return err
	}

	// Открываем порт в UFW на RuVDS
	openTelemtPortRuVDS(cfg)

	return StartTelemtRuVDS()
}

func openTelemtPortRuVDS(cfg database.TelemetConfig) {
	port := cfg.Port
	if port == 0 {
		port = 9443
	}
	client, err := sshConnect()
	if err != nil {
		return
	}
	defer client.Close()
	runSSH(client, fmt.Sprintf("ufw allow %d/tcp comment 'telemt RuVDS' 2>/dev/null; true", port))
}

// GenerateAndReloadTelemtRuVDS — перегенерация TOML + рестарт telemt на RuVDS.
func GenerateAndReloadTelemtRuVDS() error {
	var cfg database.TelemetConfig
	if err := database.DB.First(&cfg).Error; err != nil {
		return nil
	}
	if !cfg.RuVDSEnabled {
		return nil
	}
	if err := DeployTelemtConfigRuVDS(BuildTelemetConfigTOML(cfg)); err != nil {
		return err
	}
	return ReloadTelemtRuVDS()
}
