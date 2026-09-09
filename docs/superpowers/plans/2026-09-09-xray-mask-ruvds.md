# Xray TCP-маска на RuVDS (этап 1) — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Инбаунд `Protocol="mask"`: Xray на RuVDS снимает finalmask (sudoku/header-custom) с TCP-потока и отдаёт его в существующий VLESS-Reality инбаунд sing-box на loopback; ссылка для Xray-клиентов с параметром `fm`.

**Architecture:** Xray — сайдкар рядом с sing-box, ничего не знает о пользователях и ключах. Новая чистая функция `buildXrayConfig()` строит JSON Xray из mask-инбаундов; `service/xrayruvds.go` копирует паттерн `singboxruvds.go` (SSH: install, unit, deploy, start/stop/status/logs). Sing-box генераторы и Hetzner-подписка mask-инбаунды пропускают. Ссылка = ссылка внутреннего инбаунда с подменённым портом и `fm=<URL-encoded JSON>`.

**Tech Stack:** Go 1.21, GORM/SQLite, gin, telebot.v3, Xray-core v26.9.9 (linux-64 zip), SSH через `sshConnect()/runSSH()` из `service/portforward.go`.

**Spec:** `docs/superpowers/specs/2026-09-09-xray-finalmask-design.md` (результаты спайка — `docs/xray-finalmask-spike.md`).

## Global Constraints

- Xray-core пин: `26.9.9`, sha256 `Xray-linux-64.zip` = `1eb9175d0f0a8f8149c9230a7fc5ae66ce332ed20a53155ce61fe62e3f58b7df`.
- На RuVDS нет `unzip` — распаковка через `python3 -m zipfile`.
- Xray не умеет hot-reload конфига: деплой = `systemctl restart xray`.
- Mask-инбаунд: только TCP; внутренний инбаунд — `Protocol="vless"`, `Transport=""`, enabled.
- Mask-инбаунды НЕ попадают в sing-box конфиги (Hetzner и RuVDS) и НЕ отдаются через `/sub/:token`; отдаются только через `/sub-ruvds/:token` и бота с адресом RuVDS.
- Комментарии в коде и тексты бота — по-русски; ответы API — по-английски (как в репозитории).
- Коммиты: `go vet ./... && go test ./service/ -v` зелёные перед каждым коммитом.
- Не трогать существующие DNAT/REDIRECT правила на RuVDS; порт маски не должен совпадать с портами, которые nft в PREROUTING перенаправляет (2053–2058, 4443, 8444, 8447, 9443). Seed использует 2071.

---

### Task 1: Модель — поля mask-инбаунда и seed

**Files:**
- Modify: `database/database.go` (struct `InboundConfig` ~строки 213–256; функция seed после блока `shadowtlsSeed` ~строка 451)

**Interfaces:**
- Produces: поля `InboundConfig.MaskInnerTag string` (json `mask_inner_tag`), `InboundConfig.MaskJSON string` (json `mask_json`); значение `Protocol == "mask"`.

- [ ] **Step 1: Добавить поля в struct**

В `database/database.go` после блока ShadowTLS-полей (после `InnerPassword string ...`) добавить:

```go
	// Finalmask-обёртка (Protocol="mask"): Xray на RuVDS снимает маску и
	// форвардит голый поток в VLESS-инбаунд sing-box с тегом MaskInnerTag
	// на 127.0.0.1:<его ListenPort>. MaskJSON — блок streamSettings.finalmask
	// Xray как есть; он же уходит в ссылку параметром fm.
	MaskInnerTag string `json:"mask_inner_tag"`
	MaskJSON     string `gorm:"type:text" json:"mask_json"`
```

И поправить комментарий поля `Protocol`:

```go
	Protocol      string `json:"protocol"` // "vless" | "hysteria2" | "shadowtls" | "mask"
```

- [ ] **Step 2: Добавить seed, который создаётся и на существующей БД**

Сразу после `DB.Create(&shadowtlsSeed)` и закрывающей `}` того блока (перед `var hc HealthConfig`) добавить:

```go
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
```

- [ ] **Step 3: Собрать и провести vet**

Run: `go build -o /dev/null . && go vet ./...`
Expected: без ошибок (AutoMigrate добавит колонки при старте).

- [ ] **Step 4: Commit**

```bash
git add database/database.go
git commit -m "feat(db): поля mask-инбаунда и seed RU-MASK"
```

---

### Task 2: Sing-box генераторы пропускают mask-инбаунды

**Files:**
- Modify: `service/vpn.go` (`buildInboundGroup` ~строка 191; цикл в `GenerateRuVDSConfig` ~строка 460)
- Test: `service/vpn_test.go`

**Interfaces:**
- Consumes: `Protocol == "mask"` из Task 1.
- Produces: `buildInboundGroup(ib, users)` возвращает пустой срез для mask; `buildSingBoxConfig` и `GenerateRuVDSConfig` не эмитят ни инбаунд, ни route-rule для mask.

- [ ] **Step 1: Написать падающие тесты**

В конец `service/vpn_test.go`:

