package service

import (
	"fmt"
	"log"
	"os/exec"
	"strings"
	"vpnbot/database"
)

// Xray-XDNS — второй Xray-инстанс, живущий ЛОКАЛЬНО на Hetzner (там же, где
// сам vpnbot). Слушает :53, VLESS+mKCP+finalmask xdns. В отличие от
// xrayruvds.go (деплой по SSH на другую машину) все команды здесь идут
// локально через os/exec — сервер деплоит сам себя.
const (
	XrayXDNSBinaryPath  = "/usr/local/bin/xray"
	XrayXDNSConfigDir   = "/etc/xray-xdns"
	XrayXDNSConfigPath  = "/etc/xray-xdns/config.json"
	XrayXDNSServiceName = "xray-xdns"
	XrayXDNSServicePath = "/etc/systemd/system/xray-xdns.service"
)

// runLocal — локальная команда через shell, объединённый stdout+stderr.
func runLocal(cmd string) (string, error) {
	out, err := exec.Command("bash", "-c", cmd).CombinedOutput()
	return string(out), err
}

// XrayXDNSVersion — строка `xray version` на Hetzner ("" если не установлен).
func XrayXDNSVersion() string {
	out, _ := runLocal(XrayXDNSBinaryPath + " version 2>/dev/null | head -1")
	return strings.TrimSpace(out)
}

// InstallXrayHetzner — ставит запиненную версию Xray на Hetzner. Идемпотентна:
// если `xray version` уже показывает XrayVersion — ничего не делает. Хетзнер
// достаёт GitHub напрямую (не через CDN, как RuVDS), но команда идентична
// InstallXrayRuVDS — только выполняется локально.
func InstallXrayHetzner() error {
	out, _ := runLocal(XrayXDNSBinaryPath + " version 2>/dev/null | head -1")
	if strings.Contains(out, "Xray "+XrayVersion+" ") {
		log.Println("Xray на Hetzner уже установлен:", strings.TrimSpace(out))
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
		url, XrayLinux64SHA256, XrayXDNSBinaryPath)
	if output, err := runLocal(cmd); err != nil {
		return fmt.Errorf("установка Xray на Hetzner: %w: %s", err, output)
	}
	log.Println("Xray установлен на Hetzner:", XrayXDNSBinaryPath, XrayVersion)
	return nil
}

// EnsureXrayXDNSService — systemd unit. Xray не перечитывает конфиг по
// SIGHUP, поэтому ExecReload нет: деплой делает restart.
func EnsureXrayXDNSService() error {
	unit := fmt.Sprintf(`[Unit]
Description=Xray XDNS channel (managed by vpnbot)
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
`, XrayXDNSBinaryPath, XrayXDNSConfigPath)

	cmd := fmt.Sprintf("mkdir -p %s && cat > %s << 'UNITEOF'\n%sUNITEOF",
		XrayXDNSConfigDir, XrayXDNSServicePath, unit)
	if out, err := runLocal(cmd); err != nil {
		return fmt.Errorf("запись systemd unit: %w: %s", err, out)
	}
	if out, err := runLocal("systemctl daemon-reload"); err != nil {
		return fmt.Errorf("daemon-reload: %w: %s", err, out)
	}
	log.Println("systemd unit xray-xdns создан на Hetzner")
	return nil
}

// GenerateXDNSConfig — конфиг Xray-XDNS из enabled xdns-инбаундов и активных
// пользователей БД. nil, nil — xdns-инбаундов нет.
func GenerateXDNSConfig() ([]byte, error) {
	var inbounds []database.InboundConfig
	database.DB.Where("enabled = ?", true).Order("sort_order").Find(&inbounds)

	var users []database.User
	database.DB.Where("status = ?", "active").Find(&users)

	return buildXrayXDNSConfig(inbounds, users)
}

// HasXDNSInbounds — есть ли в БД хотя бы одна запись Protocol="xdns", в любом
// состоянии Enabled. Используется чтобы не гонять деплой XDNS на каждый
// reload, если xdns-инбаундов в системе вообще не заведено.
func HasXDNSInbounds() bool {
	var count int64
	database.DB.Model(&database.InboundConfig{}).Where("protocol = ?", "xdns").Count(&count)
	return count > 0
}

