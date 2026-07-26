# VPN DPI Hardening (2026) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Применить обходы DPI из статьи Habr (padding, uTLS, ShadowTLS v3, zapret) + добавить direct-exit категорию инбаундов с русского IP RuVDS, без даунтайма для 34 существующих пользователей.

**Architecture:** 4 этапа rollout по возрастанию риска. Существующие 10 VLESS инбаундов и wg-туннель RuVDS→Hetzner не трогаются. Env-driven конфиг-расширение сохраняет wg-out на RuVDS через `EXTRA_OUTBOUND_JSON_PATH` + `ROUTE_FINAL`. Новые инбаунды получают `ExitOutbound` — per-inbound route rules. Пользователь вручную валидирует каждый этап перед следующим.

**Tech Stack:** Go 1.21, GORM/SQLite, sing-box, systemd, nftables, zapret/nfqws, uTLS chrome fingerprint. Тесты через стандартный `go test`.

**Related spec:** `docs/superpowers/specs/2026-07-11-vpn-dpi-hardening-design.md`

---

## File Structure

**Modified (Go):**
- `service/vpn.go` — `RouteRule` (+Inbound), `MultiplexConfig` (+Padding, +MaxStreams), `SingboxInbound` (+ShadowTLS fields), `GenerateAndReload()` (env-driven outbound + per-inbound rules), `buildSingboxInbound()` (returns `[]SingboxInbound`), `GenerateLinkForInbound()` (+shadowtls branch).
- `database/database.go` — `InboundConfig` (+`ExitOutbound`, +`MuxPadding`, +`MuxMaxStreams`, +`ShadowTLSPassword`, +`ShadowTLSVersion`, +`CoverDomain`, +`InnerMethod`, +`InnerPassword`), 3 новых seed inbound'а (2 в этапе 1, 1 в этапе 4).

**Created (Go tests):**
- `service/vpn_test.go` — unit-тесты для функций генерации конфига (чистые, без DB/сети).

**Created (deploy artifacts):**
- `deploy/ruvds/zapret/README.md` — инструкция ручного разворачивания.
- `deploy/ruvds/zapret/nfqws.service` — systemd unit.
- `deploy/ruvds/zapret/nftables-nfqws.rules` — nftables правила.
- `deploy/ruvds/extra-outbound.json.example` — образец wg-out файла.

**Deployed on servers (not in repo):**
- RuVDS: `/etc/vpnbot/extra-outbound.json` — актуальный wg-out (заполнить из существующего конфига).
- RuVDS: `/opt/VpnBot/.env` — добавить `EXTRA_OUTBOUND_JSON_PATH` и `ROUTE_FINAL`.

---

# Этап 1 — Routing rework + 2 direct-exit inbounds

### Task 1.1: Test — RouteRule умеет ограничивать правило по inbound

**Files:**
- Create: `service/vpn_test.go`

- [ ] **Step 1: Write the failing test**

```go
package service

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRouteRule_InboundOmitEmpty(t *testing.T) {
	// Пустой Inbound не должен сериализоваться (backwards compat).
	r := RouteRule{IPCIDR: []string{"10.0.0.0/8"}, Outbound: "block"}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "inbound") {
		t.Fatalf("empty Inbound must be omitted, got: %s", b)
	}
}

func TestRouteRule_InboundSerialized(t *testing.T) {
	r := RouteRule{Inbound: []string{"vless-direct-xhttp"}, Outbound: "direct"}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	if !strings.Contains(got, `"inbound":["vless-direct-xhttp"]`) {
		t.Fatalf("expected inbound field, got: %s", got)
	}
	if !strings.Contains(got, `"outbound":"direct"`) {
		t.Fatalf("expected outbound=direct, got: %s", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /home/markriaz/vpn-backend-tg-bot && go test ./service/ -run TestRouteRule -v`
Expected: FAIL — `RouteRule` не имеет поля `Inbound`, компиляция упадёт с `unknown field 'Inbound' in struct literal of type RouteRule`.

- [ ] **Step 3: Add `Inbound` field to `RouteRule`**

Modify: `service/vpn.go:42-45` — добавить одно поле:
```go
type RouteRule struct {
	Inbound  []string `json:"inbound,omitempty"`
	IPCIDR   []string `json:"ip_cidr,omitempty"`
	Outbound string   `json:"outbound"`
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./service/ -run TestRouteRule -v`
Expected: PASS — обе подтеста зелёные.

- [ ] **Step 5: Commit**

```bash
git add service/vpn.go service/vpn_test.go
git commit -m "feat(service): add Inbound field to RouteRule for per-inbound routing"
```

---

### Task 1.2: Test — env-driven extra outbound injection

- [ ] **Step 1: Write the failing test**

Append to `service/vpn_test.go`:
```go
import (
	"os"
	"path/filepath"
)

func TestLoadExtraOutbound_MissingEnv(t *testing.T) {
	os.Unsetenv("EXTRA_OUTBOUND_JSON_PATH")
	got, err := loadExtraOutbound()
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("expected nil when env missing, got %v", got)
	}
}

func TestLoadExtraOutbound_ReadsFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wg.json")
	content := `{"type":"wireguard","tag":"wg-out","server":"1.2.3.4","server_port":51820}`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("EXTRA_OUTBOUND_JSON_PATH", path)

	got, err := loadExtraOutbound()
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("expected non-nil outbound")
	}
	if got["tag"] != "wg-out" {
		t.Fatalf("expected tag=wg-out, got %v", got["tag"])
	}
}

func TestLoadExtraOutbound_MissingFileIsError(t *testing.T) {
	t.Setenv("EXTRA_OUTBOUND_JSON_PATH", "/nonexistent/wg.json")
	_, err := loadExtraOutbound()
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./service/ -run TestLoadExtraOutbound -v`
Expected: FAIL — функция `loadExtraOutbound` не существует.

- [ ] **Step 3: Implement `loadExtraOutbound`**

Add to `service/vpn.go` (перед `GenerateAndReload`):
```go
// loadExtraOutbound читает JSON-файл, указанный в EXTRA_OUTBOUND_JSON_PATH.
// Возвращает (nil, nil) если env не задан. Возвращает ошибку если файл указан, но нечитаем.
func loadExtraOutbound() (map[string]any, error) {
	path := os.Getenv("EXTRA_OUTBOUND_JSON_PATH")
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read extra outbound: %w", err)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("parse extra outbound: %w", err)
	}
	return out, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./service/ -run TestLoadExtraOutbound -v`
Expected: PASS — все 3 подтеста зелёные.

- [ ] **Step 5: Commit**

```bash
git add service/vpn.go service/vpn_test.go
git commit -m "feat(service): add env-driven extra outbound loader for wg-out injection"
```

---

### Task 1.3: Change `OutboundConfig` to interface (allow raw JSON)

Проблема: `[]OutboundConfig` не даст добавить wg-out через `map[string]any`. Нужна возможность смешивать типизированные outbound'ы с raw JSON.

**Files:**
- Modify: `service/vpn.go:32-35` (SingBoxConfig), `service/vpn.go:122-125` (OutboundConfig)

- [ ] **Step 1: Write test for mixed outbounds**

Append to `service/vpn_test.go`:
```go
func TestSingBoxConfig_OutboundsMixedTypes(t *testing.T) {
	cfg := SingBoxConfig{
		Outbounds: []any{
			map[string]any{"type": "wireguard", "tag": "wg-out", "server": "1.2.3.4"},
			OutboundConfig{Type: "direct", Tag: "direct"},
		},
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	if !strings.Contains(got, `"tag":"wg-out"`) {
		t.Fatalf("expected wg-out, got %s", got)
	}
	if !strings.Contains(got, `"tag":"direct"`) {
		t.Fatalf("expected direct, got %s", got)
	}
}
```

- [ ] **Step 2: Run to fail**

Run: `go test ./service/ -run TestSingBoxConfig_OutboundsMixedTypes -v`
Expected: FAIL — `Outbounds` имеет тип `[]OutboundConfig`, не `[]any`.

- [ ] **Step 3: Change `Outbounds` type to `[]any`**