```go
func TestBuildInboundGroup_MaskReturnsNothing(t *testing.T) {
	ib := database.InboundConfig{Tag: "RU-MASK", Protocol: "mask", ListenPort: 2071, MaskInnerTag: "vless-direct-tcp"}
	group := buildInboundGroup(ib, nil)
	if len(group) != 0 {
		t.Fatalf("mask inbound must not produce sing-box inbounds, got %d", len(group))
	}
}

func TestBuildSingBoxConfig_MaskNotEmitted(t *testing.T) {
	inner := database.InboundConfig{Tag: "vless-direct-tcp", Protocol: "vless", ListenPort: 2060, TLSType: "reality", ExitOutbound: "direct"}
	mask := database.InboundConfig{Tag: "RU-MASK", Protocol: "mask", ListenPort: 2071, MaskInnerTag: "vless-direct-tcp", ExitOutbound: "direct"}
	cfg := buildSingBoxConfig([]database.InboundConfig{inner, mask}, nil, nil, "")
	b, _ := json.Marshal(cfg)
	got := string(b)
	if strings.Contains(got, `"RU-MASK"`) {
		t.Fatalf("mask inbound leaked into sing-box config: %s", got)
	}
	if strings.Contains(got, `"listen_port":2071`) {
		t.Fatalf("mask port leaked into sing-box config: %s", got)
	}
	if !strings.Contains(got, `"listen_port":2060`) {
		t.Fatalf("inner inbound must stay, got: %s", got)
	}
}
```

- [ ] **Step 2: Запустить, убедиться что падают**

Run: `go test ./service/ -run 'Mask' -v`
Expected: FAIL — `buildInboundGroup` вернул 1 элемент; в конфиге есть `RU-MASK`.

- [ ] **Step 3: Реализовать пропуск**

В `buildInboundGroup`:

```go
func buildInboundGroup(ib database.InboundConfig, users []database.User) []any {
	// mask обслуживает Xray (service/xray.go), sing-box о нём не знает.
	if ib.Protocol == "mask" {
		return []any{}
	}
	if ib.Protocol == "shadowtls" {
		return buildShadowTLSGroup(ib, users)
	}
	return []any{buildSingboxInbound(ib, users)}
}
```

Route-rule для mask не должен появляться. Найти в `buildSingBoxConfig` и в `GenerateRuVDSConfig` место, где `if ib.ExitOutbound != ""` добавляет `RouteRule`, и в обоих циклах перед этим добавить:

```go
		if ib.Protocol == "mask" {
			continue
		}
```

(в начале тела цикла `for _, ib := range inbounds`, до вызова `buildInboundGroup`).

- [ ] **Step 4: Прогнать тесты**

Run: `go test ./service/ -v`
Expected: PASS, включая старые shadowtls-тесты.

- [ ] **Step 5: Commit**

```bash
git add service/vpn.go service/vpn_test.go
git commit -m "feat(singbox): mask-инбаунды не попадают в конфиг sing-box"
```

---

### Task 3: Чистый генератор конфига Xray

**Files:**
- Create: `service/xray.go`
- Test: `service/xray_test.go`

**Interfaces:**
- Consumes: `database.InboundConfig` с полями Task 1.
- Produces:
  - `const XrayVersion = "26.9.9"`, `const XrayLinux64SHA256 = "1eb9175d…b7df"`.
  - `func ValidateMaskJSON(s string) error` — JSON-объект с непустым массивом `tcp`.
  - `func buildXrayConfig(inbounds []database.InboundConfig) ([]byte, error)` — принимает список enabled-инбаундов (любых протоколов); возвращает `nil, nil` если mask-инбаундов нет; ошибку, если внутренний тег не найден/не vless-TCP или MaskJSON невалиден.

- [ ] **Step 1: Написать падающие тесты**

Создать `service/xray_test.go`:

```go
package service

import (
	"encoding/json"
	"strings"
	"testing"
	"vpnbot/database"
)

const testSudokuMask = `{"tcp":[{"type":"sudoku","settings":{"password":"p","ascii":"prefer_entropy"}}]}`

func maskFixture() (database.InboundConfig, database.InboundConfig) {
	inner := database.InboundConfig{Tag: "vless-direct-tcp", Protocol: "vless", ListenPort: 2060, TLSType: "reality", Enabled: true}
	mask := database.InboundConfig{Tag: "RU-MASK", Protocol: "mask", ListenPort: 2071, MaskInnerTag: "vless-direct-tcp", MaskJSON: testSudokuMask, Enabled: true}
	return inner, mask
}

func TestValidateMaskJSON(t *testing.T) {
	cases := map[string]bool{
		testSudokuMask:                 true,
		`{"tcp":[]}`:                   false,
		`{"udp":[{"type":"noise"}]}`:   false,
		`not json`:                     false,
		``:                             false,
		`{"tcp":[{"type":"sudoku"}],"udp":[]}`: true,
	}
	for in, ok := range cases {
		err := ValidateMaskJSON(in)
		if ok && err != nil {
			t.Errorf("%q: expected valid, got %v", in, err)
		}
		if !ok && err == nil {
			t.Errorf("%q: expected error", in)
		}
	}
}

func TestBuildXrayConfig_NoMaskInbounds(t *testing.T) {
	inner, _ := maskFixture()
	out, err := buildXrayConfig([]database.InboundConfig{inner})
	if err != nil {
		t.Fatal(err)
	}
	if out != nil {
		t.Fatalf("expected nil config when no mask inbounds, got %s", out)
	}
}

func TestBuildXrayConfig_SingleMask(t *testing.T) {
	inner, mask := maskFixture()
	out, err := buildXrayConfig([]database.InboundConfig{inner, mask})
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(out, &cfg); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out)
	}
	inbounds := cfg["inbounds"].([]any)
	if len(inbounds) != 1 {
		t.Fatalf("expected 1 inbound, got %d", len(inbounds))
	}
	got := string(out)
	for _, want := range []string{
		`"tag": "RU-MASK"`,
		`"protocol": "dokodemo-door"`,
		`"listen": "0.0.0.0"`,
		`"port": 2071`,
		`"address": "127.0.0.1"`,
		`"port": 2060`,
		`"network": "tcp"`,
		`"type": "sudoku"`,
		`"protocol": "freedom"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in:\n%s", want, got)
		}
	}
}

