# XDNS-канал на Hetzner (этап 2) — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Инбаунд `Protocol="xdns"`: отдельный экземпляр Xray на Hetzner слушает `:53` (VLESS+mKCP+finalmask xdns), проксирует данные внутри DNS-запросов; ссылка для Happ раздаётся только кнопкой в боте.

**Architecture:** Второй экземпляр Xray, локально на Hetzner (где работает vpnbot), отдельно от RuVDS-сайдкара этапа 1. Новый чистый билдер `buildXrayXDNSConfig`, локальный деплой `service/xrayhetzner.go` (без SSH, по образцу `service/xrayruvds.go`), unit `xray-xdns.service`, конфиг `/etc/xray-xdns/config.json`. Пара VLESS-шифрования генерится `xray vlessenc` при setup. XDNS не попадает в подписки, только кнопка в боте.

**Tech Stack:** Go 1.21, GORM/SQLite, gin, telebot.v3, Xray-core v26.9.9 (linux-64), локальный `systemctl` + `os/exec`.

**Spec:** `docs/superpowers/specs/2026-09-09-xray-finalmask-design.md`, раздел «Этап 2 — XDNS». Спайк: `docs/xray-finalmask-spike.md`.

## Global Constraints

- Xray-core пин уже задан в `service/xray.go`: `XrayVersion="26.9.9"`, `XrayLinux64SHA256="1eb9175d0f0a8f8149c9230a7fc5ae66ce332ed20a53155ce61fe62e3f58b7df"`. Переиспользовать, НЕ дублировать.
- XDNS — второй экземпляр Xray на **Hetzner**, локально (без SSH): unit `xray-xdns.service`, бинарь `/usr/local/bin/xray` (общий), конфиг `/etc/xray-xdns/config.json`. НЕ путать с RuVDS-сайдкаром (`xray.service`, `/etc/xray/config.json`, управляется по SSH).
- Xray не умеет hot-reload: деплой = write `.new` → `xray run -test -format json -c .new` (флаг `-format json` обязателен, иначе «Failed to get format») → `cmp -s` → `mv` → `systemctl restart` только если изменилось И сервис active.
- MTU: сервер 900, клиент 130 — константы (спайк: клиентский MTU 400/900 не работают).
- VLESS без TLS требует `xray vlessenc`: пара `decryption` (сервер) / `encryption` (клиент). Короткий X25519-вариант (первая пара из вывода `xray vlessenc`, строка после «Authentication: X25519»), НЕ ML-KEM.
- XDNS НЕ попадает в `/sub/:token` и `/sub-ruvds/:token`; раздаётся только кнопкой `XDNS (Happ)` в боте (как маска).
- Комментарии и тексты бота — по-русски; API-ответы и ошибки — по-английски.
- Освобождение порта 53 от slipstream — операторский шаг в рунбуке, НЕ в коде. `SetupXrayXDNS` не трогает slipstream; при занятом 53 сервис не стартует, и это отражается в статусе.
- Коммиты: только изменённые файлы явным pathspec (в рабочем дереве есть посторонние правки — никогда `git add -A`/`.`/`-a`). Каждое сообщение оканчивается:
  ```
  Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_01JZr7zP3FCpVL7Nsmdq91V5
  ```
- Перед каждым коммитом: `gofmt -l <файлы>` (пусто), `go vet ./service/ ./api/... ./bot/ ./database/ .`, `go build -o /dev/null .`, `go test ./service/`. Не гонять `go vet ./...` — untracked `test-combined-with-dnstt/` его ломает (два package в одной папке, посторонний).

---

### Task 1: Модель — поля xdns-инбаунда

**Files:**
- Modify: `database/database.go` (struct `InboundConfig`, после ShadowTLS/Mask-полей; комментарий `Protocol`)

**Interfaces:**
- Produces: поля `InboundConfig.XDNSDomain`, `.XDNSResolvers`, `.XDNSDecryption`, `.XDNSEncryption` (все `string`); значение `Protocol == "xdns"`.

- [ ] **Step 1: Добавить поля**

В `database/database.go` после блока полей маски (`MaskInnerTag`, `MaskJSON`) добавить:

```go
	// XDNS-канал (Protocol="xdns"): отдельный Xray на Hetzner, VLESS+mKCP,
	// данные внутри DNS-запросов. Домен зоны + клиентские резолверы + пара
	// VLESS-шифрования (генерится xray vlessenc при setup).
	XDNSDomain     string `json:"xdns_domain"`               // напр. "t.edgn.net:txt"
	XDNSResolvers  string `json:"xdns_resolvers"`            // список через запятую: "t.edgn.net:txt+udp://1.2.3.4:53,..."
	XDNSDecryption string `gorm:"type:text" json:"xdns_decryption"` // серверный ключ (decryption)
	XDNSEncryption string `gorm:"type:text" json:"xdns_encryption"` // клиентский ключ (encryption), в ссылку
```

