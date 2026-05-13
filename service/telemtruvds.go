package service

import (
	"fmt"
	"log"
	"strings"
	"vpnbot/database"
)

// telemt на RuVDS: те же пути и константы что и в telemt.go.
// Управление через SSH (паттерн из turnproxy.go).

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

	// Определяем libc
	libc := "gnu"
	lddOut, _ := runSSH(client, "ldd --version 2>&1 | head -1")
	if strings.Contains(strings.ToLower(lddOut), "musl") {
		libc = "musl"
	}

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
	out, err := runSSH(client, "systemctl is-active --quiet telemt && echo running")
	return err == nil && strings.TrimSpace(out) == "running"
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