func TestBuildXrayConfig_TwoMasks(t *testing.T) {
	inner, mask := maskFixture()
	mask2 := mask
	mask2.Tag = "RU-MASK-2"
	mask2.ListenPort = 2072
	out, err := buildXrayConfig([]database.InboundConfig{inner, mask, mask2})
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	_ = json.Unmarshal(out, &cfg)
	if n := len(cfg["inbounds"].([]any)); n != 2 {
		t.Fatalf("expected 2 inbounds, got %d", n)
	}
}

func TestBuildXrayConfig_InnerMissing(t *testing.T) {
	_, mask := maskFixture()
	if _, err := buildXrayConfig([]database.InboundConfig{mask}); err == nil {
		t.Fatal("expected error when inner inbound is absent")
	}
}

func TestBuildXrayConfig_InnerMustBeVlessTCP(t *testing.T) {
	inner, mask := maskFixture()
	inner.Transport = "xhttp"
	if _, err := buildXrayConfig([]database.InboundConfig{inner, mask}); err == nil {
		t.Fatal("expected error for non-TCP inner inbound")
	}
	inner.Transport = ""
	inner.Protocol = "hysteria2"
	if _, err := buildXrayConfig([]database.InboundConfig{inner, mask}); err == nil {
		t.Fatal("expected error for non-vless inner inbound")
	}
}

func TestBuildXrayConfig_BadMaskJSON(t *testing.T) {
	inner, mask := maskFixture()
	mask.MaskJSON = `{"udp":[]}`
	if _, err := buildXrayConfig([]database.InboundConfig{inner, mask}); err == nil {
		t.Fatal("expected error for mask JSON without tcp")
	}
}
```

- [ ] **Step 2: Запустить, убедиться что не компилируется**

Run: `go test ./service/ -run 'Xray|MaskJSON' -v`
Expected: FAIL — `undefined: buildXrayConfig`, `undefined: ValidateMaskJSON`.

- [ ] **Step 3: Реализовать `service/xray.go`**

```go
package service

import (
	"encoding/json"
	"fmt"
	"vpnbot/database"
)

// Xray-core на RuVDS — сайдкар перед sing-box: снимает finalmask и отдаёт
// голый поток во внутренний VLESS-инбаунд на loopback. Пин версии — из
// спайка 2026-09-09 (docs/xray-finalmask-spike.md): sudoku/header-custom
// есть с 26.3.27, баг #6184 закрыт в 26.6.1, 26.9.9 проверен на стенде.
const (
	XrayVersion       = "26.9.9"
	XrayLinux64SHA256 = "1eb9175d0f0a8f8149c9230a7fc5ae66ce332ed20a53155ce61fe62e3f58b7df"
)

// finalmaskBlock — минимальная схема для валидации MaskJSON. Остальное
// содержимое не трактуем: в Xray уходит исходная строка как есть.
type finalmaskBlock struct {
	TCP []json.RawMessage `json:"tcp"`
}

// ValidateMaskJSON проверяет, что строка — JSON-объект с непустым массивом tcp.
func ValidateMaskJSON(s string) error {
	if s == "" {
		return fmt.Errorf("mask_json is empty")
	}
	var fm finalmaskBlock
	if err := json.Unmarshal([]byte(s), &fm); err != nil {
		return fmt.Errorf("mask_json is not a JSON object: %w", err)
	}
	if len(fm.TCP) == 0 {
		return fmt.Errorf("mask_json must contain a non-empty \"tcp\" array")
	}
	return nil
}

type xrayLog struct {
	Loglevel string `json:"loglevel"`
}

type xrayDokodemoSettings struct {
	Address string `json:"address"`
	Port    int    `json:"port"`
	Network string `json:"network"`
}

type xrayStreamSettings struct {
	Network   string          `json:"network"`
	Finalmask json.RawMessage `json:"finalmask"`
}

type xrayInbound struct {
	Tag            string               `json:"tag"`
	Listen         string               `json:"listen"`
	Port           int                  `json:"port"`
	Protocol       string               `json:"protocol"`
	Settings       xrayDokodemoSettings `json:"settings"`
	StreamSettings xrayStreamSettings   `json:"streamSettings"`
}

type xrayOutbound struct {
	Protocol string `json:"protocol"`
	Tag      string `json:"tag"`
}

type xrayConfig struct {
	Log       xrayLog        `json:"log"`
	Inbounds  []xrayInbound  `json:"inbounds"`
	Outbounds []xrayOutbound `json:"outbounds"`
}

// findMaskInner ищет внутренний инбаунд для маски среди переданных (enabled).
func findMaskInner(mask database.InboundConfig, inbounds []database.InboundConfig) (database.InboundConfig, error) {
	for _, ib := range inbounds {
		if ib.Tag != mask.MaskInnerTag {
			continue
		}
		if ib.Protocol != "vless" || ib.Transport != "" {
			return database.InboundConfig{}, fmt.Errorf(
				"mask %q: inner inbound %q must be vless over plain TCP (got protocol=%q transport=%q)",
				mask.Tag, ib.Tag, ib.Protocol, ib.Transport)
		}
		return ib, nil
	}
	return database.InboundConfig{}, fmt.Errorf("mask %q: inner inbound %q not found among enabled inbounds", mask.Tag, mask.MaskInnerTag)
}