Modify `service/vpn.go:29-35`:
```go
type SingBoxConfig struct {
	Log          LogConfig           `json:"log"`
	Experimental *ExperimentalConfig `json:"experimental,omitempty"`
	Inbounds     []SingboxInbound    `json:"inbounds"`
	Outbounds    []any               `json:"outbounds"`
	Route        *RouteConfig        `json:"route,omitempty"`
}
```

- [ ] **Step 4: Update `GenerateAndReload` to use `[]any`**

Modify `service/vpn.go:269-272` (заменить блок Outbounds):
```go
		Outbounds: []any{
			OutboundConfig{Type: "direct", Tag: "direct"},
			OutboundConfig{Type: "block", Tag: "block"},
		},
```

- [ ] **Step 5: Run all tests and build**

Run: `go build ./... && go test ./service/ -v`
Expected: build OK, все тесты PASS.

- [ ] **Step 6: Commit**

```bash
git add service/vpn.go service/vpn_test.go
git commit -m "refactor(service): use []any for Outbounds to allow raw JSON injection"
```

---

### Task 1.4: `GenerateAndReload` инжектит extra outbound + меняет `route.final`

- [ ] **Step 1: Extract config-building into pure function + test**

Разделяем: чистая функция `buildSingBoxConfig(inbounds, users, extraOut, finalTag) SingBoxConfig` (testable) и обёртка `GenerateAndReload()` (I/O).

Append to `service/vpn_test.go`:
```go
import "vpnbot/database"

func TestBuildSingBoxConfig_NoExtraOutbound(t *testing.T) {
	cfg := buildSingBoxConfig(nil, nil, nil, "")
	if len(cfg.Outbounds) != 2 {
		t.Fatalf("expected 2 outbounds (direct, block), got %d", len(cfg.Outbounds))
	}
	if cfg.Route.Final != "direct" {
		t.Fatalf("expected route.final=direct, got %q", cfg.Route.Final)
	}
}

func TestBuildSingBoxConfig_WithExtraOutbound(t *testing.T) {
	extra := map[string]any{"type": "wireguard", "tag": "wg-out"}
	cfg := buildSingBoxConfig(nil, nil, extra, "wg-out")
	if len(cfg.Outbounds) != 3 {
		t.Fatalf("expected 3 outbounds, got %d", len(cfg.Outbounds))
	}
	first, ok := cfg.Outbounds[0].(map[string]any)
	if !ok || first["tag"] != "wg-out" {
		t.Fatalf("expected first outbound wg-out, got %+v", cfg.Outbounds[0])
	}
	if cfg.Route.Final != "wg-out" {
		t.Fatalf("expected route.final=wg-out, got %q", cfg.Route.Final)
	}
}

func TestBuildSingBoxConfig_PerInboundRule(t *testing.T) {
	ib := database.InboundConfig{
		Tag:           "vless-direct-xhttp",
		Protocol:      "vless",
		ListenPort:    2059,
		TLSType:       "reality",
		Transport:     "xhttp",
		SNI:           "yastatic.net",
		UserType:      "new",
		Enabled:       true,
		ExitOutbound:  "direct",
		RealityPrivateKey: "x", RealityPublicKey: "y",
		RealityShortIDs:   database.JSONStringArray{"abcd"},
	}
	cfg := buildSingBoxConfig([]database.InboundConfig{ib}, nil, nil, "")
	// Первое правило должно быть per-inbound, второе — bogon.
	if len(cfg.Route.Rules) < 2 {
		t.Fatalf("expected >=2 rules, got %d", len(cfg.Route.Rules))
	}
	first := cfg.Route.Rules[0]
	if len(first.Inbound) != 1 || first.Inbound[0] != "vless-direct-xhttp" || first.Outbound != "direct" {
		t.Fatalf("expected first rule to route direct-xhttp→direct, got %+v", first)
	}
	// Bogon rule по-прежнему присутствует.
	found := false
	for _, r := range cfg.Route.Rules {
		if len(r.IPCIDR) > 0 && r.Outbound == "block" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("bogon block rule missing")
	}
}
```

- [ ] **Step 2: Run to fail**

Run: `go test ./service/ -run TestBuildSingBoxConfig -v`
Expected: FAIL — `buildSingBoxConfig` не существует; поле `ExitOutbound` в `InboundConfig` тоже нет.

- [ ] **Step 3: Add `ExitOutbound` to `InboundConfig`**

Modify `database/database.go:118-149`, добавить после строки с `SortOrder`:
```go
	ExitOutbound string `json:"exit_outbound"` // "" (=route.final) | "direct" | "wg-out"
```

- [ ] **Step 4: Extract `buildSingBoxConfig` из `GenerateAndReload`**

Modify `service/vpn.go` — вырезать логику построения config из `GenerateAndReload` в новую функцию, вставить перед `GenerateAndReload`:

```go
// buildSingBoxConfig строит sing-box конфиг из inbound'ов + пользователей.
// extraOutbound (nil ok) инжектится в начало outbounds.
// finalTag (пусто ok) — тег route.final; если пусто, используется "direct".
func buildSingBoxConfig(inbounds []database.InboundConfig, users []database.User, extraOutbound map[string]any, finalTag string) SingBoxConfig {
	singboxInbounds := []SingboxInbound{}
	inboundTags := []string{}
	perInboundRules := []RouteRule{}
	for _, ib := range inbounds {
		singboxInbounds = append(singboxInbounds, buildSingboxInbound(ib, users))
		inboundTags = append(inboundTags, ib.Tag)
		if ib.ExitOutbound != "" {
			perInboundRules = append(perInboundRules, RouteRule{
				Inbound:  []string{ib.Tag},
				Outbound: ib.ExitOutbound,
			})
		}
	}

	outbounds := []any{}
	if extraOutbound != nil {
		outbounds = append(outbounds, extraOutbound)
	}
	outbounds = append(outbounds,
		OutboundConfig{Type: "direct", Tag: "direct"},
		OutboundConfig{Type: "block", Tag: "block"},
	)

	if finalTag == "" {
		finalTag = "direct"
	}

	bogonRule := RouteRule{
		IPCIDR: []string{
			"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16",
			"169.254.0.0/16", "224.0.0.0/4", "240.0.0.0/4",
			"0.0.0.0/8", "100.64.0.0/10",
			"fc00::/7", "fe80::/10", "ff00::/8", "::/128",
		},
		Outbound: "block",
	}
	rules := append(perInboundRules, bogonRule)

	return SingBoxConfig{
		Log: LogConfig{
			Level:     "info",
			Timestamp: true,
			Output:    "/etc/sing-box/access.log",
		},
		Experimental: &ExperimentalConfig{
			V2RayAPI: V2RayAPIConfig{
				Listen: ApiAddr,
				Stats: StatsConfig{
					Enabled:  true,
					Inbounds: inboundTags,
					Users:    buildUserNames(users),
				},
			},
		},
		Inbounds:  singboxInbounds,
		Outbounds: outbounds,
		Route: &RouteConfig{
			Rules: rules,
			Final: finalTag,
		},
	}
}
```

- [ ] **Step 5: Rewrite `GenerateAndReload` to use `buildSingBoxConfig` + env**

Replace body of `GenerateAndReload()` in `service/vpn.go`:
```go
func GenerateAndReload() error {
	var users []database.User
	database.DB.Where("status = ?", "active").Find(&users)

	var inbounds []database.InboundConfig
	database.DB.Where("enabled = ?", true).Order("sort_order").Find(&inbounds)

	extraOutbound, err := loadExtraOutbound()
	if err != nil {
		log.Println("Warning: extra outbound not loaded:", err)
	}
	finalTag := os.Getenv("ROUTE_FINAL")

	cfg := buildSingBoxConfig(inbounds, users, extraOutbound, finalTag)

	file, _ := json.MarshalIndent(cfg, "", "  ")
	if err := os.WriteFile(ConfigPath, file, 0644); err != nil {
		log.Println("Error writing config file:", err)
		fmt.Println(string(file))
		return nil
	}
	return ReloadService()
}
```

- [ ] **Step 6: Run all tests**