// DeployXDNSConfig — пишет config.json и рестартует сервис. cfgJSON == nil
// означает «xdns-инбаундов нет»: сервис останавливается, конфиг не трогаем.
//
// Конфиг сначала пишется во временный config.json.new и проверяется
// `xray run -test`; только если он реально отличается от текущего
// config.json, файл подменяется. Restart дёргается ТОЛЬКО когда конфиг
// изменился И сервис уже active — иначе деплой на каждый reload перезапускал
// бы Xray без причины (рвал активные UDP-сессии клиентов) и, что хуже,
// поднимал бы Xray обратно после явного `systemctl stop` (Setup/Start —
// единственные места, где запуск сервиса — осознанное действие).
func DeployXDNSConfig(cfgJSON []byte) error {
	if cfgJSON == nil {
		runLocal(fmt.Sprintf("systemctl stop %s 2>/dev/null; true", XrayXDNSServiceName))
		return nil
	}

	if out, _ := runLocal(fmt.Sprintf("test -x %s && echo ok", XrayXDNSBinaryPath)); strings.TrimSpace(out) != "ok" {
		return fmt.Errorf("Xray не установлен: POST /api/xray/xdns/setup")
	}

	newPath := XrayXDNSConfigPath + ".new"
	cmd := fmt.Sprintf("mkdir -p %s && cat > %s << 'CFGEOF'\n%s\nCFGEOF",
		XrayXDNSConfigDir, newPath, string(cfgJSON))
	if out, err := runLocal(cmd); err != nil {
		return fmt.Errorf("запись config.json.new: %w: %s", err, out)
	}

	// -format json обязателен: xray определяет формат по расширению файла, а у
	// временного config.json.new его нет — без флага падает с "Failed to get format".
	testOut, testErr := runLocal(fmt.Sprintf("%s run -test -format json -c %s 2>&1 | tail -1",
		XrayXDNSBinaryPath, newPath))
	if testErr != nil || !strings.Contains(testOut, "Configuration OK") {
		runLocal(fmt.Sprintf("rm -f %s", newPath))
		return fmt.Errorf("xray -test отверг конфиг: %s", strings.TrimSpace(testOut))
	}

	// Меняем местами только если конфиг реально другой; иначе просто убираем .new.
	swapCmd := fmt.Sprintf(
		"if cmp -s %s %s; then rm -f %s; echo unchanged; else mv -f %s %s; echo changed; fi",
		newPath, XrayXDNSConfigPath, newPath, newPath, XrayXDNSConfigPath)
	swapOut, err := runLocal(swapCmd)
	if err != nil {
		return fmt.Errorf("замена config.json: %w: %s", err, swapOut)
	}
	if strings.TrimSpace(swapOut) != "changed" {
		return nil
	}

	activeOut, _ := runLocal(fmt.Sprintf("systemctl is-active --quiet %s && echo active",
		XrayXDNSServiceName))
	if strings.TrimSpace(activeOut) == "active" {
		if out, err := runLocal(fmt.Sprintf("systemctl restart %s", XrayXDNSServiceName)); err != nil {
			return fmt.Errorf("restart xray-xdns: %w: %s", err, out)
		}
	}
	return nil
}

func StartXrayXDNS() error {
	if out, err := runLocal(fmt.Sprintf("systemctl start %s", XrayXDNSServiceName)); err != nil {
		return fmt.Errorf("start: %w: %s", err, out)
	}
	runLocal(fmt.Sprintf("systemctl enable %s", XrayXDNSServiceName))
	return nil
}

func StopXrayXDNS() error {
	out, err := runLocal(fmt.Sprintf("systemctl stop %s", XrayXDNSServiceName))
	if err != nil {
		return fmt.Errorf("stop: %w: %s", err, out)
	}
	return nil
}

func IsXrayXDNSRunning() bool {
	out, _ := runLocal(fmt.Sprintf("systemctl is-active %s 2>/dev/null; true", XrayXDNSServiceName))
	return strings.TrimSpace(out) == "active"
}

// XrayXDNSLogs — последние N строк журнала xray-xdns на Hetzner.
func XrayXDNSLogs(lines int) (string, error) {
	if lines <= 0 {
		lines = 50
	}
	out, err := runLocal(fmt.Sprintf(
		"journalctl -u %s -n %d --no-pager 2>&1; echo '---'; systemctl status %s --no-pager 2>&1; true",
		XrayXDNSServiceName, lines, XrayXDNSServiceName))
	if err != nil {
		return out, fmt.Errorf("journalctl: %w", err)
	}
	return out, nil
}

// Port53Owner — диагностика: кто (если кто-то) слушает :53 на Hetzner.
// Пустая строка — порт свободен.
func Port53Owner() string {
	out, _ := runLocal("ss -lunp | grep ':53 ' | head -1")
	return strings.TrimSpace(out)
}