Поправить комментарий `Protocol`:

```go
	Protocol      string `json:"protocol"` // "vless" | "hysteria2" | "shadowtls" | "mask" | "xdns"
```

Сид НЕ добавлять: xdns-инбаунд создаётся оператором через API, ключи генерятся при setup.

- [ ] **Step 2: Сборка и vet**

Run: `go build -o /dev/null . && go vet ./database/`
Expected: без ошибок (AutoMigrate добавит колонки на старте).

- [ ] **Step 3: Commit**

```bash
git add database/database.go
git commit -m "feat(db): поля xdns-инбаунда"
```

---

### Task 2: Чистый билдер конфига XDNS + валидация

**Files:**
- Create: `service/xrayxdns.go`
- Test: `service/xrayxdns_test.go`

**Interfaces:**
- Consumes: `database.InboundConfig` с полями Task 1; `buildNewUsers([]database.User) []VLessUser` (существует в `service/vpn.go`, отдаёт `{Name,UUID}`); `xrayLog`, `xrayOutbound` (существуют в `service/xray.go`).
- Produces:
  - `func ValidateXDNSInbound(ib database.InboundConfig) error` — проверяет `ListenPort!=0`, `XDNSDomain!=""`, `XDNSResolvers!=""`.
  - `func buildXrayXDNSConfig(xdns []database.InboundConfig, users []database.User) ([]byte, error)` — берёт только `Protocol=="xdns"`; `nil,nil` если их нет; ошибка если у enabled-инбаунда пустой `XDNSDecryption` (не сделан setup) или невалидные поля.
  - `func xdnsClientFinalmask(resolvers string) string` — клиентский `fm`-блок JSON из списка резолверов.

- [ ] **Step 1: Написать падающие тесты**

Создать `service/xrayxdns_test.go`:

```go
package service

import (
	"encoding/json"
	"strings"
	"testing"
	"vpnbot/database"
)

func xdnsFixture() database.InboundConfig {
	return database.InboundConfig{
		Tag: "XDNS", Protocol: "xdns", ListenPort: 53, Enabled: true,
		XDNSDomain:     "t.edgn.net:txt",
		XDNSResolvers:  "t.edgn.net:txt+udp://8.8.8.8:53,t.edgn.net:txt+udp://1.1.1.1:53",
		XDNSDecryption: "mlkem768x25519plus.native.600s.SERVERKEY",
		XDNSEncryption: "mlkem768x25519plus.native.0rtt.CLIENTKEY",
	}
}

func TestValidateXDNSInbound(t *testing.T) {
	ok := xdnsFixture()
	if err := ValidateXDNSInbound(ok); err != nil {
		t.Fatalf("valid inbound rejected: %v", err)
	}
	for _, bad := range []database.InboundConfig{
		func() database.InboundConfig { c := ok; c.ListenPort = 0; return c }(),
		func() database.InboundConfig { c := ok; c.XDNSDomain = ""; return c }(),
		func() database.InboundConfig { c := ok; c.XDNSResolvers = ""; return c }(),
	} {
		if err := ValidateXDNSInbound(bad); err == nil {
			t.Errorf("expected error for %+v", bad)
		}
	}
}

func TestBuildXrayXDNSConfig_None(t *testing.T) {
	out, err := buildXrayXDNSConfig([]database.InboundConfig{{Tag: "DE", Protocol: "vless"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out != nil {
		t.Fatalf("expected nil when no xdns inbounds, got %s", out)
	}
}

func TestBuildXrayXDNSConfig_Single(t *testing.T) {
	users := []database.User{{Username: "alice", UUID: "550e8400-e29b-41d4-a716-446655440000"}}
	out, err := buildXrayXDNSConfig([]database.InboundConfig{xdnsFixture()}, users)
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(out, &cfg); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	got := string(out)
	for _, want := range []string{
		`"protocol": "vless"`, `"port": 53`, `"network": "kcp"`, `"mtu": 900`,
		`"type": "xdns"`, `"t.edgn.net:txt"`, `SERVERKEY`,
		`550e8400-e29b-41d4-a716-446655440000`, `"protocol": "freedom"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "CLIENTKEY") {
		t.Errorf("client encryption key must NOT be in server config: %s", got)
	}
}

func TestBuildXrayXDNSConfig_MissingKeysErrors(t *testing.T) {
	c := xdnsFixture()
	c.XDNSDecryption = ""
	if _, err := buildXrayXDNSConfig([]database.InboundConfig{c}, nil); err == nil {
		t.Fatal("expected error when XDNSDecryption empty (setup not run)")
	}
}

