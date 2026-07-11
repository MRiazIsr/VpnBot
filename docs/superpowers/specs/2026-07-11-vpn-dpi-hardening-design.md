# VPN DPI Hardening (2026) — Design

## Context

Топология VPN сейчас: `клиент → RuVDS sing-box (VLESS Reality, 10 инбаундов) → WireGuard → Hetzner (интернет-выход)`. На фоне обновлений ТСПУ статья на Habr (id 1049748, снят с публикации автором, восстановлен из web.archive.org) описывает поведенческий DPI-анализ по размеру пакетов, таймингам и энтропии. Классические симптомы: залипание Reels/YouTube после ~16 КБ, бесконечное `Connecting` в Telegram, JA4 fingerprint детект у Google AI.

Дополнительное требование пользователя: категория инбаундов с прямым выходом в интернет с публичного IP RuVDS (194.87.80.237) — не туннелировать в Hetzner. Нужно для сервисов, доступных только с российских IP.

Апгрейд сочетает две задачи: (1) применение DPI-обходных техник из статьи, (2) добавление direct-exit категории через расширение маршрутизации.

## Общая архитектура

**Инвариант.** 10 существующих инбаундов на RuVDS/Hetzner не меняются. Все новые сущности живут параллельно, на новых портах, с новыми тегами. Откат любого этапа = disable инбаунд в админке / `systemctl stop` / `git revert`.

**Порядок этапов** (rollout по возрастанию риска):

| # | Этап | Правки | Downtime |
|---|------|--------|----------|
| 1 | Routing rework + 2 direct-exit inbounds + bogon deploy | Go код, env-переменные, seed | 0 (reload) |
| 2 | Padding + uTLS для новых xhttp-инбаундов | Go код, обновление seed из этапа 1 | 0 |
| 3 | zapret/nfqws на RuVDS | Только RuVDS: apt, systemd, nftables | 1-2 мин |
| 4 | ShadowTLS v3 | Go код (крупные правки), новый seed | 0 |

Между этапами пользователь вручную подтверждает работоспособность. Каждый этап — отдельный commit/PR.

**Что НЕ делаем (out of scope):**
- Google AI / Gemini через residential IP — Hetzner ASN забанен, честного решения нет пока exit там.
- Клиентские приложения / GoodbyeDPI wrapper — user-space на устройстве.
- Обновление `CLAUDE.md` под фактическую топологию — отдельным коммитом в конце.

---

## Этап 1 — Routing rework + 2 direct-exit inbounds

### Проблема разрыва кода и продакшн-конфига RuVDS

Факт: `service/vpn.go:269-294` пишет `outbounds = [direct, block]` и `route.final = "direct"`. На RuVDS фактически: `outbounds = [wg-out, direct, block]` и `route.final = "wg-out"`. Конфиг вручную дорабатывался после генерации. Запуск текущего `GenerateAndReload()` на RuVDS сотрёт wg-out → у 34 живых пользователей ляжет туннель.

### Решение — env-driven расширение

Новые ENV для vpnbot:
- `EXTRA_OUTBOUND_JSON_PATH` — путь к JSON с одним outbound-объектом. Если задан и файл существует — объект инжектится в начало `outbounds` при генерации.
- `ROUTE_FINAL` — тег для `route.final`. Если задан — заменяет дефолтное `"direct"`.

На RuVDS: `EXTRA_OUTBOUND_JSON_PATH=/etc/vpnbot/extra-outbound.json`, `ROUTE_FINAL=wg-out`. Файл содержит текущий wg-out.
На Hetzner: оба ENV пусты → поведение как сейчас.

### Изменения в коде

**`service/vpn.go`**
- `RouteRule` расширить: добавить `Inbound []string \`json:"inbound,omitempty"\``.
- `GenerateAndReload()`:
  - Прочитать `EXTRA_OUTBOUND_JSON_PATH` (если задан) → `json.Unmarshal` в `map[string]any` → prepend к `outbounds`.
  - Заменить `Route.Final` на `os.Getenv("ROUTE_FINAL")` если непусто.
  - Для каждого enabled inbound с `ExitOutbound != ""` эмитить `RouteRule{Inbound: []string{ib.Tag}, Outbound: ib.ExitOutbound}` до bogon-правила. Порядок правил: сначала per-inbound, потом bogon.