// buildXrayConfig — чистая функция: JSON Xray из mask-инбаундов.
// nil, nil — если mask-инбаундов нет (сервис на RuVDS надо остановить).
func buildXrayConfig(inbounds []database.InboundConfig) ([]byte, error) {
	cfg := xrayConfig{
		Log:       xrayLog{Loglevel: "warning"},
		Inbounds:  []xrayInbound{},
		Outbounds: []xrayOutbound{{Protocol: "freedom", Tag: "direct"}},
	}
	for _, mask := range inbounds {
		if mask.Protocol != "mask" {
			continue
		}
		if err := ValidateMaskJSON(mask.MaskJSON); err != nil {
			return nil, fmt.Errorf("mask %q: %w", mask.Tag, err)
		}
		inner, err := findMaskInner(mask, inbounds)
		if err != nil {
			return nil, err
		}
		cfg.Inbounds = append(cfg.Inbounds, xrayInbound{
			Tag:      mask.Tag,
			Listen:   "0.0.0.0",
			Port:     mask.ListenPort,
			Protocol: "dokodemo-door",
			Settings: xrayDokodemoSettings{Address: "127.0.0.1", Port: inner.ListenPort, Network: "tcp"},
			StreamSettings: xrayStreamSettings{
				Network:   "tcp",
				Finalmask: json.RawMessage(mask.MaskJSON),
			},
		})
	}
	if len(cfg.Inbounds) == 0 {
		return nil, nil
	}
	return json.MarshalIndent(cfg, "", "  ")
}
```

- [ ] **Step 4: Прогнать тесты**

Run: `go test ./service/ -run 'Xray|MaskJSON' -v`
Expected: PASS (7 тестов).

- [ ] **Step 5: Commit**

```bash
git add service/xray.go service/xray_test.go
git commit -m "feat(xray): чистый генератор конфига Xray для mask-инбаундов"
```

---

### Task 4: Зеркало Xray на RuVDS по SSH

**Files:**
- Create: `service/xrayruvds.go`
- Modify: `service/vpn.go` (`GenerateAndReloadRuVDS` ~строка 505)

**Interfaces:**
- Consumes: `sshConnect()`, `runSSH(client, cmd)` из `service/portforward.go`; `IsRuVDSEnabled()`, `IsPortForwardConfigured()`; `buildXrayConfig`, `XrayVersion`, `XrayLinux64SHA256` из Task 3.
- Produces: `InstallXrayRuVDS() error`, `EnsureXrayRuVDSService() error`, `GenerateXrayRuVDSConfig() ([]byte, error)`, `DeployXrayConfigRuVDS(cfgJSON []byte) error`, `StartXrayRuVDS() error`, `StopXrayRuVDS() error`, `IsXrayRuVDSRunning() bool`, `XrayRuVDSLogs(lines int) (string, error)`, `SetupXrayRuVDS() error`, `XrayRuVDSVersion() string`.

- [ ] **Step 1: Создать `service/xrayruvds.go`**

```go
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
```

- [ ] **Step 2: Подключить в `GenerateAndReloadRuVDS`**

Заменить тело функции в `service/vpn.go`:

```go
// GenerateAndReloadRuVDS — перегенерация + деплой config.json на RuVDS через SSH.
// Xray-сайдкар деплоится после sing-box; его ошибка не откатывает sing-box,
// а возвращается отдельным сообщением.
func GenerateAndReloadRuVDS() error {
	if !IsRuVDSEnabled() {
		return nil
	}
	cfgJSON, err := GenerateRuVDSConfig()
	if err != nil {
		return err
	}
	if err := DeploySingboxConfigRuVDS(cfgJSON); err != nil {
		return err
	}
	xrayJSON, err := GenerateXrayRuVDSConfig()
	if err != nil {
		return fmt.Errorf("xray RuVDS: %w", err)
	}
	if err := DeployXrayConfigRuVDS(xrayJSON); err != nil {
		return fmt.Errorf("xray RuVDS: %w", err)
	}
	return nil
}
```

- [ ] **Step 3: Vet, тесты, сборка**

Run: `go vet ./... && go test ./service/ && go build -o /dev/null .`
Expected: без ошибок. (`GenerateRuVDSConfig` уже не учитывает mask благодаря Task 2.)

- [ ] **Step 4: Commit**

```bash
git add service/xrayruvds.go service/vpn.go
git commit -m "feat(xray): зеркало Xray на RuVDS по SSH и деплой вместе с sing-box"
```

---

### Task 5: Ссылка для mask-инбаунда

**Files:**
- Modify: `service/vpn.go` (`GenerateLinkForInbound` ~строка 517)
- Test: `service/xray_test.go`

**Interfaces:**
- Produces: `func GenerateMaskLink(mask, inner database.InboundConfig, user database.User, serverAddr string) string` — чистая; `GenerateLinkForInbound` для `Protocol=="mask"` ищет inner в БД и вызывает её (пустая строка, если inner не найден или БД не инициализирована).

- [ ] **Step 1: Написать падающие тесты**

В `service/xray_test.go`:

```go
func TestGenerateMaskLink(t *testing.T) {
	inner, mask := maskFixture()
	inner.RealityPublicKey = "PUBKEY"
	inner.SNI = "gosuslugi.ru"
	inner.RealityShortIDs = database.JSONStringArray{"caa7f714"}
	inner.Flow = "xtls-rprx-vision"
	inner.ServerAddress = "hetzner.example" // должен быть проигнорирован
	user := database.User{Username: "alice", UUID: "550e8400-e29b-41d4-a716-446655440000"}

	link := GenerateMaskLink(mask, inner, user, "198.51.100.7")

	if !strings.HasPrefix(link, "vless://550e8400-e29b-41d4-a716-446655440000@198.51.100.7:2071?") {
		t.Fatalf("expected inner uuid + mask port, got %s", link)
	}
	for _, want := range []string{"security=reality", "pbk=PUBKEY", "sni=gosuslugi.ru", "sid=caa7f714", "flow=xtls-rprx-vision"} {
		if !strings.Contains(link, want) {
			t.Errorf("missing %s in %s", want, link)
		}
	}
	// fm = URL-encoded компактный JSON блока finalmask (формат v2rayN/v2rayNG).
	if !strings.Contains(link, "fm=%7B%22tcp%22%3A%5B%7B%22type%22%3A%22sudoku%22") {
		t.Errorf("expected url-encoded fm, got %s", link)
	}
	if !strings.HasSuffix(link, "#RU-MASK") {
		t.Errorf("expected #RU-MASK fragment, got %s", link)
	}
	if strings.Contains(link, "hetzner.example") || strings.Contains(link, ":2060") {
		t.Errorf("inner address/port leaked: %s", link)
	}
}

