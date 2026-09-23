# Учёт трафика: «всего» и «за месяц» по пользователям и подключениям

Дата: 2026-09-23. Статус: дизайн одобрен в чате, спецификация на ревью.

## Зачем

Видеть, сколько трафика каждый пользователь потратил за всё время и за
месяц, и сколько трафика несёт каждое подключение (DE-TCP, DE-XHTTP, RU…)
по дням. Цель — анализ доступа к интернету: какие протоколы реально
работают, что просело после очередного ужесточения DPI или блокировки
(пример — блок IP Hetzner 23.09.2026).

## Что есть сейчас (и что не так)

- `users.traffic_used` — единственный счётчик, накопительный, без
  разбивки по периодам и без upload/download.
- `UpdateTrafficViaAPI()` (`service/vpn.go`) раз в 10 с читает V2Ray Stats
  gRPC **только локального Hetzner sing-box**, `Reset_: false`, дельту
  считает от `previousStats` в памяти процесса. После рестарта vpnbot
  (27 раз с 01.09) первый опрос прибавляет весь накопленный счётчик
  sing-box заново → вероятный двойной счёт, текущие ~4.6 ТБ завышены.
- RuVDS sing-box статистику не публикует (`GenerateRuVDSConfig` без
  блока `experimental`) → трафик RU, RU-TCP, RU-STLS, RU-MASK не учитывается.
- `checkLimits()` при исчерпании лимита перегенерирует конфиги, но
  пользователь продолжает работать на RuVDS до следующего его reload.
- `connection_logs` никем не пишется.

## Решения

- Лимит остаётся **за всё время** (`users.traffic_used` против
  `traffic_limit`), меняется только точность учёта и отображение.
- Сутки и месяц считаются по **Europe/Moscow** (`time.LoadLocation` +
  `import _ "time/tzdata"`, чтобы не зависеть от tzdata на сервере).
- История начинается с момента деплоя; старое `traffic_used` сохраняется
  как есть (восстановить задним числом нельзя).

## Данные

```go
// Трафик пользователя за сутки на одном сервере.
type TrafficDaily struct {
    ID       uint
    UserID   uint   `gorm:"uniqueIndex:idx_td_user_day_srv"`
    Day      string `gorm:"uniqueIndex:idx_td_user_day_srv;size:10"` // "2026-09-23", MSK
    Server   string `gorm:"uniqueIndex:idx_td_user_day_srv;size:16"` // "hetzner" | "ruvds"
    Upload   int64
    Download int64
}

// Трафик подключения (inbound tag) за сутки на одном сервере.
type InboundTrafficDaily struct {
    ID       uint
    Tag      string `gorm:"uniqueIndex:idx_itd_tag_day_srv;size:64"`
    Day      string `gorm:"uniqueIndex:idx_itd_tag_day_srv;size:10"`
    Server   string `gorm:"uniqueIndex:idx_itd_tag_day_srv;size:16"`
    Upload   int64
    Download int64
}
```

Запись — upsert с инкрементом (`ON CONFLICT(...) DO UPDATE SET upload =
upload + excluded.upload, ...`) в той же транзакции, что и инкремент
`users.traffic_used`. Объём: ~34 пользователя × 2 сервера × 365 ≈ 25k
строк в год, инбаундов ещё меньше.

## Сбор

Новый файл `service/traffic.go`:

- `parseStats([]*command.Stat) (users, inbounds map[string]Delta)` —
  чистая функция: разбирает `user>>>NAME>>>traffic>>>uplink|downlink` и
  `inbound>>>TAG>>>traffic>>>uplink|downlink`, суммирует в `Delta{Up, Down}`.
- `recordTraffic(server, day string, users, inbounds map[string]Delta)` —
  одна транзакция: `traffic_daily`, `inbound_traffic_daily`,
  `users.traffic_used += up+down`; после неё `checkLimits` по затронутым.