**`database/database.go`**
- `InboundConfig` добавить поле:
  ```go
  ExitOutbound string `json:"exit_outbound"` // "" (=route.final) | "direct" | "wg-out" | ...
  ```
- GORM auto-migration подхватит поле (SQLite `ADD COLUMN`).
- Seed 2 новых inbounds (`IsBuiltin=false`, `Enabled=true`):
  - `vless-direct-xhttp`: Protocol=`vless`, TLSType=`reality`, Transport=`xhttp`, ListenPort=**2059** (свободен), SNI=`yastatic.net`, UserType=`new`, ExitOutbound=`direct`, Fingerprint=`random` (chrome добавится в этапе 2).
  - `vless-direct-tcp`: Protocol=`vless`, TLSType=`reality`, Transport=`""` (tcp), ListenPort=**2060**, SNI=`yastatic.net`, UserType=`legacy` (flow=xtls-rprx-vision), ExitOutbound=`direct`, Fingerprint=`random`.
  - Reality-ключи для новых inbounds: перед деплоем сгенерировать пару командой `sing-box generate reality-keypair` (сделать это локально или SSH-ом на любой сервер), приватный ключ вставить в seed как `RealityPrivateKey`, публичный — в `RealityPublicKey`. Short-ID сгенерировать `openssl rand -hex 8`, положить в `RealityShortIDs`. Хранить ключи именно в Go-исходнике seed'а нежелательно; альтернатива — seed вставляет строку-плейсхолдер, а deploy-скрипт `INSERT`-ит настоящие через API до `POST /api/reload`. **Выбор: seed с placeholder + документированный шаг ручной подстановки перед первым reload.**

**UFW на RuVDS:** открыть 2059/tcp, 2060/tcp. UFW на Hetzner — тоже (там эти инбаунды тоже поднимутся, но с exit_outbound=direct → выход из Hetzner-IP, что нормально).

### Верификация этапа 1

1. `go build && go vet ./...` — pass.
2. Деплой на Hetzner (сначала). `curl POST /api/reload`. Дифф конфига до/после: только добавились 2 новых inbound и bogon-правило. `outbounds` и `final` не изменились.
3. Деплой на RuVDS. Убедиться, что `EXTRA_OUTBOUND_JSON_PATH` и `ROUTE_FINAL` заданы в `/opt/VpnBot/.env`. `curl POST /api/reload`. Дифф конфига: wg-out сохранился, добавились 2 инбаунда, bogon-правило, per-inbound rules для vless-direct-*.
4. Подключиться существующим ключом → `curl ipinfo.io/json` → должен показать **Hetzner IP** (49.13.201.110) — старые инбаунды работают через wg.
5. Подключиться новым direct-exit ключом → `curl ipinfo.io/json` → **RuVDS IP** (194.87.80.237).
6. `journalctl -u sing-box -n 50` — без ошибок.

---

## Этап 2 — Padding + uTLS для новых xhttp-инбаундов

### Изменения в коде

**`service/vpn.go`**
- `MultiplexConfig` расширить:
  ```go
  type MultiplexConfig struct {
      Enabled    bool `json:"enabled"`
      Padding    bool `json:"padding,omitempty"`
      MaxStreams int  `json:"max_streams,omitempty"`
  }
  ```
- `buildSingboxInbound()`: если `ib.MuxPadding` — заполнить `Padding` и `MaxStreams` в `MultiplexConfig`.

**`database/database.go`**
- `InboundConfig` добавить:
  ```go
  MuxPadding    bool `json:"mux_padding"`
  MuxMaxStreams int  `json:"mux_max_streams"`
  ```
