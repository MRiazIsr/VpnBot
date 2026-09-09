package service

import (
	"fmt"
	"log"
	"strings"
	"vpnbot/database"
)

const (
	XrayRuVDSBinaryPath  = "/usr/local/bin/xray"
	XrayRuVDSConfigDir   = "/etc/xray"
	XrayRuVDSConfigPath  = "/etc/xray/config.json"
	XrayRuVDSServiceName = "xray"
	XrayRuVDSServicePath = "/etc/systemd/system/xray.service"
)

// XrayRuVDSVersion — строка `xray version` на RuVDS ("" если не установлен).
func XrayRuVDSVersion() string {
	client, err := sshConnect()
	if err != nil {
		return ""
	}
	defer client.Close()
	out, _ := runSSH(client, fmt.Sprintf("%s version 2>/dev/null | head -1; true", XrayRuVDSBinaryPath))
	return strings.TrimSpace(out)
}

// InstallXrayRuVDS — ставит запиненную версию Xray на RuVDS. Идемпотентна:
// если `xray version` уже показывает XrayVersion — ничего не делает. Любую
// другую версию перезаписывает: в отличие от sing-box, кастомных сборок
// Xray на RuVDS нет, а поведение finalmask между релизами менялось.
// На RuVDS нет unzip — распаковка через python3.
func InstallXrayRuVDS() error {
	client, err := sshConnect()
	if err != nil {
		return fmt.Errorf("SSH: %w", err)
	}
	defer client.Close()

	out, _ := runSSH(client, fmt.Sprintf("%s version 2>/dev/null | head -1; true", XrayRuVDSBinaryPath))
	if strings.Contains(out, "Xray "+XrayVersion+" ") {
		log.Println("Xray на RuVDS уже установлен:", strings.TrimSpace(out))
		return nil
	}

	url := fmt.Sprintf("https://github.com/XTLS/Xray-core/releases/download/v%s/Xray-linux-64.zip", XrayVersion)
	cmd := fmt.Sprintf(
		"set -e; cd /tmp && rm -rf xray-dl && mkdir xray-dl && cd xray-dl && "+
			"wget -qO xray.zip --tries=3 '%s' && "+
			"echo '%s  xray.zip' | sha256sum -c - && "+
			"python3 -c \"import zipfile; zipfile.ZipFile('xray.zip').extractall(members=['xray','geoip.dat','geosite.dat'])\" && "+
			"install -m 0755 xray %s && mkdir -p /usr/local/share/xray && mv -f geoip.dat geosite.dat /usr/local/share/xray/ && "+
			"cd /tmp && rm -rf xray-dl",
		url, XrayLinux64SHA256, XrayRuVDSBinaryPath)
	if output, err := runSSH(client, cmd); err != nil {
		return fmt.Errorf("установка Xray на RuVDS: %w: %s", err, output)
	}
	log.Println("Xray установлен на RuVDS:", XrayRuVDSBinaryPath, XrayVersion)
	return nil
}

// EnsureXrayRuVDSService — systemd unit. Xray не перечитывает конфиг по
// SIGHUP, поэтому ExecReload нет: деплой делает restart.
func EnsureXrayRuVDSService() error {
	client, err := sshConnect()
	if err != nil {
		return fmt.Errorf("SSH: %w", err)
	}
	defer client.Close()

	unit := fmt.Sprintf(`[Unit]
Description=Xray finalmask sidecar (managed by vpnbot)
After=network-online.target nss-lookup.target
Wants=network-online.target

[Service]
Type=simple
Environment=XRAY_LOCATION_ASSET=/usr/local/share/xray
ExecStart=%s run -c %s
Restart=always
RestartSec=5
LimitNOFILE=65536
AmbientCapabilities=CAP_NET_BIND_SERVICE
CapabilityBoundingSet=CAP_NET_BIND_SERVICE

[Install]
WantedBy=multi-user.target
`, XrayRuVDSBinaryPath, XrayRuVDSConfigPath)

	cmd := fmt.Sprintf("mkdir -p %s && cat > %s << 'UNITEOF'\n%sUNITEOF",
		XrayRuVDSConfigDir, XrayRuVDSServicePath, unit)
	if _, err := runSSH(client, cmd); err != nil {
		return fmt.Errorf("запись systemd unit: %w", err)
	}
	if _, err := runSSH(client, "systemctl daemon-reload"); err != nil {
		return fmt.Errorf("daemon-reload: %w", err)
	}
	log.Println("systemd unit xray создан на RuVDS")
	return nil
}

// GenerateXrayRuVDSConfig — конфиг Xray из enabled-инбаундов БД.
// nil, nil — mask-инбаундов нет.
func GenerateXrayRuVDSConfig() ([]byte, error) {
	var inbounds []database.InboundConfig
	database.DB.Where("enabled = ?", true).Order("sort_order").Find(&inbounds)
	return buildXrayConfig(inbounds)
}