Run: `go build ./... && go test ./service/ -v`
Expected: build OK, все тесты PASS.

- [ ] **Step 7: Commit**

```bash
git add service/vpn.go service/vpn_test.go database/database.go
git commit -m "feat(routing): env-driven extra outbound + per-inbound route rules"
```

---

### Task 1.5: Seed 2 direct-exit inbound'ов (placeholder Reality keys)

**Files:**
- Modify: `database/database.go:263-267` (после существующих builtins).

- [ ] **Step 1: Add seed logic (без Reality keys — placeholder)**

Внутри блока `if inboundCount == 0` в функции `Init()`, после существующих `builtins := []InboundConfig{...}`, добавить (не в builtins, а рядом):

Modify `database/database.go` — заменить закрывающий цикл на следующий, добавив seed для не-builtin direct-exit инбаундов:

```go
			for _, ib := range builtins {
				DB.Create(&ib)
			}
			// Direct-exit inbounds (RuVDS выход) — не builtin, placeholder Reality keys.
			directExits := []InboundConfig{
				{
					Tag:               "vless-direct-xhttp",
					DisplayName:       "VLESS Direct-Exit (xhttp)",
					Protocol:          "vless",
					ListenPort:        2059,
					TLSType:           "reality",
					SNI:               "yastatic.net",
					Transport:         "xhttp",
					UserType:          "new",
					Flow:              "",
					Multiplex:         false,
					Enabled:           false, // отключены до заполнения ключей
					IsBuiltin:         false,
					SortOrder:         10,
					ExitOutbound:      "direct",
					RealityPrivateKey: "REPLACE_ME_VIA_API",
					RealityPublicKey:  "REPLACE_ME_VIA_API",
					RealityShortIDs:   JSONStringArray{"REPLACE_ME"},
					Fingerprint:       "random",
				},
				{
					Tag:               "vless-direct-tcp",
					DisplayName:       "VLESS Direct-Exit (tcp)",
					Protocol:          "vless",
					ListenPort:        2060,
					TLSType:           "reality",
					SNI:               "yastatic.net",
					Transport:         "",
					UserType:          "legacy",
					Flow:              "xtls-rprx-vision",
					Multiplex:         false,
					Enabled:           false, // отключены до заполнения ключей
					IsBuiltin:         false,
					SortOrder:         11,
					ExitOutbound:      "direct",
					RealityPrivateKey: "REPLACE_ME_VIA_API",
					RealityPublicKey:  "REPLACE_ME_VIA_API",
					RealityShortIDs:   JSONStringArray{"REPLACE_ME"},
					Fingerprint:       "random",
				},
			}
			for _, ib := range directExits {
				DB.Create(&ib)
			}
```

- [ ] **Step 2: Verify build**

Run: `go build ./...`
Expected: build OK.

- [ ] **Step 3: Commit**

```bash
git add database/database.go
git commit -m "feat(seed): add 2 direct-exit inbounds (placeholder keys, disabled)"
```

---

### Task 1.6: Sanity test — full generation for a direct-exit inbound produces expected JSON

- [ ] **Step 1: Write end-to-end config generation test**

Append to `service/vpn_test.go`:
```go
func TestBuildSingBoxConfig_DirectExit_EndToEnd(t *testing.T) {
	users := []database.User{
		{Username: "alice", UUID: "550e8400-e29b-41d4-a716-446655440000"},
	}
	ib := database.InboundConfig{
		Tag:               "vless-direct-xhttp",
		Protocol:          "vless",
		ListenPort:        2059,
		TLSType:           "reality",
		SNI:               "yastatic.net",
		Transport:         "xhttp",
		UserType:          "new",
		Enabled:           true,
		ExitOutbound:      "direct",
		RealityPrivateKey: "priv",
		RealityPublicKey:  "pub",
		RealityShortIDs:   database.JSONStringArray{"abcd1234"},
	}
	cfg := buildSingBoxConfig([]database.InboundConfig{ib}, users, nil, "")

	b, _ := json.Marshal(cfg)
	got := string(b)
	// Проверяем, что в конфиге есть SNI, tag, listen_port и per-inbound rule.
	for _, expected := range []string{
		`"tag":"vless-direct-xhttp"`,
		`"listen_port":2059`,
		`"server_name":"yastatic.net"`,
		`"private_key":"priv"`,
		`"inbound":["vless-direct-xhttp"]`,
		`"outbound":"direct"`,
	} {
		if !strings.Contains(got, expected) {
			t.Fatalf("missing %q in generated config: %s", expected, got)
		}
	}
}
```

- [ ] **Step 2: Run test**