- `pollStats(server string, dial func(ctx, addr) (net.Conn, error))` —
  `QueryStats(Pattern: "", Reset_: true)`, затем parse → record.
  - **Reset_: true** — счётчики обнуляются при чтении, каждый опрос
    приносит ровно трафик с прошлого опроса; `previousStats` удаляется.
  - **Первое чтение после старта процесса (на каждый сервер) выбрасывается.**
    Иначе при первом деплое повторно прибавился бы весь уже учтённый
    накопленный счётчик. Цена — до одного интервала опроса трафика на
    каждый рестарт vpnbot.
  - Reload sing-box сбрасывает счётчики — теряется трафик с последнего
    опроса (до одного интервала). Принимается.
- Hetzner: dial по TCP на `127.0.0.1:10000`, каждые 10 с (как сейчас).
- RuVDS: dial через `sshConnect()` → `client.Dial("tcp", "127.0.0.1:10000")`
  в `grpc.WithContextDialer`, каждые 60 с; только если `IsRuVDSEnabled()`.
  Ошибка SSH логируется, не роняет Hetzner-опрос.
- `GenerateRuVDSConfig` получает тот же блок `experimental.v2ray_api`
  (listen `127.0.0.1:10000`, свободен — проверено), stats по active users
  и RuVDS-инбаундам (кроме mask/xdns — их нет в sing-box).
- Двойного счёта между серверами нет: DE-трафик через релей
  терминируется на Hetzner, RU и маска — на RuVDS sing-box.
- `UpdateTrafficViaAPI()` остаётся точкой входа для бота (`getStatusMsg`
  опрашивает Hetzner перед ответом), внутри — `pollStats("hetzner", ...)`.

## Лимиты

`checkLimits` без изменения смысла (лимит за всё время), но при переводе
в `expired` вызывается `GenerateAndReload()`, который уже зеркалит конфиг
на RuVDS при `IsRuVDSEnabled()` — проверить тестом/чтением, что expired
пользователь исчезает и из RuVDS-конфига.

## Отображение

- **Бот, пользователь** (`getStatusMsg`): `За месяц: X · Всего: Y · Лимит: Z`.
  «Всего» = `users.traffic_used`; «за месяц» = SUM(`traffic_daily`) по
  `Day LIKE '2026-09-%'`.
- **Бот, админ:**
  - `/traffic` — топ-20 за текущий месяц: имя, месяц, всего;
  - `/traffic <username>` — последние 6 месяцев по месяцам + всего;
  - `/traffic inbounds` — трафик по подключениям за 7 дней (сумма по дню).
- **API** (защищённые, JSON):
  - `GET /api/traffic/users?month=YYYY-MM` →
    `[{user_id, username, month_upload, month_download, total}]`;
  - `GET /api/traffic/users/:id` → `{total, months: [{month, upload, download}]}`;
  - `GET /api/traffic/inbounds?from=YYYY-MM-DD&to=YYYY-MM-DD` →
    `[{day, tag, server, upload, download}]`.
- **Заголовок подписки** (`Subscription-Userinfo`, оба хендлера):
  `upload = SUM(traffic_daily.upload)` за всё время,
  `download = traffic_used − upload`, `total = traffic_limit`. Сумма
  upload+download по-прежнему равна `traffic_used` — клиенты сравнивают
  её с лимитом. Месячную цифру стандартный заголовок не несёт — только бот/API.

## Тесты

- `parseStats`: пользователи и инбаунды, uplink/downlink, мусорные имена.
- `recordTraffic` на SQLite in-memory: upsert-инкремент двух опросов в
  один день, смена суток, `traffic_used` растёт на up+down.
- Агрегации: месяц/всего для пользователя, история по месяцам, инбаунды за
  период.
- «Первое чтение выбрасывается»: pollStats с фейковым источником.
- `GenerateRuVDSConfig`/builder содержит `experimental.v2ray_api` с users.
- Заголовок подписки: upload+download == traffic_used.

После деплоя: сверить приращение `traffic_daily` за несколько минут с
сырыми счётчиками sing-box на обоих серверах; `sing-box check` на RuVDS.

## Вне рамок

- Помесячный лимит, автосброс.
- Фронтенд админ-панели (исходников нет в репо) — только API.
- Учёт Xray-маски/XDNS отдельно (маска приходит в RuVDS sing-box как
  inner-инбаунд и считается там; XDNS выключен).