- Обновить seed из этапа 1:
  - `vless-direct-xhttp`: `Multiplex=true, MuxPadding=true, MuxMaxStreams=8, Fingerprint="chrome"`.
  - `vless-direct-tcp`: `Fingerprint="chrome"` (padding не работает для чистого tcp без mux — оставить как есть).
- Для существующих пользователей seed-миграция не запускается повторно; изменения применяются либо новым коммитом миграции, либо через админ-панель.

### Верификация этапа 2

1. `go build && go vet` — pass.
2. Redeploy → `POST /api/reload`.
3. `python3 -c 'import json; c=json.load(open("/etc/sing-box/config.json")); [print(i["tag"], i.get("multiplex")) for i in c["inbounds"] if "direct" in i["tag"]]'` → показывает `{enabled: true, padding: true, max_streams: 8}` для xhttp inbound.
4. Пользователь перегенерирует subscription-ссылку → в ссылке `fp=chrome`.
5. Импорт в клиент → Reels/YouTube не залипают на 5-й секунде через direct-exit-xhttp.

---

## Этап 3 — zapret/nfqws на RuVDS

### Инсталляция (вне репо, но артефакты хранятся в репо)

Каталог `deploy/ruvds/zapret/` содержит:
- `nfqws.service` — systemd unit.
- `nftables-nfqws.rules` — правила маркировки трафика.
- `README.md` — инструкции ручного разворачивания через SSH.

### Что устанавливается на RuVDS

- `apt install nftables` (уже есть в Ubuntu 20.04).
- Собрать zapret из исходников: `git clone https://github.com/bol-van/zapret /opt/zapret && cd /opt/zapret && make -C nfq` → бинарь `/opt/zapret/nfq/nfqws`.
- systemd unit:
  ```ini
  [Unit]
  Description=zapret nfqws (DPI desync)
  After=network.target
  [Service]
  ExecStart=/opt/zapret/nfq/nfqws --qnum=100 \
    --dpi-desync=split --dpi-desync-split-pos=2 \
    --dpi-desync-ttl=5 --dpi-desync-fooling=badseq
  Restart=on-failure
  [Install]
  WantedBy=multi-user.target
  ```
- nftables правило: маркировать только TCP egress через **eth0** в NFQUEUE 100, **не трогать wg0**:
  ```nft
  table inet zapret {
    chain output {
      type filter hook output priority mangle;
      oifname "eth0" tcp dport { 80, 443 } queue num 100 bypass
    }
  }
  ```
- `systemctl enable --now nfqws.service && nft -f /etc/nftables-nfqws.rules`.

### Верификация этапа 3

1. `systemctl status nfqws` — active.
2. `nft list ruleset` — правило есть, счётчик пакетов растёт при трафике.
3. `journalctl -u nfqws -n 100` — без критичных ошибок.
4. С клиента через direct-exit inbound → воспроизвести кейс из статьи: играть Reels в Instagram / YouTube shorts → сравнить время залипания до/после.
5. **Роллбэк-тест:** `systemctl stop nfqws && nft delete table inet zapret` — сеть работает как раньше за < 10 сек.

### Риски и mitigation

- False-positive на легитимный HTTPS: тюнить `--dpi-desync-split-pos` и `--dpi-desync-ttl`, начать с рекомендуемых значений из статьи.
- Влияние на wg-туннель: правила фильтруют только `oifname "eth0"` — wg-out идёт через wg0, изолирован.
- Влияние на API vpnbot (Caddy на Hetzner:8443): RuVDS не хостит API, риска нет.

---

## Этап 4 — ShadowTLS v3

### Мотивация

Reality в 2026 детектится ТСПУ по таймингам. ShadowTLS v3 одалживает живую TLS-сессию у «белого» домена (gosuslugi.ru) для маскировки, что для DPI выглядит как легитимный трафик из белого списка.

### Изменения в коде

**`database/database.go`, `InboundConfig`:**
```go
ShadowTLSPassword string // пароль для внешней инкапсуляции
ShadowTLSVersion  int    // = 3
CoverDomain       string // "gosuslugi.ru"
InnerMethod       string // "2022-blake3-aes-128-gcm"
InnerPassword     string // pre-shared key для inner shadowsocks
```
- Валидация: если `Protocol == "shadowtls"` — эти поля обязательны.