Run: `go test ./service/ -run TestBuildSingBoxConfig_DirectExit_EndToEnd -v`
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add service/vpn_test.go
git commit -m "test(service): end-to-end config generation for direct-exit inbound"
```

---

### Task 1.7: Deploy — SSH-ом залить бинарь и настроить RuVDS ENV

Инфра-шаг: код готов, деплой.

- [ ] **Step 1: Build binary**

Run:
```bash
cd /home/markriaz/vpn-backend-tg-bot && GOOS=linux GOARCH=amd64 go build -o /tmp/vpnbot-new .
```
Expected: файл `/tmp/vpnbot-new` создан (~30 MB).

- [ ] **Step 2: Extract wg-out block from RuVDS current config**

Run:
```bash
ssh -i ~/.ssh/ruvds root@194.87.80.237 "python3 -c 'import json; c=json.load(open(\"/etc/sing-box/config.json\")); print(json.dumps([o for o in c[\"outbounds\"] if o[\"tag\"]==\"wg-out\"][0]))'" > /tmp/wg-out.json
cat /tmp/wg-out.json
```
Expected: одна строка JSON с wg-outbound (type=wireguard, tag=wg-out и полями server/server_port/local_address/private_key/peer_public_key).

- [ ] **Step 3: Deploy extra-outbound.json and ENV to RuVDS**

Run:
```bash
ssh -i ~/.ssh/ruvds root@194.87.80.237 "mkdir -p /etc/vpnbot"
scp -i ~/.ssh/ruvds /tmp/wg-out.json root@194.87.80.237:/etc/vpnbot/extra-outbound.json
ssh -i ~/.ssh/ruvds root@194.87.80.237 "grep -q EXTRA_OUTBOUND_JSON_PATH /opt/VpnBot/.env || echo -e 'EXTRA_OUTBOUND_JSON_PATH=/etc/vpnbot/extra-outbound.json\nROUTE_FINAL=wg-out' >> /opt/VpnBot/.env && grep -E '^(EXTRA_OUTBOUND_JSON_PATH|ROUTE_FINAL)=' /opt/VpnBot/.env"
```
Expected: обе ENV в файле `/opt/VpnBot/.env`.

- [ ] **Step 4: Deploy binary to Hetzner FIRST (safer — нет wg-out для восстановления)**

Run:
```bash
scp -i ~/.ssh/hertzner-ubuntu /tmp/vpnbot-new root@49.13.201.110:/opt/VpnBot/app_bin.new
ssh -i ~/.ssh/hertzner-ubuntu root@49.13.201.110 "cp /opt/VpnBot/app_bin /opt/VpnBot/app_bin.bak && mv /opt/VpnBot/app_bin.new /opt/VpnBot/app_bin && systemctl restart vpnbot && sleep 3 && systemctl is-active vpnbot"
```
Expected: `active`.

- [ ] **Step 5: Trigger reload on Hetzner + verify existing inbounds intact**

Run:
```bash
ssh -i ~/.ssh/hertzner-ubuntu root@49.13.201.110 "curl -sS -X POST -H 'Authorization: Bearer $(curl -sS -X POST http://127.0.0.1:8085/api/login -H \"Content-Type: application/json\" -d \"{\\\"password\\\":\\\"$(grep ADMIN_PASSWORD /opt/VpnBot/.env | cut -d= -f2)\\\"}\" | jq -r .token)' http://127.0.0.1:8085/api/reload"
ssh -i ~/.ssh/hertzner-ubuntu root@49.13.201.110 "python3 -c 'import json; c=json.load(open(\"/etc/sing-box/config.json\")); print(\"inbounds=\", len(c[\"inbounds\"])); print(\"outbounds=\", [o.get(\"tag\") for o in c[\"outbounds\"]]); print(\"final=\", c[\"route\"][\"final\"])'"
```
Expected: `inbounds=10`, `outbounds=[direct, block]` (без wg-out — на Hetzner ENV пусты), `final=direct`. Новые direct-exit инбаунды disabled, не эмитятся.

- [ ] **Step 6: Deploy binary to RuVDS**

Run:
```bash
scp -i ~/.ssh/ruvds /tmp/vpnbot-new root@194.87.80.237:/opt/VpnBot/app_bin.new
ssh -i ~/.ssh/ruvds root@194.87.80.237 "cp /opt/VpnBot/app_bin /opt/VpnBot/app_bin.bak && mv /opt/VpnBot/app_bin.new /opt/VpnBot/app_bin && systemctl restart vpnbot && sleep 3 && systemctl is-active vpnbot"
```
Expected: `active`.

- [ ] **Step 7: Reload sing-box on RuVDS + verify wg-out survived**

Run:
```bash
ssh -i ~/.ssh/ruvds root@194.87.80.237 "curl -sS -X POST -H 'Authorization: Bearer $(curl -sS -X POST http://127.0.0.1:8085/api/login -H \"Content-Type: application/json\" -d \"{\\\"password\\\":\\\"$(grep ADMIN_PASSWORD /opt/VpnBot/.env | cut -d= -f2)\\\"}\" | jq -r .token)' http://127.0.0.1:8085/api/reload"
ssh -i ~/.ssh/ruvds root@194.87.80.237 "python3 -c 'import json; c=json.load(open(\"/etc/sing-box/config.json\")); print(\"inbounds=\", len(c[\"inbounds\"])); print(\"outbounds=\", [o.get(\"tag\") for o in c[\"outbounds\"]]); print(\"final=\", c[\"route\"][\"final\"]); print(\"bogon_rules=\", sum(1 for r in c[\"route\"][\"rules\"] if r.get(\"outbound\")==\"block\"))'"
```
Expected: `inbounds=10`, `outbounds=[wg-out, direct, block]`, `final=wg-out`, `bogon_rules=1`.

- [ ] **Step 8: Smoke test — существующий пользователь по-прежнему выходит через Hetzner**

**Manual verification (пользователь):** подключиться к любому существующему инбаунду (например, VLESS Reality TCP :8444) → `curl ipinfo.io/json` в клиенте → **IP должен быть 49.13.201.110** (Hetzner). Если RuVDS IP (194.87.80.237) — wg-туннель разорван, откат: Step 9.

- [ ] **Step 9 (only on failure): Rollback**

```bash
ssh -i ~/.ssh/ruvds root@194.87.80.237 "mv /opt/VpnBot/app_bin.bak /opt/VpnBot/app_bin && systemctl restart vpnbot && curl -sS -X POST http://127.0.0.1:8085/api/reload"
```

---

### Task 1.8: Generate Reality keys для новых inbounds + активировать

- [ ] **Step 1: Generate keypair pair и short_id локально**

Run:
```bash
ssh -i ~/.ssh/hertzner-ubuntu root@49.13.201.110 "sing-box generate reality-keypair" > /tmp/reality-1.txt
ssh -i ~/.ssh/hertzner-ubuntu root@49.13.201.110 "sing-box generate reality-keypair" > /tmp/reality-2.txt
cat /tmp/reality-1.txt /tmp/reality-2.txt
openssl rand -hex 8 > /tmp/short-1.txt
openssl rand -hex 8 > /tmp/short-2.txt
```
Expected: два блока с `PrivateKey:` и `PublicKey:`, два short_id.

- [ ] **Step 2: Update seed inbounds via API — Hetzner + RuVDS**

Найти ID новых inbound'ов и залить настоящие ключи. Псевдо (заменить {token}, {id_xhttp}, {id_tcp} и ключи из шага 1):

Run (для Hetzner и повторить для RuVDS):
```bash
TOKEN=$(curl -sS -X POST http://49.13.201.110:8085/api/login -H 'Content-Type: application/json' -d "{\"password\":\"$(ssh -i ~/.ssh/hertzner-ubuntu root@49.13.201.110 'grep ADMIN_PASSWORD /opt/VpnBot/.env | cut -d= -f2')\"}" | jq -r .token)
# получить ID двух direct-exit инбаундов
curl -sS -H "Authorization: Bearer $TOKEN" http://49.13.201.110:8085/api/inbounds | jq '.[] | select(.tag | startswith("vless-direct")) | {id, tag}'
# для каждого — PUT с настоящими ключами и enabled=true
curl -sS -X PUT -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  http://49.13.201.110:8085/api/inbounds/{id_xhttp} \
  -d '{"reality_private_key":"<priv1>","reality_public_key":"<pub1>","reality_short_ids":["<short1>"],"enabled":true}'
# аналогично для id_tcp с priv2/pub2/short2
# reload
curl -sS -X POST -H "Authorization: Bearer $TOKEN" http://49.13.201.110:8085/api/reload
```
Expected: `200 OK`, конфиг перегенерирован.

- [ ] **Step 3: UFW open ports 2059/tcp, 2060/tcp на обоих серверах**

Run:
```bash
ssh -i ~/.ssh/hertzner-ubuntu root@49.13.201.110 "ufw allow 2059/tcp comment 'direct-exit-xhttp'; ufw allow 2060/tcp comment 'direct-exit-tcp'"
ssh -i ~/.ssh/ruvds root@194.87.80.237 "ufw allow 2059/tcp comment 'direct-exit-xhttp'; ufw allow 2060/tcp comment 'direct-exit-tcp'"
```
Expected: `Rules updated`.

- [ ] **Step 4: Manual verify — direct-exit из RuVDS**

**Manual (пользователь):** подписаться на новый inbound (subscription-ссылка перегенерируется автоматически), импортировать в клиент, подключиться к `vless-direct-xhttp` на порту 2059 сервера **194.87.80.237** → `curl ipinfo.io/json` → **должен быть 194.87.80.237** (RuVDS, российский IP).

- [ ] **Step 5: Commit deploy log (без секретов)**

```bash
git add -A
git commit --allow-empty -m "deploy: etap 1 — routing rework + direct-exit inbounds live on prod

- Hetzner: bogon rules applied, existing 10 inbounds intact
- RuVDS: wg-out preserved via EXTRA_OUTBOUND_JSON_PATH, 2 direct-exit inbounds active (2059, 2060)
- UFW updated on both servers
- Reality keys generated and applied via API"
```

---

# Этап 2 — Padding + uTLS chrome для новых xhttp инбаундов

### Task 2.1: Test — MultiplexConfig умеет padding + max_streams

- [ ] **Step 1: Write failing test**

Append to `service/vpn_test.go`:
```go
func TestMultiplexConfig_PaddingAndMaxStreams(t *testing.T) {
	m := MultiplexConfig{Enabled: true, Padding: true, MaxStreams: 8}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	if !strings.Contains(got, `"padding":true`) {
		t.Fatalf("expected padding, got %s", got)
	}
	if !strings.Contains(got, `"max_streams":8`) {
		t.Fatalf("expected max_streams=8, got %s", got)
	}
}

func TestMultiplexConfig_OmitsWhenZero(t *testing.T) {
	m := MultiplexConfig{Enabled: true}
	b, _ := json.Marshal(m)
	got := string(b)
	if strings.Contains(got, "padding") {
		t.Fatalf("expected no padding for zero value, got %s", got)
	}
	if strings.Contains(got, "max_streams") {
		t.Fatalf("expected no max_streams for zero value, got %s", got)
	}
}
```

- [ ] **Step 2: Run to fail**

Run: `go test ./service/ -run TestMultiplexConfig -v`
Expected: FAIL — `unknown field 'Padding'`.

- [ ] **Step 3: Extend `MultiplexConfig`**

Modify `service/vpn.go:86-88`:
```go
type MultiplexConfig struct {
	Enabled    bool `json:"enabled"`
	Padding    bool `json:"padding,omitempty"`
	MaxStreams int  `json:"max_streams,omitempty"`
}
```

- [ ] **Step 4: Run to pass**

Run: `go test ./service/ -run TestMultiplexConfig -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add service/vpn.go service/vpn_test.go
git commit -m "feat(service): add Padding and MaxStreams to MultiplexConfig"
```

---

### Task 2.2: `InboundConfig` получает MuxPadding + MuxMaxStreams

- [ ] **Step 1: Write test that inbound with padding emits in mux**

Append to `service/vpn_test.go`:
```go
func TestBuildSingboxInbound_MuxPadding(t *testing.T) {
	ib := database.InboundConfig{
		Tag:           "vless-direct-xhttp",
		Protocol:      "vless",
		ListenPort:    2059,
		TLSType:       "reality",
		SNI:           "yastatic.net",
		Transport:     "xhttp",
		UserType:      "new",
		Multiplex:     true,
		MuxPadding:    true,
		MuxMaxStreams: 8,
	}
	sb := buildSingboxInbound(ib, nil)
	if sb.Multiplex == nil {
		t.Fatal("expected non-nil Multiplex")
	}
	if !sb.Multiplex.Padding {
		t.Fatal("expected padding=true")
	}
	if sb.Multiplex.MaxStreams != 8 {
		t.Fatalf("expected max_streams=8, got %d", sb.Multiplex.MaxStreams)
	}
}
```

- [ ] **Step 2: Run to fail**

Run: `go test ./service/ -run TestBuildSingboxInbound_MuxPadding -v`
Expected: FAIL — поля `MuxPadding`/`MuxMaxStreams` не существуют в `InboundConfig`.

- [ ] **Step 3: Add fields to `InboundConfig`**

Modify `database/database.go`, добавить после `Multiplex bool`:
```go
	MuxPadding    bool `json:"mux_padding"`
	MuxMaxStreams int  `gorm:"default:0" json:"mux_max_streams"`
```

- [ ] **Step 4: Update `buildSingboxInbound` to emit padding**

Modify `service/vpn.go:229-232`:
```go
	// Multiplex
	if ib.Multiplex {
		sb.Multiplex = &MultiplexConfig{
			Enabled:    true,
			Padding:    ib.MuxPadding,
			MaxStreams: ib.MuxMaxStreams,
		}
	}
```

- [ ] **Step 5: Run to pass**

Run: `go build ./... && go test ./service/ -v`
Expected: build OK, все тесты PASS.

- [ ] **Step 6: Commit**

```bash
git add service/vpn.go database/database.go service/vpn_test.go
git commit -m "feat(inbound): emit multiplex padding + max_streams from InboundConfig"
```

---

### Task 2.3: Обновить direct-exit seed'ы + fingerprint=chrome + деплой

- [ ] **Step 1: Update seed defaults in `database/database.go`**

Modify seed `vless-direct-xhttp` в `database/database.go`:
```go
					Multiplex:         true,
					MuxPadding:        true,
					MuxMaxStreams:     8,
					Fingerprint:       "chrome",
```

`vless-direct-tcp` (не мультиплексируется, но fingerprint меняем):
```go
					Fingerprint:       "chrome",
```

**Note:** Эти изменения затронут только НОВЫЕ БД (при первом seed). Существующие direct-exit inbound'ы из этапа 1 нужно обновить через API — см. Step 3.

- [ ] **Step 2: Build and deploy binary (same steps as Task 1.7 Step 1, 4, 6)**

Run:
```bash
cd /home/markriaz/vpn-backend-tg-bot && GOOS=linux GOARCH=amd64 go build -o /tmp/vpnbot-new .
scp -i ~/.ssh/hertzner-ubuntu /tmp/vpnbot-new root@49.13.201.110:/opt/VpnBot/app_bin.new
ssh -i ~/.ssh/hertzner-ubuntu root@49.13.201.110 "cp /opt/VpnBot/app_bin /opt/VpnBot/app_bin.bak2 && mv /opt/VpnBot/app_bin.new /opt/VpnBot/app_bin && systemctl restart vpnbot"
scp -i ~/.ssh/ruvds /tmp/vpnbot-new root@194.87.80.237:/opt/VpnBot/app_bin.new
ssh -i ~/.ssh/ruvds root@194.87.80.237 "cp /opt/VpnBot/app_bin /opt/VpnBot/app_bin.bak2 && mv /opt/VpnBot/app_bin.new /opt/VpnBot/app_bin && systemctl restart vpnbot"
```

- [ ] **Step 3: Update existing direct-exit rows via API**

Для каждого сервера (Hetzner и RuVDS) обновить существующие `vless-direct-xhttp` и `vless-direct-tcp` записи:

```bash
# Hetzner
TOKEN=$(curl -sS -X POST http://49.13.201.110:8085/api/login -H 'Content-Type: application/json' -d '{"password":"..."}' | jq -r .token)
ID_XHTTP=$(curl -sS -H "Authorization: Bearer $TOKEN" http://49.13.201.110:8085/api/inbounds | jq -r '.[] | select(.tag=="vless-direct-xhttp") | .id')
curl -sS -X PUT -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  http://49.13.201.110:8085/api/inbounds/$ID_XHTTP \
  -d '{"multiplex":true,"mux_padding":true,"mux_max_streams":8,"fingerprint":"chrome"}'

ID_TCP=$(curl -sS -H "Authorization: Bearer $TOKEN" http://49.13.201.110:8085/api/inbounds | jq -r '.[] | select(.tag=="vless-direct-tcp") | .id')
curl -sS -X PUT -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  http://49.13.201.110:8085/api/inbounds/$ID_TCP \
  -d '{"fingerprint":"chrome"}'
curl -sS -X POST -H "Authorization: Bearer $TOKEN" http://49.13.201.110:8085/api/reload
# Повторить для RuVDS (194.87.80.237)
```

- [ ] **Step 4: Verify config has padding**

Run:
```bash
ssh -i ~/.ssh/ruvds root@194.87.80.237 "python3 -c 'import json; c=json.load(open(\"/etc/sing-box/config.json\")); [print(i[\"tag\"], i.get(\"multiplex\")) for i in c[\"inbounds\"] if i[\"tag\"].startswith(\"vless-direct\")]'"
```
Expected:
```
vless-direct-xhttp {'enabled': True, 'padding': True, 'max_streams': 8}
vless-direct-tcp None
```

- [ ] **Step 5: Manual verify — Reels/YouTube не залипают**

**Manual (пользователь):** подключиться через `vless-direct-xhttp` → пробовать Reels/YouTube shorts несколько минут → должно грузиться без 5-сек залипаний.

- [ ] **Step 6: Commit deploy log**

```bash
git commit --allow-empty -m "deploy: etap 2 — padding + uTLS chrome on direct-exit inbounds"
```

---

# Этап 3 — zapret/nfqws на RuVDS

Server-level этап. Артефакты в репо, применение вручную по README.

### Task 3.1: Создать deploy-артефакты в репо

**Files:**
- Create: `deploy/ruvds/zapret/README.md`
- Create: `deploy/ruvds/zapret/nfqws.service`
- Create: `deploy/ruvds/zapret/nftables-nfqws.rules`

- [ ] **Step 1: Create `deploy/ruvds/zapret/nfqws.service`**

```ini
[Unit]
Description=zapret nfqws (DPI desync, обход ТСПУ для direct-exit трафика)
After=network.target

[Service]
Type=simple
ExecStart=/opt/zapret/nfq/nfqws --qnum=100 \
  --dpi-desync=split \
  --dpi-desync-split-pos=2 \
  --dpi-desync-ttl=5 \
  --dpi-desync-fooling=badseq
Restart=on-failure
RestartSec=3
CapabilityBoundingSet=CAP_NET_ADMIN CAP_NET_RAW
AmbientCapabilities=CAP_NET_ADMIN CAP_NET_RAW

[Install]
WantedBy=multi-user.target
```

- [ ] **Step 2: Create `deploy/ruvds/zapret/nftables-nfqws.rules`**

```nft
# Маркирует TCP-egress через eth0 (публичный интерфейс RuVDS) в NFQUEUE 100.
# Не трогает wg0 (wg-туннель к Hetzner изолирован).
table inet zapret {
    chain output {
        type filter hook output priority mangle; policy accept;
        oifname "eth0" tcp dport { 80, 443, 8080, 8443 } queue num 100 bypass
    }
}
```

- [ ] **Step 3: Create `deploy/ruvds/zapret/README.md`**

```markdown
# zapret/nfqws deploy на RuVDS

## Установка

```bash
# 1. Клонировать и собрать zapret
apt install -y build-essential gcc libnetfilter-queue-dev
git clone https://github.com/bol-van/zapret /opt/zapret
cd /opt/zapret && make -C nfq

# 2. Установить systemd unit
cp /root/vpn-backend-tg-bot/deploy/ruvds/zapret/nfqws.service /etc/systemd/system/
systemctl daemon-reload
systemctl enable --now nfqws.service
systemctl status nfqws

# 3. Применить nftables правила
cp /root/vpn-backend-tg-bot/deploy/ruvds/zapret/nftables-nfqws.rules /etc/nftables-nfqws.rules
nft -f /etc/nftables-nfqws.rules
nft list ruleset | grep -A 4 'table inet zapret'
```

## Верификация

```bash
# Пакеты идут в NFQUEUE
nft list ruleset | grep -A 4 'table inet zapret'
# systemd активен
systemctl is-active nfqws
# Логи без ошибок
journalctl -u nfqws -n 100 --no-pager
```

## Откат

```bash
systemctl stop nfqws
systemctl disable nfqws
nft delete table inet zapret
```

Восстановление сети — 5-10 секунд.

## Тюнинг

Если частые false-positive: увеличить `--dpi-desync-split-pos` до 4-8 или сменить `--dpi-desync=split` на `--dpi-desync=fake,split`.
```

- [ ] **Step 4: Commit deploy artifacts**

```bash
git add deploy/ruvds/zapret/
git commit -m "chore(deploy): add zapret/nfqws artifacts for RuVDS"
```

---

### Task 3.2: Deploy zapret на RuVDS (по README)

- [ ] **Step 1: Copy repo to RuVDS**

Run:
```bash
ssh -i ~/.ssh/ruvds root@194.87.80.237 "test -d /root/vpn-backend-tg-bot || git clone https://github.com/markriaz13/vpn-backend-tg-bot /root/vpn-backend-tg-bot; cd /root/vpn-backend-tg-bot && git pull"
```
Expected: репо на месте.

- [ ] **Step 2: Install build deps + build nfqws**

Run:
```bash
ssh -i ~/.ssh/ruvds root@194.87.80.237 "apt install -y build-essential gcc libnetfilter-queue-dev && test -d /opt/zapret || git clone https://github.com/bol-van/zapret /opt/zapret && cd /opt/zapret && make -C nfq && ls -l /opt/zapret/nfq/nfqws"
```
Expected: файл `/opt/zapret/nfq/nfqws` собран.

- [ ] **Step 3: Install systemd unit + start**

Run:
```bash
ssh -i ~/.ssh/ruvds root@194.87.80.237 "cp /root/vpn-backend-tg-bot/deploy/ruvds/zapret/nfqws.service /etc/systemd/system/ && systemctl daemon-reload && systemctl enable --now nfqws && sleep 2 && systemctl is-active nfqws"
```
Expected: `active`.

- [ ] **Step 4: Apply nftables rules**

Run:
```bash
ssh -i ~/.ssh/ruvds root@194.87.80.237 "cp /root/vpn-backend-tg-bot/deploy/ruvds/zapret/nftables-nfqws.rules /etc/nftables-nfqws.rules && nft -f /etc/nftables-nfqws.rules && nft list ruleset | grep -A 4 'table inet zapret'"
```
Expected: правила применены, счётчики видны.

- [ ] **Step 5: Verify traffic flows through NFQUEUE**

Run:
```bash
ssh -i ~/.ssh/ruvds root@194.87.80.237 "curl -sS --max-time 5 https://ipinfo.io/json"
sleep 3
ssh -i ~/.ssh/ruvds root@194.87.80.237 "nft list ruleset | grep -A 2 'oifname \"eth0\"'"
```
Expected: счётчик пакетов на nftables-правиле > 0.

- [ ] **Step 6: Verify wg-туннель НЕ затронут**

**Manual (пользователь):** подключиться к существующему инбаунду (Hetzner-exit) → `curl ipinfo.io/json` → **всё ещё 49.13.201.110**. Если отвалилось — nftables правило зацепило wg0 некорректно, откат: `ssh ...ruvds "systemctl stop nfqws && nft delete table inet zapret"`.

- [ ] **Step 7: Manual verify — Reels/YouTube через direct-exit не залипают**

**Manual (пользователь):** подключиться через `vless-direct-xhttp` (RuVDS-exit) → 5-10 минут Reels/YouTube. Если стало хуже (не работает вообще) — тюнить split-pos (см. README), или откат.

- [ ] **Step 8: Commit deploy log**

```bash
git commit --allow-empty -m "deploy: etap 3 — zapret/nfqws active on RuVDS eth0"
```

---

# Этап 4 — ShadowTLS v3

Самый большой этап: новый протокол, новые DB-поля, breaking изменение сигнатуры `buildSingboxInbound()`.

### Task 4.1: DB migration — новые поля ShadowTLS

- [ ] **Step 1: Add fields to `InboundConfig`**

Modify `database/database.go`, добавить в конец struct'а `InboundConfig`:
```go
	// ShadowTLS fields (Protocol="shadowtls")
	ShadowTLSPassword string `json:"shadowtls_password"`
	ShadowTLSVersion  int    `gorm:"default:0" json:"shadowtls_version"`
	CoverDomain       string `json:"cover_domain"`
	InnerMethod       string `json:"inner_method"`
	InnerPassword     string `json:"inner_password"`
```

- [ ] **Step 2: Verify build**

Run: `go build ./...`
Expected: OK.

- [ ] **Step 3: Commit**

```bash
git add database/database.go
git commit -m "feat(db): add ShadowTLS fields to InboundConfig"
```

---

### Task 4.2: Test — ShadowTLS inbound emits paired shadowtls+shadowsocks

- [ ] **Step 1: Write failing test**

Append to `service/vpn_test.go`:
```go
func TestBuildInboundGroup_ShadowTLS(t *testing.T) {
	ib := database.InboundConfig{
		Tag:               "vless-direct-shadowtls-v3",
		Protocol:          "shadowtls",
		ListenPort:        8446,
		ShadowTLSVersion:  3,
		ShadowTLSPassword: "outer-password",
		CoverDomain:       "gosuslugi.ru",
		InnerMethod:       "2022-blake3-aes-128-gcm",
		InnerPassword:     "inner-password",
		ExitOutbound:      "direct",
	}
	users := []database.User{
		{Username: "alice", UUID: "550e8400-e29b-41d4-a716-446655440000"},
	}
	group := buildInboundGroup(ib, users)
	if len(group) != 2 {
		t.Fatalf("expected 2 inbounds (shadowtls + shadowsocks), got %d", len(group))
	}
	// Первый — shadowtls
	b0, _ := json.Marshal(group[0])
	if !strings.Contains(string(b0), `"type":"shadowtls"`) {
		t.Fatalf("expected first inbound shadowtls, got %s", b0)
	}
	if !strings.Contains(string(b0), `"listen_port":8446`) {
		t.Fatalf("expected port 8446 on shadowtls, got %s", b0)
	}
	if !strings.Contains(string(b0), `"server":"gosuslugi.ru"`) {
		t.Fatalf("expected handshake server=gosuslugi.ru, got %s", b0)
	}
	if !strings.Contains(string(b0), `"detour":"ss-inner-vless-direct-shadowtls-v3"`) {
		t.Fatalf("expected detour, got %s", b0)
	}
	// Второй — shadowsocks
	b1, _ := json.Marshal(group[1])
	if !strings.Contains(string(b1), `"type":"shadowsocks"`) {
		t.Fatalf("expected second inbound shadowsocks, got %s", b1)
	}
	if !strings.Contains(string(b1), `"method":"2022-blake3-aes-128-gcm"`) {
		t.Fatalf("expected method, got %s", b1)
	}
}
```

- [ ] **Step 2: Run to fail**

Run: `go test ./service/ -run TestBuildInboundGroup_ShadowTLS -v`
Expected: FAIL — `buildInboundGroup` не существует.

- [ ] **Step 3: Introduce `buildInboundGroup` that returns `[]any`**

Add to `service/vpn.go` (перед `buildSingboxInbound`):

```go
// buildInboundGroup возвращает 1+ sing-box inbound-объектов для одной DB-записи.
// Для vless/hysteria2 — 1 элемент (типизированный SingboxInbound).
// Для shadowtls — 2 элемента (shadowtls + inner shadowsocks).
func buildInboundGroup(ib database.InboundConfig, users []database.User) []any {
	if ib.Protocol == "shadowtls" {
		return buildShadowTLSGroup(ib, users)
	}
	return []any{buildSingboxInbound(ib, users)}
}

// buildShadowTLSGroup строит пару shadowtls+shadowsocks для одного inbound.
func buildShadowTLSGroup(ib database.InboundConfig, users []database.User) []any {
	innerTag := "ss-inner-" + ib.Tag

	// shadowtls-users берут ShadowTLSPassword (общий пароль внешней инкапсуляции).
	stlsUsers := []map[string]any{
		{"name": "default", "password": ib.ShadowTLSPassword},
	}
	shadowtls := map[string]any{
		"type":        "shadowtls",
		"tag":         ib.Tag,
		"listen":      "::",
		"listen_port": ib.ListenPort,
		"version":     ib.ShadowTLSVersion,
		"users":       stlsUsers,
		"handshake": map[string]any{
			"server":      ib.CoverDomain,
			"server_port": 443,
		},
		"detour": innerTag,
	}

	// shadowsocks-users берут UUID как pre-shared key inner-пользователя.
	// Note: для 2022-blake3 методов нужен base64-encoded key соответствующей длины (16 байт для aes-128).
	// Ожидается, что оператор проинжектит InnerPassword (base64 32B на инбаунд).
	// Per-user passwords пока не реализуем — все пользователи используют один InnerPassword.
	innerUsers := []map[string]any{}
	for _, u := range users {
		innerUsers = append(innerUsers, map[string]any{
			"name":     u.Username,
			"password": ib.InnerPassword, // общий, per-inbound
		})
	}
	shadowsocks := map[string]any{
		"type":        "shadowsocks",
		"tag":         innerTag,
		"listen":      "127.0.0.1",
		"listen_port": 0, // sing-box присвоит; detour ходит по tag
		"method":      ib.InnerMethod,
		"password":    ib.InnerPassword,
		"users":       innerUsers,
	}

	return []any{shadowtls, shadowsocks}
}
```

- [ ] **Step 4: Change `SingBoxConfig.Inbounds` type to `[]any`**

Modify `service/vpn.go:32`:
```go
	Inbounds     []any               `json:"inbounds"`
```

- [ ] **Step 5: Update `buildSingBoxConfig` to use `buildInboundGroup`**

Modify `service/vpn.go`, в `buildSingBoxConfig`:
```go
	singboxInbounds := []any{}
	inboundTags := []string{}
	perInboundRules := []RouteRule{}
	for _, ib := range inbounds {
		group := buildInboundGroup(ib, users)
		singboxInbounds = append(singboxInbounds, group...)
		inboundTags = append(inboundTags, ib.Tag)
		if ib.ExitOutbound != "" {
			perInboundRules = append(perInboundRules, RouteRule{
				Inbound:  []string{ib.Tag},
				Outbound: ib.ExitOutbound,
			})
		}
	}
	// ...
	// В финальный cfg.Inbounds = singboxInbounds
```

- [ ] **Step 6: Run all tests + build**

Run: `go build ./... && go test ./service/ -v`
Expected: build OK, все тесты PASS (в том числе новый TestBuildInboundGroup_ShadowTLS).

- [ ] **Step 7: Commit**

```bash
git add service/vpn.go service/vpn_test.go
git commit -m "feat(service): buildInboundGroup returns []any, supports paired shadowtls+ss"
```

---

### Task 4.3: `GenerateLinkForInbound` умеет shadowtls

- [ ] **Step 1: Test — shadowtls link is base64 JSON with correct structure**

Append to `service/vpn_test.go`:
```go
import (
	"encoding/base64"
)

func TestGenerateLinkForInbound_ShadowTLS(t *testing.T) {
	ib := database.InboundConfig{
		Tag:               "vless-direct-shadowtls-v3",
		Protocol:          "shadowtls",
		ListenPort:        8446,
		ShadowTLSVersion:  3,
		ShadowTLSPassword: "outer-pass",
		CoverDomain:       "gosuslugi.ru",
		InnerMethod:       "2022-blake3-aes-128-gcm",
		InnerPassword:     "inner-pass",
	}
	user := database.User{Username: "alice", UUID: "550e8400-e29b-41d4-a716-446655440000"}
	link := GenerateLinkForInbound(ib, user, "194.87.80.237")
	if !strings.HasPrefix(link, "sing-box://") {
		t.Fatalf("expected sing-box://, got %q", link)
	}
	payload := strings.TrimPrefix(link, "sing-box://")
	data, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		t.Fatalf("payload not base64: %v", err)
	}
	got := string(data)
	if !strings.Contains(got, `"server":"194.87.80.237"`) {
		t.Fatalf("expected server IP, got %s", got)
	}
	if !strings.Contains(got, `"password":"outer-pass"`) {
		t.Fatalf("expected outer password, got %s", got)
	}
	if !strings.Contains(got, `"server_name":"gosuslugi.ru"`) {
		t.Fatalf("expected cover domain in SNI, got %s", got)
	}
	if !strings.Contains(got, `"fingerprint":"chrome"`) {
		t.Fatalf("expected chrome uTLS, got %s", got)
	}
}
```

- [ ] **Step 2: Run to fail**

Run: `go test ./service/ -run TestGenerateLinkForInbound_ShadowTLS -v`
Expected: FAIL — генератор не обрабатывает `Protocol="shadowtls"`.

- [ ] **Step 3: Add shadowtls branch to `GenerateLinkForInbound`**

Modify `service/vpn.go`, найти `GenerateLinkForInbound` (около строки 310). В начало функции добавить branch:
```go
func GenerateLinkForInbound(ib database.InboundConfig, user database.User, serverAddr string) string {
	if ib.ServerAddress != "" {
		serverAddr = ib.ServerAddress
	}

	if ib.Protocol == "shadowtls" {
		return generateShadowTLSLink(ib, user, serverAddr)
	}
	// ... existing logic
```

Add helper `generateShadowTLSLink`:
```go
func generateShadowTLSLink(ib database.InboundConfig, user database.User, serverAddr string) string {
	payload := map[string]any{
		"type":        "shadowtls",
		"tag":         ib.Tag + "-out",
		"server":      serverAddr,
		"server_port": ib.ListenPort,
		"version":     ib.ShadowTLSVersion,
		"password":    ib.ShadowTLSPassword,
		"tls": map[string]any{
			"enabled":     true,
			"server_name": ib.CoverDomain,
			"utls": map[string]any{
				"enabled":     true,
				"fingerprint": "chrome",
			},
		},
		"detour": "ss-inner-out",
	}
	inner := map[string]any{
		"type":     "shadowsocks",
		"tag":      "ss-inner-out",
		"method":   ib.InnerMethod,
		"password": ib.InnerPassword,
	}
	bundle := map[string]any{
		"outbounds": []any{payload, inner},
	}
	b, _ := json.Marshal(bundle)
	return "sing-box://" + base64.StdEncoding.EncodeToString(b)
}
```

Add import `"encoding/base64"` в top of file если ещё нет.

- [ ] **Step 4: Run to pass**

Run: `go test ./service/ -run TestGenerateLinkForInbound_ShadowTLS -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add service/vpn.go service/vpn_test.go
git commit -m "feat(link): shadowtls subscription generator (base64 sing-box JSON bundle)"
```

---

### Task 4.4: Seed 1 ShadowTLS inbound

- [ ] **Step 1: Add seed for ShadowTLS**

Modify `database/database.go`, добавить после блока `directExits` (внутри `if inboundCount == 0`):

```go
			shadowtlsSeed := InboundConfig{
				Tag:               "vless-direct-shadowtls-v3",
				DisplayName:       "ShadowTLS v3 → Direct (RuVDS)",
				Protocol:          "shadowtls",
				ListenPort:        8446,
				Enabled:           false, // отключён до задания секретов
				IsBuiltin:         false,
				SortOrder:         12,
				ExitOutbound:      "direct",
				ShadowTLSVersion:  3,
				ShadowTLSPassword: "REPLACE_ME_VIA_API",
				CoverDomain:       "gosuslugi.ru",
				InnerMethod:       "2022-blake3-aes-128-gcm",
				InnerPassword:     "REPLACE_ME_BASE64_16B",
			}
			DB.Create(&shadowtlsSeed)
```

- [ ] **Step 2: Build**

Run: `go build ./...`
Expected: OK.

- [ ] **Step 3: Commit**

```bash
git add database/database.go
git commit -m "feat(seed): add 1 ShadowTLS v3 direct-exit inbound (disabled)"
```

---

### Task 4.5: Deploy ShadowTLS + generate secrets + activate

- [ ] **Step 1: Build & deploy binary (одинаково для обоих серверов)**

Run:
```bash
cd /home/markriaz/vpn-backend-tg-bot && GOOS=linux GOARCH=amd64 go build -o /tmp/vpnbot-new .
scp -i ~/.ssh/hertzner-ubuntu /tmp/vpnbot-new root@49.13.201.110:/opt/VpnBot/app_bin.new
ssh -i ~/.ssh/hertzner-ubuntu root@49.13.201.110 "cp /opt/VpnBot/app_bin /opt/VpnBot/app_bin.bak3 && mv /opt/VpnBot/app_bin.new /opt/VpnBot/app_bin && systemctl restart vpnbot"
scp -i ~/.ssh/ruvds /tmp/vpnbot-new root@194.87.80.237:/opt/VpnBot/app_bin.new
ssh -i ~/.ssh/ruvds root@194.87.80.237 "cp /opt/VpnBot/app_bin /opt/VpnBot/app_bin.bak3 && mv /opt/VpnBot/app_bin.new /opt/VpnBot/app_bin && systemctl restart vpnbot"
```

- [ ] **Step 2: Generate secrets locally**

Run:
```bash
STLS_PASS=$(openssl rand -base64 32)
INNER_PASS=$(openssl rand -base64 16)
echo "STLS_PASS=$STLS_PASS"
echo "INNER_PASS=$INNER_PASS"
```

- [ ] **Step 3: Update ShadowTLS inbound on RuVDS via API + enable**

```bash
TOKEN=$(curl -sS -X POST http://194.87.80.237:8085/api/login -H 'Content-Type: application/json' -d "{\"password\":\"...\"}" | jq -r .token)
ID=$(curl -sS -H "Authorization: Bearer $TOKEN" http://194.87.80.237:8085/api/inbounds | jq -r '.[] | select(.tag=="vless-direct-shadowtls-v3") | .id')
curl -sS -X PUT -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  "http://194.87.80.237:8085/api/inbounds/$ID" \
  -d "{\"shadowtls_password\":\"$STLS_PASS\",\"inner_password\":\"$INNER_PASS\",\"enabled\":true}"
curl -sS -X POST -H "Authorization: Bearer $TOKEN" http://194.87.80.237:8085/api/reload
```

Повторить для Hetzner (49.13.201.110) с теми же секретами (важно — для консистентности подписки).

- [ ] **Step 4: UFW open 8446/tcp**

Run:
```bash
ssh -i ~/.ssh/hertzner-ubuntu root@49.13.201.110 "ufw allow 8446/tcp comment 'shadowtls-v3'"
ssh -i ~/.ssh/ruvds root@194.87.80.237 "ufw allow 8446/tcp comment 'shadowtls-v3'"
```

- [ ] **Step 5: Verify sing-box config emitted paired inbounds**

Run:
```bash
ssh -i ~/.ssh/ruvds root@194.87.80.237 "python3 -c 'import json; c=json.load(open(\"/etc/sing-box/config.json\")); [print(i.get(\"type\"), i.get(\"tag\"), i.get(\"listen_port\")) for i in c[\"inbounds\"] if \"shadowtls\" in i.get(\"tag\",\"\") or \"ss-inner\" in i.get(\"tag\",\"\")]'"
```
Expected:
```
shadowtls vless-direct-shadowtls-v3 8446
shadowsocks ss-inner-vless-direct-shadowtls-v3 0
```

Плюс `journalctl -u sing-box -n 50 --no-pager` — sing-box успешно запустился без ошибок парсинга.

- [ ] **Step 6: Manual verify — sing-box native client**

**Manual (пользователь):**
1. Скачать sing-box native клиент (Android/iOS/Desktop).
2. Импортировать subscription-ссылку — должен появиться outbound `vless-direct-shadowtls-v3-out`.
3. Подключиться → `curl ipinfo.io/json` → **194.87.80.237** (RuVDS).
4. Telegram → соединение устанавливается < 5 сек (базовая цель этапа).

- [ ] **Step 7: Commit deploy log**

```bash
git commit --allow-empty -m "deploy: etap 4 — ShadowTLS v3 direct-exit inbound active"
```

---

# Финализация

### Task F.1: Обновить CLAUDE.md под фактическую топологию

- [ ] **Step 1: Rewrite network topology section in CLAUDE.md**

Modify `CLAUDE.md`, найти секцию "Network topology" (grep для `Client → RuVDS`). Заменить на:

```markdown
Network topology (2026-07-11 update): `Client → RuVDS sing-box (VLESS Reality, 10 inbounds) → WireGuard wg0 (10.8.0.0/24, UDP 51820) → Hetzner sing-box → Internet`.

**Direct-exit inbounds** (added 2026-07-11): 2 VLESS + 1 ShadowTLS v3 на RuVDS c ExitOutbound="direct" — выход в интернет напрямую с российского IP 194.87.80.237.

**Env-driven wg-out preservation:** `EXTRA_OUTBOUND_JSON_PATH=/etc/vpnbot/extra-outbound.json` + `ROUTE_FINAL=wg-out` на RuVDS сохраняют wg-outbound при автогенерации. На Hetzner эти ENV пусты.

**DPI hardening on RuVDS:** zapret/nfqws + nftables NFQUEUE 100 фильтрует TCP-egress через eth0 (не трогает wg0).

**Note:** iptables DNAT/MASQUERADE (упоминались в предыдущей версии CLAUDE.md) **не используются** — RuVDS работает как sing-box front-end, не как порт-форвардер.
```

- [ ] **Step 2: Commit**

```bash
git add CLAUDE.md
git commit -m "docs: update CLAUDE.md with actual topology + hardening notes"
```

---

## Verification (после всех этапов)

- [ ] Все `go test ./...` зелёные.
- [ ] `go vet ./...` без предупреждений.
- [ ] `systemctl is-active sing-box vpnbot nfqws` — все `active` на RuVDS; `sing-box vpnbot` — на Hetzner.
- [ ] Существующие 10 инбаундов работают через wg-туннель (curl показывает Hetzner IP).
- [ ] 2 direct-exit VLESS работают через RuVDS-eth0 (curl показывает RuVDS IP).
- [ ] 1 ShadowTLS v3 работает через sing-box native клиент (curl показывает RuVDS IP).
- [ ] Bogon rules присутствуют в `/etc/sing-box/config.json` на обоих серверах.
- [ ] `nft list ruleset` на RuVDS показывает счётчики > 0 на zapret-правиле.
- [ ] `journalctl -u sing-box --since '5 min ago'` — без ERROR/FATAL.
- [ ] Telegram/Reels работают без залипаний через direct-exit inbounds.