func TestGenerateLinkForInbound_MaskWithoutDB(t *testing.T) {
	_, mask := maskFixture()
	if got := GenerateLinkForInbound(mask, database.User{}, "198.51.100.7"); got != "" {
		t.Fatalf("expected empty link without DB, got %q", got)
	}
}
```

- [ ] **Step 2: Запустить, убедиться что падают**

Run: `go test ./service/ -run 'MaskLink|MaskWithoutDB' -v`
Expected: FAIL — `undefined: GenerateMaskLink`.

- [ ] **Step 3: Реализовать**

В `service/vpn.go` в начало `GenerateLinkForInbound` после подмены `serverAddr`:

```go
	if ib.Protocol == "mask" {
		inner, ok := lookupInboundByTag(ib.MaskInnerTag)
		if !ok {
			return ""
		}
		return GenerateMaskLink(ib, inner, user, serverAddr)
	}
```

(вставить перед `if ib.Protocol == "shadowtls"`). Ниже `generateShadowTLSLink` добавить:

```go
// lookupInboundByTag — внутренний инбаунд маски из БД. false — если БД не
// инициализирована (юнит-тесты) или тег не найден.
func lookupInboundByTag(tag string) (database.InboundConfig, bool) {
	if database.DB == nil || tag == "" {
		return database.InboundConfig{}, false
	}
	var ib database.InboundConfig
	if err := database.DB.Where("tag = ?", tag).First(&ib).Error; err != nil {
		return database.InboundConfig{}, false
	}
	return ib, true
}

// GenerateMaskLink — ссылка внутреннего VLESS-инбаунда с портом маски и
// параметром fm. Формат fm (v2rayN BaseFmt.cs, v2rayNG FmtBase.kt):
// компактный JSON блока finalmask, URL-encoded. Понимают только
// Xray-клиенты: v2rayNG, v2rayN, Happ, Streisand.
func GenerateMaskLink(mask, inner database.InboundConfig, user database.User, serverAddr string) string {
	inner.ServerAddress = "" // адрес маски — тот, что передан (RuVDS), не override внутреннего
	innerLink := GenerateLinkForInbound(inner, user, serverAddr)
	u, err := url.Parse(innerLink)
	if err != nil || u.Scheme != "vless" {
		return ""
	}
	u.Host = fmt.Sprintf("%s:%d", serverAddr, mask.ListenPort)

	var compact bytes.Buffer
	if err := json.Compact(&compact, []byte(mask.MaskJSON)); err != nil {
		return ""
	}
	q := u.Query()
	q.Set("fm", compact.String())
	u.RawQuery = q.Encode()
	u.Fragment = mask.Tag
	return u.String()
}
```

Добавить в импорты `service/vpn.go` пакет `"bytes"` (остальные — `encoding/json`, `fmt`, `net/url` — уже есть).

- [ ] **Step 4: Прогнать тесты**

Run: `go test ./service/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add service/vpn.go service/xray_test.go
git commit -m "feat(links): ссылка mask-инбаунда с fm для Xray-клиентов"
```

---

### Task 6: API — валидация mask-инбаунда, роуты Xray, Hetzner-подписка без масок

**Files:**
- Modify: `api/handlers/inbounds.go` (`CreateInbound` ~строка 194, `UpdateInbound` ~строка 288)
- Modify: `api/handlers/subscription.go` (`GetSubscription`, цикл ~строка 39)
- Create: `api/handlers/xrayruvds.go`
- Modify: `api/router/router.go` (после строки 104, блок singbox/ruvds)

**Interfaces:**
- Consumes: `service.ValidateMaskJSON`, `service.SetupXrayRuVDS`, `service.GenerateAndReloadRuVDS`, `service.StartXrayRuVDS`, `service.StopXrayRuVDS`, `service.IsXrayRuVDSRunning`, `service.XrayRuVDSVersion`, `service.XrayRuVDSLogs`, `service.GenerateXrayRuVDSConfig`, `service.XrayVersion`.
- Produces: роуты `/api/xray/ruvds/{setup,reload,start,stop,status,config,logs}`.

- [ ] **Step 1: Валидация в handlers**

В `api/handlers/inbounds.go` рядом с `validateInboundCombination` добавить:

```go
// validateMaskInbound — проверки для Protocol="mask". Возвращает текст
// ошибки или "". Сбрасывает ExitOutbound: маршрут решает внутренний инбаунд.
func validateMaskInbound(input *database.InboundConfig) string {
	if input.ListenPort == 0 {
		return "listen_port is required for mask"
	}
	if input.MaskInnerTag == "" {
		return "mask_inner_tag is required for mask"
	}
	if err := service.ValidateMaskJSON(input.MaskJSON); err != nil {
		return "mask_json: " + err.Error()
	}
	var inner database.InboundConfig
	if err := database.DB.Where("tag = ?", input.MaskInnerTag).First(&inner).Error; err != nil {
		return "mask_inner_tag: inbound not found"
	}
	if inner.Protocol != "vless" || inner.Transport != "" {
		return "mask_inner_tag must point to a vless inbound over plain TCP"
	}
	if !inner.Enabled {
		return "mask_inner_tag: inner inbound is disabled"
	}
	// Exit решает внутренний инбаунд; у маски своего маршрута нет.
	input.ExitOutbound = ""
	return ""
}
```

В `CreateInbound` заменить проверку протокола и ветку валидации:

```go
		if input.Protocol != "vless" && input.Protocol != "hysteria2" && input.Protocol != "shadowtls" && input.Protocol != "mask" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Protocol must be 'vless', 'hysteria2', 'shadowtls', or 'mask'"})
			return
		}

		switch input.Protocol {
		case "mask":
			if msg := validateMaskInbound(&input); msg != "" {
				c.JSON(http.StatusBadRequest, gin.H{"error": msg})
				return
			}
		case "shadowtls":
		default:
			if err := validateInboundCombination(&input); err != "" {
				c.JSON(http.StatusBadRequest, gin.H{"error": err})
				return
			}
		}