func TestXDNSClientFinalmask(t *testing.T) {
	fm := xdnsClientFinalmask("t.edgn.net:txt+udp://8.8.8.8:53, t.edgn.net:txt+udp://1.1.1.1:53")
	var m map[string]any
	if err := json.Unmarshal([]byte(fm), &m); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, fm)
	}
	for _, want := range []string{`"udp"`, `"xdns"`, `"resolvers"`, `8.8.8.8`, `1.1.1.1`} {
		if !strings.Contains(fm, want) {
			t.Errorf("missing %s in %s", want, fm)
		}
	}
	// пробелы вокруг запятой должны срезаться
	if strings.Contains(fm, `" t.edgn.net`) {
		t.Errorf("resolver not trimmed: %s", fm)
	}
}
```

- [ ] **Step 2: Запустить, убедиться что не компилируется**

Run: `go test ./service/ -run 'XDNS' -v`
Expected: FAIL — undefined `ValidateXDNSInbound`, `buildXrayXDNSConfig`, `xdnsClientFinalmask`.

- [ ] **Step 3: Реализовать `service/xrayxdns.go`**

```go
package service

import (
	"encoding/json"
	"fmt"
	"strings"
	"vpnbot/database"
)

const (
	XDNSServerMTU = 900
	XDNSClientMTU = 130
)

// ValidateXDNSInbound — проверки полей xdns-инбаунда (без ключей: они
// заполняются при setup).
func ValidateXDNSInbound(ib database.InboundConfig) error {
	if ib.ListenPort == 0 {
		return fmt.Errorf("listen_port is required for xdns")
	}
	if ib.XDNSDomain == "" {
		return fmt.Errorf("xdns_domain is required (e.g. \"t.edgn.net:txt\")")
	}
	if ib.XDNSResolvers == "" {
		return fmt.Errorf("xdns_resolvers is required (comma-separated resolver specs)")
	}
	return nil
}

// splitResolvers — режет XDNSResolvers по запятой, срезая пробелы и пустые.
func splitResolvers(s string) []string {
	out := []string{}
	for _, r := range strings.Split(s, ",") {
		if r = strings.TrimSpace(r); r != "" {
			out = append(out, r)
		}
	}
	return out
}

// xdnsClientFinalmask — клиентский блок finalmask для ссылки (fm).
func xdnsClientFinalmask(resolvers string) string {
	block := map[string]any{
		"udp": []any{map[string]any{
			"type":     "xdns",
			"settings": map[string]any{"resolvers": splitResolvers(resolvers)},
		}},
	}
	b, _ := json.Marshal(block)
	return string(b)
}

type xdnsVLessClient struct {
	ID string `json:"id"`
}

type xdnsInboundSettings struct {
	Clients    []xdnsVLessClient `json:"clients"`
	Decryption string            `json:"decryption"`
}

type xdnsKCPSettings struct {
	MTU int `json:"mtu"`
}

type xdnsStreamSettings struct {
	Network     string          `json:"network"`
	KCPSettings xdnsKCPSettings `json:"kcpSettings"`
	Finalmask   json.RawMessage `json:"finalmask"`
}

type xdnsInbound struct {
	Tag            string              `json:"tag"`
	Listen         string              `json:"listen"`
	Port           int                 `json:"port"`
	Protocol       string              `json:"protocol"`
	Settings       xdnsInboundSettings `json:"settings"`
	StreamSettings xdnsStreamSettings  `json:"streamSettings"`
}

type xdnsConfig struct {
	Log       xrayLog        `json:"log"`
	Inbounds  []xdnsInbound  `json:"inbounds"`
	Outbounds []xrayOutbound `json:"outbounds"`
}