**`service/vpn.go`:**
- Новый struct для ShadowTLS inbound:
  ```go
  type ShadowTLSInbound struct {
      Type       string   `json:"type"`     // "shadowtls"
      Tag        string   `json:"tag"`
      Listen     string   `json:"listen"`
      ListenPort int      `json:"listen_port"`
      Version    int      `json:"version"`
      Users      []ShadowTLSUser `json:"users"`
      Handshake  ServerEP `json:"handshake"`
      Detour     string   `json:"detour"`
  }
  type ShadowTLSUser struct { Name, Password string }
  ```
- Один DB-запись `Protocol="shadowtls"` эмитит **два** объекта в sing-box:
  1. `shadowtls` inbound на публичном порту, `detour: "ss-inner-<tag>"`.
  2. `shadowsocks` inbound на loopback (`127.0.0.1:0`), tag `ss-inner-<tag>`, method + password из `InnerMethod`/`InnerPassword`, users = все active пользователи с UUID как password.
- `buildSingboxInbound()` — новая ветка `case "shadowtls":` возвращает **список** из 2 объектов. Возможно, сигнатуру функции придётся расширить до `[]SingboxInbound`.

**`GenerateLinkForInbound()`:**
- Для `Protocol="shadowtls"` формировать sing-box outbound JSON, base64-энкодить как строку подписки. Формат:
  ```json
  {
    "type": "shadowtls",
    "server": "<address>", "server_port": <port>,
    "version": 3, "password": "<ShadowTLSPassword>",
    "tls": { "enabled": true, "server_name": "<CoverDomain>", "utls": {"enabled": true, "fingerprint": "chrome"} },
    "detour": "ss-out"
  }
  ```
  + прикреплённый outbound `ss-out` c method/password. Клиент импортирует полный JSON.

**Seed 1 shadowtls inbound на RuVDS:**
- `vless-direct-shadowtls-v3`, ListenPort **8446**, CoverDomain=`gosuslugi.ru`, ExitOutbound=`direct`, ShadowTLSVersion=3, InnerMethod=`2022-blake3-aes-128-gcm`, ShadowTLSPassword и InnerPassword сгенерировать (base64 32B).

**UFW:** открыть 8446/tcp на RuVDS и Hetzner.

### Совместимость клиентов

Поддерживают ShadowTLS v3:
- ✅ sing-box native (Windows/macOS/Linux/Android)
- ✅ v2rayN, Nekoray
- ⚠️ iOS Streisand / Shadowrocket — требует проверки в момент разворачивания; если не поддержат — оставить как опциональный резерв только для sing-box пользователей.

### Верификация этапа 4

1. `go build && go vet` — pass.
2. Redeploy → `POST /api/reload`. `python3 parse_singbox.py` показывает shadowtls + вложенный shadowsocks inbound.
3. Импортировать сгенерированную ссылку в sing-box клиент → подключиться → `curl ipinfo.io/json` → RuVDS IP.
4. Открыть Telegram через клиент → соединение устанавливается за < 5 сек (базовая цель).
5. Проверить, что старые инбаунды и WG-туннель работают без изменений.

---

## Верификация всего апгрейда

**Финальный чек-лист после всех этапов:**
- Существующие 10 инбаундов работают идентично (`ipinfo.io` показывает Hetzner IP через wg-путь).
- 2 direct-exit VLESS inbound'а работают, показывают RuVDS IP.
- 1 ShadowTLS v3 inbound работает, показывает RuVDS IP.
- `sing-box`, `vpnbot`, `nfqws` — все `active (running)`.
- Bogon-правила в route → блокировка private CIDR подтверждена в конфиге.
- API `/api/network/check-all` возвращает healthy.
- Обновить `CLAUDE.md` под фактическую топологию (WG вместо iptables DNAT) отдельным финальным коммитом.