```

В `UpdateInbound` аналогично:

```go
		if input.Protocol != "" && input.Protocol != "vless" && input.Protocol != "hysteria2" && input.Protocol != "shadowtls" && input.Protocol != "mask" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Protocol must be 'vless', 'hysteria2', 'shadowtls', or 'mask'"})
			return
		}

		effectiveProtocol := input.Protocol
		if effectiveProtocol == "" {
			effectiveProtocol = existing.Protocol
		}
		switch effectiveProtocol {
		case "mask":
			// Для частичного апдейта берём недостающие поля из existing.
			merged := existing
			if input.ListenPort != 0 {
				merged.ListenPort = input.ListenPort
			}
			if input.MaskInnerTag != "" {
				merged.MaskInnerTag = input.MaskInnerTag
			}
			if input.MaskJSON != "" {
				merged.MaskJSON = input.MaskJSON
			}
			if msg := validateMaskInbound(&merged); msg != "" {
				c.JSON(http.StatusBadRequest, gin.H{"error": msg})
				return
			}
			input.ExitOutbound = ""
		case "shadowtls":
		default:
			if err := validateInboundCombination(&input); err != "" {
				c.JSON(http.StatusBadRequest, gin.H{"error": err})
				return
			}
		}
```

(старые строки `if input.Protocol != "shadowtls" && existing.Protocol != "shadowtls" { ... }` удалить — их заменяет `switch`).

- [ ] **Step 2: Hetzner-подписка пропускает маски**

В `api/handlers/subscription.go`, в `GetSubscription`, цикл:

```go
		for _, ib := range inbounds {
			// Xray-сайдкар стоит только на RuVDS: Hetzner-ссылка на маску не сработает.
			if ib.Protocol == "mask" {
				continue
			}
			links = append(links, service.GenerateLinkForInbound(ib, user, serverIP))
		}
```

В `GetSubscriptionRuVDS` ничего не менять: `GenerateLinkForInbound` для mask сам ищет inner и подставляет `ruvdsIP`.

- [ ] **Step 3: Handlers Xray**

Создать `api/handlers/xrayruvds.go`:

```go
package handlers

import (
	"strconv"
	"vpnbot/service"

	"github.com/gin-gonic/gin"
)

// POST /api/xray/ruvds/setup — установка Xray-сайдкара на RuVDS (пин service.XrayVersion)
func SetupXrayRuVDS() gin.HandlerFunc {
	return func(c *gin.Context) {
		if err := service.SetupXrayRuVDS(); err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, gin.H{"message": "Xray установлен на RuVDS", "version": service.XrayVersion})
	}
}

// POST /api/xray/ruvds/reload — перегенерация sing-box + Xray и деплой
func ReloadXrayRuVDS() gin.HandlerFunc {
	return func(c *gin.Context) {
		if err := service.GenerateAndReloadRuVDS(); err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, gin.H{"message": "RuVDS sing-box и Xray перезагружены"})
	}
}

// POST /api/xray/ruvds/start
func StartXrayRuVDS() gin.HandlerFunc {
	return func(c *gin.Context) {
		if err := service.StartXrayRuVDS(); err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, gin.H{"message": "Xray запущен на RuVDS"})
	}
}