// parseVlessencPair — вытаскивает пару decryption/encryption из вывода
// `xray vlessenc`. Команда печатает несколько секций (Post-Quantum ML-KEM
// среди них); нужная — первая после строки "Authentication: X25519".
// Внутри секции ищем строки вида `"decryption": "..."` / `"encryption": "..."`
// и берём значение в кавычках.
func parseVlessencPair(out string) (decryption, encryption string, err error) {
	lines := strings.Split(out, "\n")
	start := -1
	for i, l := range lines {
		if strings.Contains(l, "Authentication: X25519") {
			start = i
			break
		}
	}
	if start == -1 {
		return "", "", fmt.Errorf("секция \"Authentication: X25519\" не найдена в выводе xray vlessenc")
	}
	for _, l := range lines[start:] {
		if decryption == "" && strings.Contains(l, `"decryption":`) {
			decryption = extractQuotedValue(l)
		}
		if encryption == "" && strings.Contains(l, `"encryption":`) {
			encryption = extractQuotedValue(l)
		}
		if decryption != "" && encryption != "" {
			break
		}
	}
	if decryption == "" || encryption == "" {
		return "", "", fmt.Errorf("не удалось распарсить decryption/encryption из вывода xray vlessenc")
	}
	return decryption, encryption, nil
}

// extractQuotedValue — из строки вида `  "decryption": "mlkem...",` вытаскивает
// значение после двоеточия, заключённое в кавычки.
func extractQuotedValue(line string) string {
	idx := strings.Index(line, ":")
	if idx == -1 {
		return ""
	}
	rest := strings.TrimSpace(line[idx+1:])
	rest = strings.TrimSuffix(rest, ",")
	rest = strings.TrimSpace(rest)
	if len(rest) < 2 || rest[0] != '"' {
		return ""
	}
	rest = rest[1:]
	end := strings.Index(rest, `"`)
	if end == -1 {
		return ""
	}
	return rest[:end]
}

// EnsureXDNSKeys — для каждого enabled xdns-инбаунда с пустым XDNSDecryption
// генерирует пару VLESS-ключей через `xray vlessenc` и сохраняет в БД.
// Требует установленного бинаря — вызывать после InstallXrayHetzner.
func EnsureXDNSKeys() error {
	var inbounds []database.InboundConfig
	database.DB.Where("enabled = ? AND protocol = ?", true, "xdns").Find(&inbounds)

	for _, ib := range inbounds {
		if ib.XDNSDecryption != "" {
			continue
		}
		out, err := runLocal(XrayXDNSBinaryPath + " vlessenc")
		if err != nil {
			return fmt.Errorf("xdns %q: xray vlessenc: %w: %s", ib.Tag, err, out)
		}
		dec, enc, perr := parseVlessencPair(out)
		if perr != nil {
			return fmt.Errorf("xdns %q: %w", ib.Tag, perr)
		}
		if err := database.DB.Model(&ib).Updates(map[string]any{
			"xdns_decryption": dec,
			"xdns_encryption": enc,
		}).Error; err != nil {
			return fmt.Errorf("xdns %q: сохранение ключей: %w", ib.Tag, err)
		}
		log.Println("Сгенерированы VLESS-ключи для xdns-инбаунда:", ib.Tag)
	}
	return nil
}

// SetupXrayXDNS — полный цикл: install, unit, ключи, конфиг, start.
// ВНИМАНИЕ: меняет состояние сервера. Вызывать только явно
// (POST /api/xray/xdns/setup).
func SetupXrayXDNS() error {
	if err := InstallXrayHetzner(); err != nil {
		return err
	}
	if err := EnsureXrayXDNSService(); err != nil {
		return err
	}
	if err := EnsureXDNSKeys(); err != nil {
		return fmt.Errorf("генерация VLESS-ключей: %w", err)
	}
	cfgJSON, err := GenerateXDNSConfig()
	if err != nil {
		return fmt.Errorf("генерация config.json: %w", err)
	}
	if cfgJSON == nil {
		return fmt.Errorf("нет enabled xdns-инбаундов")
	}
	if !IsXrayXDNSRunning() {
		if owner := Port53Owner(); strings.TrimSpace(owner) != "" {
			return fmt.Errorf("порт 53 занят другим процессом (%s): освободите его (остановите slipstream, снимите REDIRECT 53→5300) и повторите", strings.TrimSpace(owner))
		}
	}
	if err := DeployXDNSConfig(cfgJSON); err != nil {
		return err
	}
	return StartXrayXDNS()
}
