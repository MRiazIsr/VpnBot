# Frontend Prompt: Network Management (Firewall + Port Forwarding + Connectivity)

## Контекст

Это admin-панель для VPN-системы (Next.js 16 + React 19 + Tailwind CSS 4 + TypeScript). Backend — Go REST API на порту 8085. Авторизация — JWT Bearer token.

Сетевая топология: `Клиент → RuVDS (iptables DNAT) → Hetzner (sing-box VPN)`.
- **Hetzner Cloud Firewall** — контролирует входящий трафик на Hetzner сервер (управляется через API)
- **RuVDS iptables** — DNAT/MASQUERADE проброс портов с RuVDS на Hetzner (управляется через SSH)

## Задача

Создать страницу/раздел **"Сеть"** (`/network`) в admin-панели со следующими секциями:

### 1. Network Status (верхняя панель)

GET `/api/network/status` — возвращает:
```json
{
  "firewall": {
    "configured": true,
    "firewall_id": 123,
    "firewall_name": "my-fw",
    "server_id": 456,
    "server_name": "vpn-hetzner",
    "hetzner_ip": "49.13.201.110",
    "rules": [...]
  },
  "port_forward": {
    "configured": true,
    "ruvds_ip": "1.2.3.4",
    "hetzner_ip": "49.13.201.110",
    "rules": [...]
  }
}
```

Показать:
- Статус подключения: зелёный бейдж если `configured: true`, серый если `false`
- IP адреса серверов
- Имена сервера и фаервола Hetzner

Если `configured: false` — показать инструкцию какие переменные окружения нужно задать (`HETZNER_API_TOKEN`, `RUVDS_IP`).

### 2. Hetzner Firewall Rules (таблица)

**GET** `/api/network/firewall/rules` → `{ "rules": [...] }`

Каждое правило:
```json
{
  "direction": "in",
  "protocol": "tcp",
  "port": "8444",
  "source_ips": ["0.0.0.0/0", "::/0"],
  "description": "VPN port 8444"
}
```

Показать таблицу правил: Direction | Protocol | Port | Source IPs | Description | Actions

**Добавить правило:**
POST `/api/network/firewall/rules`
```json
{ "port": 8444, "protocol": "tcp", "description": "VPN port 8444" }
```

**Удалить правило:**
DELETE `/api/network/firewall/rules`
```json
{ "port": 8444, "protocol": "tcp" }
```

UI: кнопка "Открыть порт" → модалка с полями port, protocol (select: tcp/udp), description. Кнопка удаления на каждой строке таблицы с подтверждением.

### 3. Port Forwarding Rules (таблица)

**GET** `/api/network/forwards/rules` → `{ "rules": [...] }`

Каждое правило:
```json
{
  "port": 8444,
  "protocol": "tcp",
  "destination": "49.13.201.110:8444"
}
```

Показать таблицу: Port | Protocol | Destination | Actions

**Добавить проброс:**
POST `/api/network/forwards/rules`
```json
{ "port": 8444, "protocol": "tcp" }
```
Destination IP берётся из конфигурации сервера автоматически (HETZNER_SERVER_IP).

**Удалить проброс:**
DELETE `/api/network/forwards/rules`
```json
{ "port": 8444, "protocol": "tcp" }
```

UI: кнопка "Добавить проброс" → модалка с port и protocol. Кнопка удаления с подтверждением.

### 4. Connectivity Check (карточки)

**GET** `/api/network/check-all` → `{ "checks": [...] }`

Каждая проверка:
```json
{
  "port": 8444,
  "protocol": "tcp",
  "tag": "vless-in",
  "ruvds_reachable": true,
  "ruvds_latency_ms": 5,
  "ruvds_error": "",
  "hetzner_reachable": true,
  "hetzner_latency_ms": 12,
  "hetzner_error": ""
}
```

Показать карточки для каждого инбаунда:
- Название (tag) + порт/протокол
- Два индикатора:
  - **RuVDS → Hetzner** (полная цепочка): зелёный/красный кружок + латенция
  - **Hetzner напрямую** (фаервол): зелёный/красный кружок + латенция
- Если `error` — показать тултип с текстом ошибки

Кнопка "Обновить" сверху для повторной проверки. Этот запрос может занять 5-15 секунд — показать спиннер.