// POST /api/xray/ruvds/stop
func StopXrayRuVDS() gin.HandlerFunc {
	return func(c *gin.Context) {
		if err := service.StopXrayRuVDS(); err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, gin.H{"message": "Xray остановлен на RuVDS"})
	}
}

// GET /api/xray/ruvds/status
func GetXrayRuVDSStatus() gin.HandlerFunc {
	return func(c *gin.Context) {
		status := "stopped"
		if service.IsXrayRuVDSRunning() {
			status = "running"
		}
		if !service.IsPortForwardConfigured() {
			status = "not_configured"
		}
		c.JSON(200, gin.H{
			"status":            status,
			"installed_version": service.XrayRuVDSVersion(),
			"pinned_version":    service.XrayVersion,
			"ruvds_ip":          service.GetRuVDSIP(),
		})
	}
}

// GET /api/xray/ruvds/config — preview конфига Xray (204, если масок нет)
func PreviewXrayRuVDSConfig() gin.HandlerFunc {
	return func(c *gin.Context) {
		cfg, err := service.GenerateXrayRuVDSConfig()
		if err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
		if cfg == nil {
			c.Status(204)
			return
		}
		c.Data(200, "application/json", cfg)
	}
}

// GET /api/xray/ruvds/logs?lines=50
func GetXrayRuVDSLogs() gin.HandlerFunc {
	return func(c *gin.Context) {
		lines, _ := strconv.Atoi(c.DefaultQuery("lines", "50"))
		out, err := service.XrayRuVDSLogs(lines)
		if err != nil {
			c.JSON(500, gin.H{"error": err.Error(), "logs": out})
			return
		}
		c.JSON(200, gin.H{"logs": out})
	}
}
```

- [ ] **Step 4: Роуты**

В `api/router/router.go` после строки `auth.POST("/singbox/ruvds/rollback", ...)`:

```go
			// Xray finalmask-сайдкар на RuVDS
			auth.POST("/xray/ruvds/setup", handlers.SetupXrayRuVDS())
			auth.POST("/xray/ruvds/reload", handlers.ReloadXrayRuVDS())
			auth.POST("/xray/ruvds/start", handlers.StartXrayRuVDS())
			auth.POST("/xray/ruvds/stop", handlers.StopXrayRuVDS())
			auth.GET("/xray/ruvds/status", handlers.GetXrayRuVDSStatus())
			auth.GET("/xray/ruvds/config", handlers.PreviewXrayRuVDSConfig())
			auth.GET("/xray/ruvds/logs", handlers.GetXrayRuVDSLogs())
```

- [ ] **Step 5: Vet и сборка**

Run: `go vet ./... && go build -o /dev/null . && go test ./service/`
Expected: без ошибок.

- [ ] **Step 6: Commit**

```bash
git add api/handlers/inbounds.go api/handlers/subscription.go api/handlers/xrayruvds.go api/router/router.go
git commit -m "feat(api): валидация mask-инбаундов и роуты /api/xray/ruvds/*"
```

---

### Task 7: Бот — кнопка маски с пометкой и адресом RuVDS

**Files:**
- Modify: `bot/bot.go` (меню подключений ~строки 190–195; обработчики `conn_link` ~246 и `conn_qr` ~255)

**Interfaces:**
- Consumes: `service.GetRuVDSIP()`, `service.GenerateLinkForInbound`.

- [ ] **Step 1: Помощник адреса и подписи**

В `bot/bot.go` рядом с `getInboundAndUser` добавить:

```go
// linkServerAddr — адрес сервера для ссылки инбаунда. Mask-инбаунды живут
// только на RuVDS (Xray-сайдкар), остальные — как раньше, через ServerIP.
func linkServerAddr(ib database.InboundConfig) string {
	if ib.Protocol == "mask" {
		return service.GetRuVDSIP()
	}
	return ServerIP
}

// qrCaption — подпись к QR: для масок клиент другой.
func qrCaption(ib database.InboundConfig) string {
	if ib.Protocol == "mask" {
		return fmt.Sprintf("%s — только v2rayNG / Happ / Streisand (Hiddify не подойдёт)", ib.DisplayName)
	}
	return fmt.Sprintf("%s — сканируйте в Hiddify", ib.DisplayName)
}
```

- [ ] **Step 2: Меню и обработчики**

В цикле кнопок заменить:

```go
		for _, ib := range inbounds {
			label := ib.DisplayName
			if ib.Protocol == "mask" {
				label = ib.DisplayName + " (v2rayNG/Happ)"
			}
			btnLink := connectMenu.Data(fmt.Sprintf("🔗 %s", label), "conn_link", fmt.Sprintf("%d", ib.ID))
			btnQR := connectMenu.Data(fmt.Sprintf("📷 %s", label), "conn_qr", fmt.Sprintf("%d", ib.ID))
			rows = append(rows, connectMenu.Row(btnLink, btnQR))
		}
```

В `conn_link`:

```go
		link := service.GenerateLinkForInbound(ib, user, linkServerAddr(ib))
		if link == "" {
			return c.Send("❌ Не удалось собрать ссылку: внутренний инбаунд маски не найден.")
		}
		if ib.Protocol == "mask" {
			return c.Send(fmt.Sprintf("`%s`\n\n⚠️ Ссылка только для v2rayNG, v2rayN, Happ или Streisand. Hiddify и Shadowrocket её не поймут.", link), tele.ModeMarkdown)
		}
		return c.Send(fmt.Sprintf("`%s`", link), tele.ModeMarkdown)