// buildXrayXDNSConfig — чистая функция: JSON Xray для XDNS на Hetzner.
// nil,nil если xdns-инбаундов нет. Ошибка если у enabled нет ключей (setup).
func buildXrayXDNSConfig(inbounds []database.InboundConfig, users []database.User) ([]byte, error) {
	clients := []xdnsVLessClient{}
	for _, u := range buildNewUsers(users) {
		clients = append(clients, xdnsVLessClient{ID: u.UUID})
	}
	cfg := xdnsConfig{
		Log:       xrayLog{Loglevel: "warning"},
		Inbounds:  []xdnsInbound{},
		Outbounds: []xrayOutbound{{Protocol: "freedom", Tag: "direct"}},
	}
	for _, ib := range inbounds {
		if ib.Protocol != "xdns" {
			continue
		}
		if err := ValidateXDNSInbound(ib); err != nil {
			return nil, fmt.Errorf("xdns %q: %w", ib.Tag, err)
		}
		if ib.XDNSDecryption == "" {
			return nil, fmt.Errorf("xdns %q: нет ключей VLESS — выполните POST /api/xray/xdns/setup", ib.Tag)
		}
		serverFM, _ := json.Marshal(map[string]any{
			"udp": []any{map[string]any{
				"type":     "xdns",
				"settings": map[string]any{"domains": []string{ib.XDNSDomain}},
			}},
		})
		cfg.Inbounds = append(cfg.Inbounds, xdnsInbound{
			Tag:      ib.Tag,
			Listen:   "0.0.0.0",
			Port:     ib.ListenPort,
			Protocol: "vless",
			Settings: xdnsInboundSettings{Clients: clients, Decryption: ib.XDNSDecryption},
			StreamSettings: xdnsStreamSettings{
				Network:     "kcp",
				KCPSettings: xdnsKCPSettings{MTU: XDNSServerMTU},
				Finalmask:   json.RawMessage(serverFM),
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

Run: `go test ./service/ -run 'XDNS' -v`
Expected: PASS (5 тестов).

- [ ] **Step 5: Commit**

```bash
git add service/xrayxdns.go service/xrayxdns_test.go
git commit -m "feat(xdns): чистый билдер конфига XDNS + валидация"
```

---

### Task 3: Ссылка XDNS

**Files:**
- Modify: `service/vpn.go` (`GenerateLinkForInbound` — ветка для `Protocol=="xdns"`; новая функция рядом с `GenerateMaskLink`)
- Test: `service/xrayxdns_test.go`

**Interfaces:**
- Consumes: `xdnsClientFinalmask`, `splitResolvers` (Task 2).
- Produces: `func GenerateXDNSLink(ib database.InboundConfig, user database.User) string` — чистая; `GenerateLinkForInbound` для `Protocol=="xdns"` вызывает её (пустая строка если нет резолверов/ключей).

- [ ] **Step 1: Написать падающий тест**

В `service/xrayxdns_test.go`:

```go
func TestGenerateXDNSLink(t *testing.T) {
	ib := xdnsFixture()
	user := database.User{Username: "alice", UUID: "550e8400-e29b-41d4-a716-446655440000"}
	link := GenerateXDNSLink(ib, user)
	if !strings.HasPrefix(link, "vless://550e8400-e29b-41d4-a716-446655440000@8.8.8.8:53?") {
		t.Fatalf("expected first resolver host, got %s", link)
	}
	for _, want := range []string{"type=kcp", "encryption=mlkem768x25519plus", "fm=%7B%22udp%22", "seed=130"} {
		if !strings.Contains(link, want) {
			t.Errorf("missing %s in %s", want, link)
		}
	}
	if !strings.HasSuffix(link, "#XDNS-alice") {
		t.Errorf("expected #XDNS-alice fragment, got %s", link)
	}
}

func TestGenerateXDNSLink_NoResolvers(t *testing.T) {
	ib := xdnsFixture()
	ib.XDNSResolvers = ""
	if got := GenerateXDNSLink(ib, database.User{UUID: "u"}); got != "" {
		t.Fatalf("expected empty link without resolvers, got %q", got)
	}
}
```

Примечание: адрес назначения — первый резолвер, но резолвер в формате `domain:method+udp://IP:port`. Хост для ссылки — IP:port из первого резолвера. Парсить: взять подстроку после `+udp://`.

- [ ] **Step 2: Запустить, убедиться что падает**

Run: `go test ./service/ -run 'XDNSLink' -v`
Expected: FAIL — undefined `GenerateXDNSLink`.

- [ ] **Step 3: Реализовать**

В `service/vpn.go` в `GenerateLinkForInbound`, сразу после ветки `if ib.Protocol == "mask"` добавить:

```go
	if ib.Protocol == "xdns" {
		return GenerateXDNSLink(ib, user)
	}
```

Рядом с `GenerateMaskLink` добавить (нужны импорты `net/url`, `fmt`, `strings` — уже есть в vpn.go):

```go
// firstResolverHost достаёт "IP:port" из первого резолвера XDNSResolvers
// (формат domain[:method]+udp://IP:port). "" если не распарсить.
func firstResolverHost(resolvers string) string {
	rs := splitResolvers(resolvers)
	if len(rs) == 0 {
		return ""
	}
	i := strings.Index(rs[0], "+udp://")
	if i < 0 {
		return ""
	}
	return strings.TrimSuffix(rs[0][i+len("+udp://"):], "/")
}

// GenerateXDNSLink — ссылка XDNS для Xray-клиентов (Happ). Адрес назначения —
// первый резолвер, а не Hetzner: рекурсия резолвера доходит до нашего
// authoritative и «отмывает» L3. Понимают только Xray-клиенты.
func GenerateXDNSLink(ib database.InboundConfig, user database.User) string {
	host := firstResolverHost(ib.XDNSResolvers)
	if host == "" || ib.XDNSEncryption == "" {
		return ""
	}
	q := url.Values{}
	q.Set("type", "kcp")
	q.Set("seed", fmt.Sprintf("%d", XDNSClientMTU)) // клиентский MTU через kcp seed-параметр
	q.Set("encryption", ib.XDNSEncryption)
	q.Set("fm", xdnsClientFinalmask(ib.XDNSResolvers))
	u := url.URL{
		Scheme:   "vless",
		User:     url.User(user.UUID),
		Host:     host,
		RawQuery: q.Encode(),
		Fragment: "XDNS-" + user.Username,
	}
	return u.String()
}
```

Примечание про `seed`: v2rayNG/Happ передают клиентский kcp MTU через параметр ссылки. Если при ручной проверке окажется, что нужный ключ — `mtu`, а не `seed`, это правится в одной строке; тест проверяет наличие подстроки, поправить обе (тест + код) вместе. **Оставить `seed=130`; при провале ручной проверки этапа сменить на `mtu=130`.**

- [ ] **Step 4: Прогнать тесты**

Run: `go test ./service/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add service/vpn.go service/xrayxdns_test.go
git commit -m "feat(xdns): генерация ссылки XDNS для Xray-клиентов"
```

---

### Task 4: Локальный деплой XDNS на Hetzner

**Files:**
- Create: `service/xrayhetzner.go`
- Modify: `service/vpn.go` (`GenerateAndReload` — добавить деплой XDNS после RuVDS-шага)

**Interfaces:**
- Consumes: `buildXrayXDNSConfig` (Task 2); `XrayVersion`, `XrayLinux64SHA256` (`service/xray.go`); `database.DB`.
- Produces: `InstallXrayHetzner() error`, `EnsureXrayXDNSService() error`, `GenerateXDNSConfig() ([]byte, error)`, `DeployXDNSConfig([]byte) error`, `StartXrayXDNS()/StopXrayXDNS()/IsXrayXDNSRunning() bool/XrayXDNSLogs(int) (string,error)`, `XrayXDNSVersion() string`, `EnsureXDNSKeys() error`, `SetupXrayXDNS() error`, `HasXDNSInbounds() bool`, `Port53Owner() string`.

**Паттерн:** `service/xrayhetzner.go` повторяет структуру `service/xrayruvds.go`, НО все команды локальные через `os/exec` (`exec.Command`), а не `runSSH`. Прочитать `service/xrayruvds.go` целиком как образец перед написанием.

- [ ] **Step 1: Реализовать `service/xrayhetzner.go`**

Константы и хелперы:

```go
package service

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"vpnbot/database"
)

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
```

Функции (по образцу `xrayruvds.go`, локально):

- `XrayXDNSVersion()` — `runLocal(XrayXDNSBinaryPath + " version 2>/dev/null | head -1")`, TrimSpace.
- `InstallXrayHetzner()` — если `xray version` содержит `"Xray "+XrayVersion+" "` → skip. Иначе wget зип с github (Hetzner до CDN достаёт), `sha256sum -c`, распаковка `python3 -m zipfile` (или `unzip` если есть — проверить `which unzip`, но python3 надёжнее), `install -m0755 xray /usr/local/bin/xray`, geoip/geosite в `/usr/local/share/xray/`. Идентично команде из `InstallXrayRuVDS`, но через `runLocal`.
- `EnsureXrayXDNSService()` — записать unit в `XrayXDNSServicePath` (тот же вид, что `xray.service` на RuVDS, но `Description=Xray XDNS channel (managed by vpnbot)`, `ExecStart=%s run -c %s` с `XrayXDNSBinaryPath`/`XrayXDNSConfigPath`, `AmbientCapabilities=CAP_NET_BIND_SERVICE` — нужен для bind :53, `Environment=XRAY_LOCATION_ASSET=/usr/local/share/xray`), `systemctl daemon-reload`.
- `GenerateXDNSConfig()` — читает enabled-инбаунды и активных `User` из БД, `return buildXrayXDNSConfig(inbounds, users)`.
- `DeployXDNSConfig(cfgJSON)` — если nil → `systemctl stop xray-xdns` (fail-silent), return nil. Иначе: `test -x` бинаря (иначе ошибка «Xray не установлен: POST /api/xray/xdns/setup»), write `.new`, `xray run -test -format json -c .new` (проверить «Configuration OK», иначе rm .new + ошибка), `cmp -s .new config → mv/rm`, restart только если changed И active. Скопировать логику из `DeployXrayConfigRuVDS`, заменив `runSSH(client,...)` на `runLocal(...)`.
- `Start/Stop/IsRunning/Logs` — локальные `systemctl` (`IsXrayXDNSRunning`: `systemctl is-active xray-xdns`).
- `EnsureXDNSKeys()` — для каждого enabled xdns-инбаунда с пустым `XDNSDecryption`: `runLocal(XrayXDNSBinaryPath + " vlessenc")`, распарсить пару после строки «Authentication: X25519» (`decryption`/`encryption`), сохранить в БД (`database.DB.Model(&ib).Updates(...)`). Парсер: искать строки, содержащие `"decryption":` и `"encryption":`, брать значение в кавычках. Требует установленного бинаря — вызывать после `InstallXrayHetzner`.
- `Port53Owner()` — `runLocal("ss -lunp | grep ':53 ' | head -1")`, TrimSpace (диагностика: занят ли 53).
- `HasXDNSInbounds()` — `Count` строк `protocol='xdns'` в БД (любой enabled).
- `SetupXrayXDNS()`:
  ```go
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
  	if owner := Port53Owner(); owner != "" && !strings.Contains(owner, XrayXDNSServiceName) {
  		return fmt.Errorf("порт 53 занят другим процессом (%s): освободите его (остановите slipstream, снимите REDIRECT 53→5300) и повторите", strings.TrimSpace(owner))
  	}
  	if err := DeployXDNSConfig(cfgJSON); err != nil {
  		return err
  	}
  	return StartXrayXDNS()
  }
  ```
  `os` импортируется для возможных file-проверок; если не нужен — убрать из импортов.

- [ ] **Step 2: Подключить в `GenerateAndReload`**

В `service/vpn.go`, в `GenerateAndReload`, после блока RuVDS (`if IsRuVDSEnabled() { ... GenerateAndReloadRuVDS() ... }`) добавить:

```go
	// XDNS-канал на Hetzner (локальный Xray). Ошибка не рушит основной reload.
	if HasXDNSInbounds() {
		xdnsJSON, xerr := GenerateXDNSConfig()
		if xerr != nil {
			log.Println("XDNS config error:", xerr)
		} else if derr := DeployXDNSConfig(xdnsJSON); derr != nil {
			log.Println("XDNS deploy error:", derr)
		}
	}
```

- [ ] **Step 3: Vet, сборка, тесты**

Run: `gofmt -l service/xrayhetzner.go service/vpn.go && go vet ./service/ && go build -o /dev/null . && go test ./service/`
Expected: без ошибок.

- [ ] **Step 4: Commit**

```bash
git add service/xrayhetzner.go service/vpn.go
git commit -m "feat(xdns): локальный деплой Xray-XDNS на Hetzner + wiring в reload"
```

---

### Task 5: API — валидация, скип в подписках, роуты XDNS

**Files:**
- Modify: `api/handlers/inbounds.go` (`CreateInbound`, `UpdateInbound` — ветка xdns)
- Modify: `api/handlers/subscription.go` (`GetSubscription`, `GetSubscriptionRuVDS` — скип xdns)
- Create: `api/handlers/xrayxdns.go`
- Modify: `api/router/router.go` (роуты `/api/xray/xdns/*`)

**Interfaces:**
- Consumes: `service.ValidateXDNSInbound`, `service.SetupXrayXDNS`, `service.GenerateAndReload`, `service.StartXrayXDNS`, `service.StopXrayXDNS`, `service.IsXrayXDNSRunning`, `service.XrayXDNSVersion`, `service.XrayXDNSLogs`, `service.GenerateXDNSConfig`, `service.Port53Owner`, `service.XrayVersion`.
- Produces: роуты `/api/xray/xdns/{setup,reload,start,stop,status,config,logs}`.

- [ ] **Step 1: Валидация в inbounds.go**

Проверить актуальные строки протокол-проверок (мог сместиться номер строки). В `CreateInbound` и `UpdateInbound` расширить список допустимых протоколов на `"xdns"` и добавить ветку в `switch` (там, где уже `case "mask":` из этапа 1):

```go
		case "xdns":
			if msg := validateXDNSInboundHandler(&input); msg != "" {
				c.JSON(http.StatusBadRequest, gin.H{"error": msg})
				return
			}
```

Хелпер рядом с `validateMaskInbound`:

```go
// validateXDNSInboundHandler — проверки xdns-инбаунда при create/update.
func validateXDNSInboundHandler(input *database.InboundConfig) string {
	if err := service.ValidateXDNSInbound(*input); err != nil {
		return err.Error()
	}
	// exit решает freedom-аутбаунд самого XDNS; ExitOutbound не нужен.
	input.ExitOutbound = ""
	return ""
}
```

В `UpdateInbound` для xdns, как и для mask, мерджить partial-поля из `existing` перед валидацией (по образцу mask-ветки) и, если effective protocol == "xdns", после `Updates(input)` явно обнулить exit_outbound: `database.DB.Model(&existing).Update("exit_outbound", "")` — тот же приём, что применён для mask (GORM `Updates(struct)` пропускает нулевые значения).

Проверить также блок допустимых протоколов (строки вида `input.Protocol != "vless" && ... != "mask"`) — добавить `&& input.Protocol != "xdns"` и обновить текст ошибки.

- [ ] **Step 2: Скип xdns в подписках**

В `api/handlers/subscription.go`, в обоих циклах (`GetSubscription` и `GetSubscriptionRuVDS`) рядом со скипом маски добавить xdns:

```go
			if ib.Protocol == "mask" || ib.Protocol == "xdns" {
				continue
			}
```

(в `GetSubscription` скип маски уже есть — расширить условием; в `GetSubscriptionRuVDS` скип маски был добавлен ранее — расширить так же.)

- [ ] **Step 3: Handlers `api/handlers/xrayxdns.go`**

По образцу `api/handlers/xrayruvds.go`:

```go
package handlers

import (
	"strconv"
	"vpnbot/service"

	"github.com/gin-gonic/gin"
)

// POST /api/xray/xdns/setup
func SetupXrayXDNS() gin.HandlerFunc {
	return func(c *gin.Context) {
		if err := service.SetupXrayXDNS(); err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, gin.H{"message": "XDNS установлен и запущен на Hetzner", "version": service.XrayVersion})
	}
}

// POST /api/xray/xdns/reload
func ReloadXrayXDNS() gin.HandlerFunc {
	return func(c *gin.Context) {
		if err := service.GenerateAndReload(); err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, gin.H{"message": "XDNS перегенерирован"})
	}
}

// POST /api/xray/xdns/start
func StartXrayXDNS() gin.HandlerFunc {
	return func(c *gin.Context) {
		if err := service.StartXrayXDNS(); err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, gin.H{"message": "XDNS запущен"})
	}
}

// POST /api/xray/xdns/stop
func StopXrayXDNS() gin.HandlerFunc {
	return func(c *gin.Context) {
		if err := service.StopXrayXDNS(); err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, gin.H{"message": "XDNS остановлен"})
	}
}

// GET /api/xray/xdns/status
func GetXrayXDNSStatus() gin.HandlerFunc {
	return func(c *gin.Context) {
		status := "stopped"
		if service.IsXrayXDNSRunning() {
			status = "running"
		}
		c.JSON(200, gin.H{
			"status":            status,
			"installed_version": service.XrayXDNSVersion(),
			"pinned_version":    service.XrayVersion,
			"port53_owner":      service.Port53Owner(),
		})
	}
}

// GET /api/xray/xdns/config
func PreviewXrayXDNSConfig() gin.HandlerFunc {
	return func(c *gin.Context) {
		cfg, err := service.GenerateXDNSConfig()
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

// GET /api/xray/xdns/logs?lines=50
func GetXrayXDNSLogs() gin.HandlerFunc {
	return func(c *gin.Context) {
		lines, _ := strconv.Atoi(c.DefaultQuery("lines", "50"))
		out, err := service.XrayXDNSLogs(lines)
		if err != nil {
			c.JSON(500, gin.H{"error": err.Error(), "logs": out})
			return
		}
		c.JSON(200, gin.H{"logs": out})
	}
}
```

- [ ] **Step 4: Роуты в router.go**

После блока `/api/xray/ruvds/*` добавить:

```go
			// XDNS-канал на Hetzner (локальный Xray)
			auth.POST("/xray/xdns/setup", handlers.SetupXrayXDNS())
			auth.POST("/xray/xdns/reload", handlers.ReloadXrayXDNS())
			auth.POST("/xray/xdns/start", handlers.StartXrayXDNS())
			auth.POST("/xray/xdns/stop", handlers.StopXrayXDNS())
			auth.GET("/xray/xdns/status", handlers.GetXrayXDNSStatus())
			auth.GET("/xray/xdns/config", handlers.PreviewXrayXDNSConfig())
			auth.GET("/xray/xdns/logs", handlers.GetXrayXDNSLogs())
```

- [ ] **Step 5: Vet, сборка, тесты**

Run: `gofmt -l api/handlers/inbounds.go api/handlers/subscription.go api/handlers/xrayxdns.go && go vet ./api/... && go build -o /dev/null . && go test ./service/`
Expected: без ошибок (router.go имеет предсуществующий gofmt-дрейф — не переформатировать, только добавить строки).

- [ ] **Step 6: Commit**

```bash
git add api/handlers/inbounds.go api/handlers/subscription.go api/handlers/xrayxdns.go api/router/router.go
git commit -m "feat(api): валидация xdns, скип в подписках, роуты /api/xray/xdns/*"
```

---

### Task 6: Бот — кнопка XDNS (Happ)

**Files:**
- Modify: `bot/bot.go` (меню, `conn_link`, `conn_qr`, `linkServerAddr`, `qrCaption`)

**Interfaces:**
- Consumes: `service.GenerateLinkForInbound` (для xdns сам берёт резолвер как адрес).

- [ ] **Step 1: Метка кнопки и адрес**

В цикле кнопок меню, где для mask ставится `label = ib.DisplayName + " (Happ)"`, расширить на xdns:

```go
			if ib.Protocol == "mask" || ib.Protocol == "xdns" {
				label = ib.DisplayName + " (Happ)"
			}
```

`linkServerAddr` для xdns вернуть пустую строку (адрес зашит в ссылку самим `GenerateXDNSLink` через резолвер), чтобы `GenerateLinkForInbound(ib, user, "")` для xdns сработал — он игнорирует `serverAddr` для xdns. Проверить: `GenerateLinkForInbound` для xdns вызывает `GenerateXDNSLink(ib, user)` без `serverAddr`, так что значение адреса неважно. Оставить `linkServerAddr` как есть (для mask возвращает RuVDS IP), а для xdns любой addr пройдёт. Достаточно, чтобы xdns-инбаунд не попал в mask-ветку `GetRuVDSIP()`; поэтому:

```go
func linkServerAddr(ib database.InboundConfig) string {
	if ib.Protocol == "mask" {
		return service.GetRuVDSIP()
	}
	return ServerIP // для xdns игнорируется GenerateXDNSLink
}
```

(без изменений, если уже так — тогда шаг пропустить.)

- [ ] **Step 2: Тексты в conn_link/conn_qr/qrCaption**

Обработчики `conn_link`/`conn_qr` уже проверяют `link == ""` и для mask показывают предупреждение про клиенты. Расширить предупреждение на xdns — заменить условие `if ib.Protocol == "mask"` в `conn_link` на `if ib.Protocol == "mask" || ib.Protocol == "xdns"`, и в `qrCaption` так же. Текст оставить прежний (Happ основной), для xdns он подходит.

- [ ] **Step 3: Vet, сборка**

Run: `gofmt -l bot/bot.go && go vet ./bot/ && go build -o /dev/null .`
Expected: без ошибок.

- [ ] **Step 4: Commit**

```bash
git add bot/bot.go
git commit -m "feat(bot): кнопка XDNS (Happ)"
```

---

### Task 7: Документация и рунбук переезда с slipstream

**Files:**
- Modify: `CLAUDE.md` (Packages, InboundConfig model, API Structure)
- Modify: `AGENTS.md` — пропустить, файл не в git (см. этап 1)
- Create: `docs/xdns-runbook.md`
- Modify: `docs/superpowers/specs/2026-09-09-xray-finalmask-design.md` (статус этапа 2)

- [ ] **Step 1: CLAUDE.md**

В «Packages/service» дополнить: `, XDNS channel on Hetzner (\`xrayxdns.go\` — pure builder, \`xrayhetzner.go\` — local deploy of \`xray-xdns.service\` on :53)`.

В «InboundConfig model» добавить:

```
- `Protocol`: ... | `"xdns"`
- `XDNSDomain`, `XDNSResolvers`, `XDNSDecryption`, `XDNSEncryption`: xdns-only (Protocol="xdns"). A second local Xray instance on Hetzner (`xray-xdns.service`, `/etc/xray-xdns/config.json`) listens VLESS+mKCP on `ListenPort` (53) with finalmask xdns, tunnelling data inside DNS queries. VLESS encryption pair generated by `xray vlessenc` at setup. Not emitted into `/sub` or `/sub-ruvds`; distributed only via the bot `XDNS (Happ)` button. Xray-based clients only.
```

В «API Structure» добавить:

```
- Xray XDNS (Hetzner): `/api/xray/xdns/{setup,reload,start,stop,status,config,logs}` — local Xray on :53. `setup` requires port 53 free (stop slipstream first — see docs/xdns-runbook.md).
```

- [ ] **Step 2: Рунбук `docs/xdns-runbook.md`**

Написать порядок активации (на русском):
1. Создать xdns-инбаунд через `POST /api/inbounds` (protocol=xdns, listen_port=53, xdns_domain=`t.edgn.net:txt`, xdns_resolvers, enabled=true). Ключи не задавать — сгенерятся при setup.
2. Освободить порт 53: `systemctl stop slipstream-server`, снять `iptables -t nat -D PREROUTING -i eth0 -p udp --dport 53 -j REDIRECT --to-ports 5300` (проверить точное правило `iptables -t nat -S PREROUTING | grep 5300`). Записать откат.
3. `POST /api/xray/xdns/setup`.
4. Проверка: `GET /api/xray/xdns/status` → running, port53_owner = xray; `dig @<hetzner> TXT probe.t.edgn.net` отвечает; тестер из РФ с Happ.
5. Откат: `POST /api/xray/xdns/stop`, вернуть REDIRECT, `systemctl start slipstream-server`.

Отметить: резолверы для РФ подбирает тестер (8.8.8.8/1.1.1.1 заблокированы); при смене резолверов — `PUT /api/inbounds/<id>` + `POST /api/xray/xdns/reload`.

- [ ] **Step 3: Спека — статус этапа 2**

В `docs/superpowers/specs/2026-09-09-xray-finalmask-design.md` в раздел «Верификация» этапа 2 добавить строку: «Статус: код этапа 2 реализован 2026-09-14 (ветка feature/xray-xdns), активация по docs/xdns-runbook.md, ручная проверка из РФ не проводилась.»

- [ ] **Step 4: Commit**

```bash
git add CLAUDE.md docs/xdns-runbook.md docs/superpowers/specs/2026-09-09-xray-finalmask-design.md
git commit -m "docs: XDNS-канал и рунбук переезда с slipstream"
```

---

## Вне скоупа

- Убийство slipstream из кода (операторский шаг, рунбук).
- Админ-панель (`vpn-admin-panel`) для xdns-полей — через API.
- Мультипуть/несколько резолверов с автопереключением — Xray xdns сам ходит через любой доступный резолвер из списка.
- Замер резолверного пути из РФ — нужен тестер.
