package service

import (
	"fmt"
	"log"
	"strings"
	"vpnbot/database"
)

const (
	SingboxRuVDSBinaryPath  = "/usr/local/bin/sing-box"
	SingboxRuVDSConfigPath  = "/etc/sing-box/config.json"
	SingboxRuVDSServiceName = "sing-box"
	SingboxRuVDSServicePath = "/etc/systemd/system/sing-box.service"
	// Версия для свежей установки. Должна быть ≥1.11 для xhttp-транспорта,
	// который у нас используется в одном из inbounds. 1.13.11 — последний
	// стабильный релиз SagerNet с нативной поддержкой xhttp.
	SingboxRuVDSVersion = "1.13.11"
	SingboxRuVDSArch    = "linux-amd64"
)

// InstallSingboxRuVDS — скачивает sing-box на RuVDS через SSH. Идемпотентна.
// Skip-условие: если бинарь уже есть и `sing-box version` отрабатывает,
// не трогаем (на проде может быть custom-build с xhttp-патчем, который мы
// не хотим перезаписывать на vanilla-релиз).
func InstallSingboxRuVDS() error {
	client, err := sshConnect()
	if err != nil {
		return fmt.Errorf("SSH: %w", err)
	}
	defer client.Close()

	out, runErr := runSSH(client, fmt.Sprintf("%s version 2>&1 | head -1", SingboxRuVDSBinaryPath))
	if runErr == nil && strings.Contains(out, "sing-box version") {
		log.Println("sing-box на RuVDS уже установлен:", strings.TrimSpace(out))
		return nil
	}

	url := fmt.Sprintf(
		"https://github.com/SagerNet/sing-box/releases/download/v%s/sing-box-%s-%s.tar.gz",
		SingboxRuVDSVersion, SingboxRuVDSVersion, SingboxRuVDSArch)
	extractedDir := fmt.Sprintf("sing-box-%s-%s", SingboxRuVDSVersion, SingboxRuVDSArch)

	cmd := fmt.Sprintf(
		"cd /tmp && wget -qO sb.tgz --tries=3 '%s' && tar -xzf sb.tgz && "+
			"mv %s/sing-box %s && chmod +x %s && rm -rf sb.tgz %s",
		url, extractedDir, SingboxRuVDSBinaryPath, SingboxRuVDSBinaryPath, extractedDir)

	if output, err := runSSH(client, cmd); err != nil {
		return fmt.Errorf("установка sing-box на RuVDS: %w: %s", err, output)
	}
	log.Println("sing-box установлен на RuVDS:", SingboxRuVDSBinaryPath)
	return nil
}

// EnsureSingboxRuVDSService — создаёт systemd unit для sing-box на RuVDS.
func EnsureSingboxRuVDSService() error {
	client, err := sshConnect()
	if err != nil {
		return fmt.Errorf("SSH: %w", err)
	}
	defer client.Close()

	unit := fmt.Sprintf(`[Unit]
Description=sing-box service (managed by vpnbot)
After=network-online.target nss-lookup.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=%s run -c %s
ExecReload=/bin/kill -HUP $MAINPID
Restart=on-failure
RestartSec=5
LimitNOFILE=65536
AmbientCapabilities=CAP_NET_ADMIN CAP_NET_BIND_SERVICE
CapabilityBoundingSet=CAP_NET_ADMIN CAP_NET_BIND_SERVICE

[Install]
WantedBy=multi-user.target
`, SingboxRuVDSBinaryPath, SingboxRuVDSConfigPath)

	cmd := fmt.Sprintf("mkdir -p /etc/sing-box && cat > %s << 'UNITEOF'\n%sUNITEOF",
		SingboxRuVDSServicePath, unit)
	if _, err := runSSH(client, cmd); err != nil {
		return fmt.Errorf("запись systemd unit: %w", err)
	}
	if _, err := runSSH(client, "systemctl daemon-reload"); err != nil {
		return fmt.Errorf("daemon-reload: %w", err)
	}
	log.Println("systemd unit sing-box создан на RuVDS")
	return nil
}

// DeploySingboxConfigRuVDS — записывает /etc/sing-box/config.json на RuVDS и reload.
func DeploySingboxConfigRuVDS(cfgJSON []byte) error {
	client, err := sshConnect()
	if err != nil {
		return fmt.Errorf("SSH: %w", err)
	}
	defer client.Close()

	cmd := fmt.Sprintf("mkdir -p /etc/sing-box && cat > %s << 'CFGEOF'\n%s\nCFGEOF",
		SingboxRuVDSConfigPath, string(cfgJSON))
	if out, err := runSSH(client, cmd); err != nil {
		return fmt.Errorf("запись config.json: %w: %s", err, out)
	}

	out, _ := runSSH(client, fmt.Sprintf("systemctl is-active --quiet %s && echo active",
		SingboxRuVDSServiceName))
	if strings.TrimSpace(out) == "active" {
		if _, err := runSSH(client, fmt.Sprintf("systemctl reload %s", SingboxRuVDSServiceName)); err != nil {
			log.Printf("sing-box RuVDS reload не сработал, пробуем restart: %v", err)
			runSSH(client, fmt.Sprintf("systemctl restart %s", SingboxRuVDSServiceName))
		}
	}
	return nil
}