```

В `conn_qr`:

```go
		link := service.GenerateLinkForInbound(ib, user, linkServerAddr(ib))
		if link == "" {
			return c.Send("❌ Не удалось собрать ссылку: внутренний инбаунд маски не найден.")
		}

		qr, qrErr := qrcode.Encode(link, qrcode.Medium, 256)
		if qrErr != nil {
			return c.Send("❌ Ошибка генерации QR кода.")
		}

		photo := &tele.Photo{File: tele.FromReader(bytes.NewReader(qr)), Caption: qrCaption(ib)}
		return c.Send(photo)
```

- [ ] **Step 3: Vet и сборка**

Run: `go vet ./... && go build -o /dev/null .`
Expected: без ошибок.

- [ ] **Step 4: Commit**

```bash
git add bot/bot.go
git commit -m "feat(bot): кнопка mask-инбаунда с адресом RuVDS и пометкой про клиенты"
```

---

### Task 8: Документация и ручная верификация

**Files:**
- Modify: `CLAUDE.md` (разделы «Packages», «InboundConfig model», «API Structure»)
- Modify: `AGENTS.md` (те же разделы, кратко)
- Modify: `docs/superpowers/specs/2026-09-09-xray-finalmask-design.md` (статус этапа 1)

- [ ] **Step 1: CLAUDE.md**

В «Packages» после `telemt mirror on RuVDS via SSH (telemtruvds.go)` добавить: `, Xray finalmask sidecar on RuVDS via SSH (\`xray.go\` — pure config builder, \`xrayruvds.go\` — SSH mirror)`.

В «InboundConfig model» добавить строки:

```
- `Protocol`: `"vless"` | `"hysteria2"` | `"shadowtls"` | `"mask"`
- `MaskInnerTag`, `MaskJSON`: mask-only (Protocol="mask"). Xray on RuVDS listens on `ListenPort` as `dokodemo-door` with `streamSettings.finalmask = MaskJSON` and forwards raw bytes to `127.0.0.1:<inner.ListenPort>` where `inner.Tag == MaskInnerTag` (must be enabled vless over plain TCP). Mask inbounds are skipped by both sing-box generators and by `/sub/:token`; `/sub-ruvds/:token` and the bot emit the inner link with the mask port and `fm=<url-encoded MaskJSON>` — Xray-based clients only (v2rayNG, v2rayN, Happ, Streisand).
```

В «API Structure» добавить:

```
- Xray RuVDS: `/api/xray/ruvds/{setup,reload,start,stop,status,config,logs}` — Xray finalmask sidecar on RuVDS (pinned `service.XrayVersion`). `reload` = the same as singbox reload (sing-box first, then Xray; Xray has no hot reload, the service is restarted).
```

- [ ] **Step 2: AGENTS.md** — те же три вставки в сокращённом виде.

- [ ] **Step 3: Ручная верификация на проде (после деплоя main)**

Выполнить по порядку и записать результат в спеку (раздел «Верификация этапа 1»):

1. `PUT /api/inbounds/<id RU-MASK>` с `{"mask_json": "{\"tcp\":[{\"type\":\"sudoku\",\"settings\":{\"password\":\"<32 случайных символов>\",\"ascii\":\"prefer_entropy\",\"paddingMin\":2,\"paddingMax\":7}}]}", "enabled": true}` → 200.
2. `GET /api/xray/ruvds/config` → JSON с `dokodemo-door`, портом 2071 и `"port": 2060` в settings.
3. `POST /api/xray/ruvds/setup` → 200; `GET /api/xray/ruvds/status` → `installed_version` содержит `26.9.9`, `status: running`.
4. На RuVDS: `ss -ltnp | grep 2071` показывает `xray`; `journalctl -u xray -n 20` без ошибок; `ufw status | grep 2071` — ALLOW.
5. `GET /sub-ruvds/<token>` → в base64 есть строка `vless://…@<RUVDS_IP>:2071?…fm=%7B%22tcp%22…#RU-MASK`; `GET /sub/<token>` этой строки НЕ содержит.
6. v2rayNG (тестер в РФ, инструкция по UI): импорт ссылки → подключение → открыть 2ip.ru, ожидается российский IP RuVDS. На RuVDS `journalctl -u sing-box` показывает `inbound connection from 127.0.0.1`.
7. Hiddify с той же ссылкой НЕ подключается — ожидаемо.
8. `PUT /api/inbounds/<id>` с `{"enabled": false}` → `GET /api/xray/ruvds/status` → `stopped` (DeployXrayConfigRuVDS(nil) остановил сервис).

- [ ] **Step 4: Commit**

```bash
git add CLAUDE.md AGENTS.md docs/superpowers/specs/2026-09-09-xray-finalmask-design.md
git commit -m "docs: mask-инбаунд и Xray-сайдкар на RuVDS"
```

---

## Вне скоупа этого плана

- Админ-панель (`vpn-admin-panel`): выбор `mask` в форме инбаунда и поля `mask_inner_tag`/`mask_json`. Пока — через API.
- XDNS (этап 2 спеки): ждёт решения по порту 53 / Cloud Firewall.
- Автооткрытие порта маски в Hetzner Cloud Firewall не нужно: порт живёт на RuVDS, ufw открывает `openRuVDSMaskPorts()` при setup. При добавлении новой маски после setup — повторить `POST /api/xray/ruvds/setup` (идемпотентно) или открыть порт вручную.