**Ping конкретного порта:**
POST `/api/network/ping`
```json
{ "host": "1.2.3.4", "port": 8444 }
```
→ `{ "reachable": true, "latency_ms": 5, "error": "" }`

### 5. Интеграция с формой создания инбаунда

На странице создания инбаунда (`POST /api/inbounds`) добавить два чекбокса:
- [ ] **Открыть порт в Hetzner Firewall** (`auto_open_firewall: true`)
- [ ] **Добавить проброс на RuVDS** (`auto_add_forward: true`)

Чекбоксы видны только если соответствующий сервис сконфигурирован (проверить через `/api/network/status`).

При отправке формы, если чекбоксы отмечены, JSON включает эти поля:
```json
{
  "tag": "new-vless",
  "protocol": "vless",
  "listen_port": 3000,
  "auto_open_firewall": true,
  "auto_add_forward": true,
  ...
}
```

Ответ (если были автофлаги):
```json
{
  "inbound": { ... },
  "firewall_result": { "success": true },
  "forward_result": { "success": false, "error": "SSH connection failed" }
}
```

Показать результат: зелёная галочка или красный крестик для каждой операции.

Если автофлаги НЕ были отмечены, ответ — просто объект InboundConfig (без обёртки).

## Общие требования по стеку и UI

- **Next.js 16** App Router, Server Components где возможно, Client Components для интерактива
- **Tailwind CSS 4** для стилей
- **Никаких дополнительных UI библиотек** — используй чистый Tailwind
- Тёмная тема по умолчанию (dark mode first)
- API base URL: `process.env.NEXT_PUBLIC_API_URL || 'http://localhost:8085'`
- JWT токен хранить в localStorage, отправлять как `Authorization: Bearer <token>`
- Язык UI — **русский**
- Все API-запросы через единый fetch-wrapper с обработкой ошибок и автоматическим добавлением JWT
- Показывать toast-уведомления при успехе/ошибке операций
- Responsive: мобильная версия с одной колонкой, десктоп — 2 колонки для firewall/forward таблиц

## Структура файлов

```
app/
  network/
    page.tsx                  — основная страница сети
  components/
    network/
      NetworkStatusBar.tsx    — верхняя панель статуса
      FirewallRulesTable.tsx  — таблица правил фаервола
      ForwardRulesTable.tsx   — таблица пробросов
      ConnectivityPanel.tsx   — карточки проверки связи
      OpenPortModal.tsx       — модалка открытия порта
      AddForwardModal.tsx     — модалка добавления проброса
  lib/
    api.ts                    — fetch wrapper с JWT
    types.ts                  — TypeScript типы для всех API ответов
```

## TypeScript типы

```typescript
interface FirewallRule {
  direction: string;
  protocol: string;
  port: string;
  source_ips: string[];
  destination_ips?: string[];
  description: string;
}

interface ForwardRule {
  port: number;
  protocol: string;
  destination: string;
}

interface PortCheck {
  port: number;
  protocol: string;
  tag: string;
  ruvds_reachable: boolean;
  ruvds_latency_ms: number;
  ruvds_error: string;
  hetzner_reachable: boolean;
  hetzner_latency_ms: number;
  hetzner_error: string;
}

interface FirewallInfo {
  configured: boolean;
  firewall_id?: number;
  firewall_name?: string;
  server_id?: number;
  server_name?: string;
  hetzner_ip?: string;
  rules?: FirewallRule[];
}

interface PortForwardInfo {
  configured: boolean;
  ruvds_ip?: string;
  hetzner_ip?: string;
  rules?: ForwardRule[];
}

interface NetworkStatus {
  firewall: FirewallInfo;
  port_forward: PortForwardInfo;
}

interface ActionResult {
  success: boolean;
  error?: string;
}

// Ответ создания инбаунда с авто-флагами
interface CreateInboundWithNetworkResponse {
  inbound: InboundConfig;
  firewall_result?: ActionResult;
  forward_result?: ActionResult;
}
```

## Существующие API для контекста (уже реализованы в бэкенде)

```
POST   /api/login                        → { "token": "jwt..." }
GET    /api/users                         → User[]
GET    /api/inbounds                      → InboundConfig[]
POST   /api/inbounds                      → InboundConfig | CreateInboundWithNetworkResponse
GET    /api/stats                         → { total_users, active_users, ... }
GET    /api/telemt/config                 → TelemetConfig
POST   /api/reload                        → Перезагрузка sing-box
```