func StartSingboxRuVDS() error {
	client, err := sshConnect()
	if err != nil {
		return fmt.Errorf("SSH: %w", err)
	}
	defer client.Close()
	if _, err := runSSH(client, fmt.Sprintf("systemctl start %s", SingboxRuVDSServiceName)); err != nil {
		return fmt.Errorf("start: %w", err)
	}
	runSSH(client, fmt.Sprintf("systemctl enable %s", SingboxRuVDSServiceName))
	return nil
}

func StopSingboxRuVDS() error {
	client, err := sshConnect()
	if err != nil {
		return fmt.Errorf("SSH: %w", err)
	}
	defer client.Close()
	_, err = runSSH(client, fmt.Sprintf("systemctl stop %s", SingboxRuVDSServiceName))
	return err
}

func IsSingboxRuVDSRunning() bool {
	client, err := sshConnect()
	if err != nil {
		return false
	}
	defer client.Close()
	// `systemctl is-active <svc>` всегда выводит state в stdout ("active"/"inactive"/"failed"/...).
	// `; true` гарантирует exit 0, чтобы runSSH вернул output без ошибки.
	out, _ := runSSH(client, fmt.Sprintf("systemctl is-active %s 2>/dev/null; true", SingboxRuVDSServiceName))
	return strings.TrimSpace(out) == "active"
}

// SingboxRuVDSLogs — последние N строк журнала sing-box на RuVDS.
func SingboxRuVDSLogs(lines int) (string, error) {
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
		SingboxRuVDSServiceName, lines, SingboxRuVDSServiceName))
	if err != nil {
		return out, fmt.Errorf("journalctl: %w", err)
	}
	return out, nil
}

// CleanupRuVDSDNAT — удаляет iptables DNAT правила на RuVDS для VPN-портов.
// ВНИМАНИЕ: разрывает старую схему «Client → RuVDS DNAT → Hetzner». Использовать
// ТОЛЬКО осознанно после того как убедились что RuVDS sing-box работает и
// все клиенты согласны на новую схему. НЕ вызывается из Setup автоматически.
func CleanupRuVDSDNAT() error {
	if !IsPortForwardConfigured() {
		return nil
	}

	var inbounds []database.InboundConfig
	database.DB.Where("enabled = ?", true).Find(&inbounds)
	if len(inbounds) == 0 {
		return nil
	}

	client, err := sshConnect()
	if err != nil {
		return fmt.Errorf("SSH: %w", err)
	}
	defer client.Close()

	hetznerIP := GetHetznerServerIP()
	for _, ib := range inbounds {
		proto := "tcp"
		if ib.Protocol == "hysteria2" {
			proto = "udp"
		}
		cmd := fmt.Sprintf(
			"iptables -t nat -D PREROUTING -p %s --dport %d -j DNAT --to-destination %s:%d 2>/dev/null; "+
				"iptables -t nat -D POSTROUTING -d %s -p %s --dport %d -j MASQUERADE 2>/dev/null; true",
			proto, ib.ListenPort, hetznerIP, ib.ListenPort,
			hetznerIP, proto, ib.ListenPort)
		runSSH(client, cmd)
	}
	persistIptables(client)
	log.Println("CleanupRuVDSDNAT: убраны DNAT-правила для VPN-портов на RuVDS")
	return nil
}