// DeployXrayConfigRuVDS — пишет config.json и рестартует сервис.
// cfgJSON == nil означает «масок нет»: сервис останавливается, конфиг не
// трогаем. Если сервис не установлен (setup не делали), а маски есть —
// restart упадёт, и это ожидаемая ошибка: нужен POST /api/xray/ruvds/setup.
func DeployXrayConfigRuVDS(cfgJSON []byte) error {
	client, err := sshConnect()
	if err != nil {
		return fmt.Errorf("SSH: %w", err)
	}
	defer client.Close()

	if cfgJSON == nil {
		runSSH(client, fmt.Sprintf("systemctl stop %s 2>/dev/null; true", XrayRuVDSServiceName))
		return nil
	}

	cmd := fmt.Sprintf("mkdir -p %s && cat > %s << 'CFGEOF'\n%s\nCFGEOF",
		XrayRuVDSConfigDir, XrayRuVDSConfigPath, string(cfgJSON))
	if out, err := runSSH(client, cmd); err != nil {
		return fmt.Errorf("запись config.json: %w: %s", err, out)
	}
	if out, err := runSSH(client, fmt.Sprintf("%s run -test -c %s 2>&1 | tail -1",
		XrayRuVDSBinaryPath, XrayRuVDSConfigPath)); err != nil || !strings.Contains(out, "Configuration OK") {
		return fmt.Errorf("xray -test отверг конфиг: %s", strings.TrimSpace(out))
	}
	if out, err := runSSH(client, fmt.Sprintf("systemctl restart %s", XrayRuVDSServiceName)); err != nil {
		return fmt.Errorf("restart xray: %w: %s", err, out)
	}
	return nil
}

func StartXrayRuVDS() error {
	client, err := sshConnect()
	if err != nil {
		return fmt.Errorf("SSH: %w", err)
	}
	defer client.Close()
	if _, err := runSSH(client, fmt.Sprintf("systemctl start %s", XrayRuVDSServiceName)); err != nil {
		return fmt.Errorf("start: %w", err)
	}
	runSSH(client, fmt.Sprintf("systemctl enable %s", XrayRuVDSServiceName))
	return nil
}

func StopXrayRuVDS() error {
	client, err := sshConnect()
	if err != nil {
		return fmt.Errorf("SSH: %w", err)
	}
	defer client.Close()
	_, err = runSSH(client, fmt.Sprintf("systemctl stop %s", XrayRuVDSServiceName))
	return err
}

func IsXrayRuVDSRunning() bool {
	client, err := sshConnect()
	if err != nil {
		return false
	}
	defer client.Close()
	out, _ := runSSH(client, fmt.Sprintf("systemctl is-active %s 2>/dev/null; true", XrayRuVDSServiceName))
	return strings.TrimSpace(out) == "active"
}

// XrayRuVDSLogs — последние N строк журнала xray на RuVDS.
func XrayRuVDSLogs(lines int) (string, error) {
	if lines <= 0 {
		lines = 50
	}
	client, err := sshConnect()
	if err != nil {
		return "", fmt.Errorf("SSH: %w", err)
	}
	defer client.Close()
	out, err := runSSH(client, fmt.Sprintf(
		"journalctl -u %s -n %d --no-pager 2>&1; echo '---'; systemctl status %s --no-pager 2>&1; true",
		XrayRuVDSServiceName, lines, XrayRuVDSServiceName))
	if err != nil {
		return out, fmt.Errorf("journalctl: %w", err)
	}
	return out, nil
}

// openRuVDSMaskPorts — ufw allow для портов всех enabled mask-инбаундов.
func openRuVDSMaskPorts() {
	var inbounds []database.InboundConfig
	database.DB.Where("enabled = ? AND protocol = ?", true, "mask").Find(&inbounds)
	if len(inbounds) == 0 {
		return
	}
	client, err := sshConnect()
	if err != nil {
		return
	}
	defer client.Close()
	for _, ib := range inbounds {
		runSSH(client, fmt.Sprintf("ufw allow %d/tcp comment 'xray mask RuVDS' 2>/dev/null; true", ib.ListenPort))
	}
}

// SetupXrayRuVDS — полный цикл: install, unit, конфиг, порты, start.
// ВНИМАНИЕ: меняет состояние чужой машины. Вызывать только явно
// (POST /api/xray/ruvds/setup).
func SetupXrayRuVDS() error {
	if !IsRuVDSEnabled() {
		return fmt.Errorf("xray RuVDS: WG не включён")
	}
	if !IsPortForwardConfigured() {
		return fmt.Errorf("xray RuVDS: RUVDS_IP не задан")
	}
	if err := InstallXrayRuVDS(); err != nil {
		return err
	}
	if err := EnsureXrayRuVDSService(); err != nil {
		return err
	}
	cfgJSON, err := GenerateXrayRuVDSConfig()
	if err != nil {
		return fmt.Errorf("генерация config.json: %w", err)
	}
	if cfgJSON == nil {
		log.Println("xray RuVDS: enabled mask-инбаундов нет, сервис не запускаем")
		return nil
	}
	if err := DeployXrayConfigRuVDS(cfgJSON); err != nil {
		return err
	}
	openRuVDSMaskPorts()
	return StartXrayRuVDS()
}
