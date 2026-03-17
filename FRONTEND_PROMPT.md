# Задача: добавить страницу Telegram Proxy (telemt) в админ-панель VpnBot

В бэкенд VpnBot добавлена поддержка **telemt** — MTProto прокси для Telegram. Нужно добавить фронтенд для управления им.

## Что нужно сделать

### 1. Добавить пункт навигации

В sidebar/навигацию добавить новый пункт:
- Иконка: `Radio` из lucide-react (или `Satellite`)
- Текст: **Telegram Proxy**
- Путь: `/telemt`

### 2. Добавить TypeScript-типы

```typescript
export interface TelemetConfig {
  id: number;
  enabled: boolean;
  port: number;
  tls_domain: string;
  server_address: string;
  proxy_tag: string;
  created_at: string;
  updated_at: string;
}

export interface TelemetUser {
  id: number;
  telemet_config_id: number;
  user_id: number;
  label: string;
  secret: string;
  user: User; // вложенный объект User (id, username, telegram_username, status, ...)
  created_at: string;
  updated_at: string;
}

export interface TelemetStatus {
  status: "running" | "stopped";
  user_count: number;
}
```

### 3. Создать страницу `/telemt`

Страница состоит из 4 секций:

---

#### Секция 1: Статус сервиса

**API:** `GET /api/telemt/status`
```json
{ "status": "running", "user_count": 5 }
```

Отображение:
- Бейдж статуса: зелёный "Running" / красный "Stopped"
- Текст: "Пользователей: 5"
- Две кнопки:
  - **"Запустить"** → `POST /api/telemt/setup` → `{ "message": "telemt настроен и запущен" }`
  - **"Остановить"** → `POST /api/telemt/stop` → `{ "message": "telemt остановлен" }`
- После нажатия — перезагрузить статус

---

#### Секция 2: Настройки

**Загрузка:** `GET /api/telemt/config`
```json
{
  "id": 1,
  "enabled": true,
  "port": 443,
  "tls_domain": "dl.google.com",
  "server_address": "",
  "proxy_tag": "abc123def456abc123def456abc123de",
  "created_at": "...",
  "updated_at": "..."
}
```

Если конфига ещё нет (id=0), бэкенд вернёт дефолт: `{ port: 443, tls_domain: "dl.google.com" }`.

**Форма:**

| Поле | Тип | По умолчанию | Подсказка |
|------|-----|-------------|-----------|
| Включён | toggle/switch | false | Включить/выключить MTProto прокси |
| Порт | number | 443 | Порт, на котором слушает MTProto proxy. Если 443 занят sing-box — используйте 8443 |
| TLS Домен | text | dl.google.com | Домен для TLS-маскировки. Трафик будет выглядеть как обращение к этому сайту |
| Адрес сервера | text | (пусто) | IP или домен для генерации ссылок. Пусто = SERVER_IP из env |
| Proxy Tag | text | (пусто) | Получите у @MTProxyBot в Telegram (/newproxy). Нужен для relay-серверов Telegram. 32 hex символа |

**Сохранение:** `POST /api/telemt/config`
```json
// Request:
{
  "enabled": true,
  "port": 443,
  "tls_domain": "dl.google.com",
  "server_address": "",
  "proxy_tag": "abc123..."
}

// Response:
{
  "config": { /* TelemetConfig */ },
  "warning": ""  // или "proxy_tag не задан — middle proxy не будет работать, прокси будет работать в режиме прямого подключения"
}
```

Если `warning` не пустой — показать жёлтый alert/toast с текстом.

После успешного сохранения — если `enabled=true`, показать кнопку/предложение **"Применить настройки"** (вызывает `POST /api/telemt/setup`).

---

#### Секция 3: Пользователи

**API:** `GET /api/telemt/users`
```json
[
  {
    "id": 1,
    "telemet_config_id": 1,
    "user_id": 1,
    "label": "user_124343839",
    "secret": "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4",
    "user": {
      "id": 1,
      "username": "MRiaz",
      "telegram_username": "MRiaz",
      "status": "active"
    },
    "created_at": "...",
    "updated_at": "..."
  }
]
```

Таблица:

| Колонка | Описание |
|---------|----------|
| Пользователь | `user.telegram_username` (или `label` если нет) |
| Статус | Бейдж: green=active, red=banned, yellow=expired |
| Секрет | По умолчанию `••••••••`, кнопка "Показать" (eye icon) и "Копировать" (copy icon) |

Кнопка над таблицей: **"Синхронизировать"** → `POST /api/telemt/sync` → `{ "message": "Пользователи telemt синхронизированы" }`

Синхронизация создаёт секреты для новых активных юзеров и удаляет для заблокированных/истёкших.

---

#### Секция 4: Инструкция (collapsible/accordion)

```
Как настроить Telegram Proxy:

1. Напишите @MTProxyBot в Telegram
2. Отправьте /newproxy
3. Укажите IP и порт вашего сервера
4. Скопируйте полученный proxy tag в поле «Proxy Tag»
5. Нажмите «Сохранить», затем «Применить настройки»

Без proxy tag прокси будет работать в режиме прямого подключения
(без relay-серверов Telegram).
```

---

### 4. Добавить карточку telemt на Dashboard

На странице Dashboard (где карточки со статистикой) добавить ещё одну карточку:

**API:** `GET /api/telemt/status`

Карточка:
- Заголовок: **Telegram Proxy**
- Статус: бейдж "Running" (зелёный) / "Stopped" (красный)
- Подпись: "N пользователей"
- Клик → переход на `/telemt`

### 5. Добавить кнопку на страницу Settings

Если есть страница настроек — добавить кнопку:
- **"Перезагрузить telemt"** → `POST /api/telemt/setup`

---

## API-эндпоинты telemt (все требуют JWT)

| Метод | Path | Описание | Body/Response |
|-------|------|----------|---------------|
| GET | `/api/telemt/config` | Получить конфиг | → `TelemetConfig` |
| POST | `/api/telemt/config` | Сохранить конфиг | `{enabled, port, tls_domain, server_address, proxy_tag}` → `{config, warning}` |
| POST | `/api/telemt/setup` | Установить + запустить | → `{message}` |
| POST | `/api/telemt/stop` | Остановить | → `{message}` |
| GET | `/api/telemt/status` | Статус сервиса | → `{status: "running"/"stopped", user_count}` |
| GET | `/api/telemt/users` | Список юзеров | → `TelemetUser[]` (с вложенным User) |
| POST | `/api/telemt/sync` | Синхронизация юзеров | → `{message}` |