// RestoreRuVDSDNAT — восстанавливает iptables DNAT для всех enabled VPN-портов
// (откат к схеме «Client → RuVDS:port → DNAT → Hetzner»). Идемпотентна.
func RestoreRuVDSDNAT() error {
	if !IsPortForwardConfigured() {
		return fmt.Errorf("RUVDS_IP не задан")
	}

	var inbounds []database.InboundConfig
	database.DB.Where("enabled = ?", true).Find(&inbounds)
	if len(inbounds) == 0 {
		return nil
	}

	var firstErr error
	for _, ib := range inbounds {
		proto := "tcp"
		if ib.Protocol == "hysteria2" {
			proto = "udp"
		}
		if err := AddForward(ib.ListenPort, proto); err != nil {
			log.Printf("RestoreRuVDSDNAT: %d/%s: %v", ib.ListenPort, proto, err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	if firstErr == nil {
		log.Println("RestoreRuVDSDNAT: DNAT-правила восстановлены для всех enabled inbound портов")
	}
	return firstErr
}

// BootstrapSingboxRuVDS — то, что разрешено делать при старте vpnbot.
//
// А именно: ничего не менять. Только посмотреть и записать в лог.
//
// Раньше main.go звал SetupSingboxRuVDS(), и тот при каждом запуске
// перезаписывал config.json на RuVDS, делал systemctl start и enable.
// 29.07.2026 это и произошло: sing-box на RuVDS, который по действующей
// схеме должен был стоять остановленным, оказался запущен и добавлен в
// автозапуск — с конфигом, где route.final указывал на туннель, ни разу
// не передавший ни одного пакета. Пока DNAT перехватывал порты раньше
// локального сокета, подмена ничего не ломала и оставалась незамеченной.
//
// Запуск процесса на чужой машине — это осознанное действие оператора,
// а не побочный эффект перезапуска демона. Поэтому всё изменяющее живёт
// в SetupSingboxRuVDS() и вызывается явно через POST /api/singbox/ruvds/setup.
func BootstrapSingboxRuVDS() error {
	if !IsRuVDSEnabled() {
		log.Println("sing-box RuVDS: WG не включён, пропускаем")
		return nil
	}
	if !IsPortForwardConfigured() {
		log.Println("sing-box RuVDS: RUVDS_IP не задан, пропускаем")
		return nil
	}

	client, err := sshConnect()
	if err != nil {
		log.Printf("sing-box RuVDS: недоступен по SSH (%v) — управление выключено", err)
		return nil
	}
	defer client.Close()

	state, _ := runSSH(client, fmt.Sprintf("systemctl is-active %s 2>/dev/null; true",
		SingboxRuVDSServiceName))
	ver, _ := runSSH(client, fmt.Sprintf("%s version 2>/dev/null | head -1; true",
		SingboxRuVDSBinaryPath))
	log.Printf("sing-box RuVDS: состояние=%s, %s (изменений не вносим)",
		strings.TrimSpace(state), strings.TrimSpace(ver))
	return nil
}

// SetupSingboxRuVDS — полный цикл установки sing-box на RuVDS.
//
// ВНИМАНИЕ: изменяет состояние чужой машины — переписывает config.json,
// открывает порты, запускает и включает сервис. Вызывать только явно.
func SetupSingboxRuVDS() error {
	if !IsRuVDSEnabled() {
		log.Println("sing-box RuVDS: WG не включён, пропускаем")
		return nil
	}
	if !IsPortForwardConfigured() {
		return fmt.Errorf("sing-box RuVDS: RUVDS_IP не задан")
	}

	if err := InstallSingboxRuVDS(); err != nil {
		return err
	}
	if err := EnsureSingboxRuVDSService(); err != nil {
		return err
	}
	// ВАЖНО: не вызываем CleanupRuVDSDNAT здесь, чтобы не сломать старую схему.
	// DNAT остаётся, RuVDS sing-box слушает порты, но трафик к старым портам
	// всё равно идёт через DNAT (PREROUTING срабатывает до local listen).
	// Чтобы клиенты пошли через RuVDS sing-box, выпустите им /sub-ruvds/:token
	// (там по умолчанию ссылки идут на RuVDS:port, и пока DNAT в pre-routing
	// перехватывает — нужно перенаправить sing-box на другой порт или
	// удалить DNAT для этого порта осознанно через CleanupRuVDSDNAT).

	cfgJSON, err := GenerateRuVDSConfig()
	if err != nil {
		return fmt.Errorf("генерация config.json: %w", err)
	}
	if err := DeploySingboxConfigRuVDS(cfgJSON); err != nil {
		return err
	}

	// Открываем VPN-порты в ufw на RuVDS
	openRuVDSVPNPorts()

	if err := StartSingboxRuVDS(); err != nil {
		return err
	}
	return nil
}

// openRuVDSVPNPorts — открывает в ufw на RuVDS порты всех enabled inbounds.
func openRuVDSVPNPorts() {
	var inbounds []database.InboundConfig
	database.DB.Where("enabled = ?", true).Find(&inbounds)
	if len(inbounds) == 0 {
		return
	}
	client, err := sshConnect()
	if err != nil {
		return
	}
	defer client.Close()
	for _, ib := range inbounds {
		proto := "tcp"
		if ib.Protocol == "hysteria2" {
			proto = "udp"
		}
		runSSH(client, fmt.Sprintf("ufw allow %d/%s comment 'sing-box RuVDS' 2>/dev/null; true",
			ib.ListenPort, proto))
	}
}
