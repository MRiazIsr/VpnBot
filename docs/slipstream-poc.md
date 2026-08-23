# Slipstream DNS-туннель — PoC

Статус: **PoC, не в коде проекта.** Всё развёрнуто вручную на серверах. Цель —
проверить, проходит ли TCP-over-DNS туннель (Slipstream) через российский сетевой
путь и рекурсивные резолверы, чтобы обходить whitelist-сети.

Основной сервер — Hetzner (Германия). Slipstream здесь — опциональный аварийный
транспорт «последней надежды», НЕ замена основным inbound'ам.

## Что такое Slipstream

`EndPositive/slipstream` — high-performance covert channel over DNS, под капотом
QUIC + multipath. Клиент в ограниченной сети кодирует TCP в DNS-запросы; сервер —
authoritative NS снаружи — декодирует и форвардит на целевой TCP-сервис.

- Релизы: только Linux (`x86_64`, `arm64`). **Нет клиентов под iPhone/Android/Windows/macOS** — это известное ограничение, клиентская доставка для конечных пользователей не решена.
- Версия запинена: **v0.1.1** (pre-1.0, протокол может меняться — пин обязателен).
- Канал **только UDP**. Клиент серт **не проверяет** → достаточно self-signed.
- Бинарь не умеет bind на конкретный IP, только `--dns-listen-port`.

### Режим (то, что тестируем — recursive)

Клиент шлёт DNS-запросы не на наш IP, а на разрешённый рекурсивный резолвер.
Резолвер по NS-делегированию доходит до нашего authoritative-сервера на Hetzner.
Клиент **никогда не касается IP Hetzner напрямую** — для сети это обычный DNS к
легальному резолверу. Это и есть censorship-resistant свойство.

## CLI-справка (снято с v0.1.1)

`slipstream-server`:
```
-l --dns-listen-port   DNS listen port (default: 53)
-6 --dns-listen-ipv6   IPv6 (default: false)
-a --target-address    Target TCP server (default: 127.0.0.1:5201)
-c --cert              Cert file (default: certs/cert.pem)
-k --key               Key file  (default: certs/key.pem)
-d --domain            Authoritative domain (Required)
```
`slipstream-client`:
```
-l --tcp-listen-port      Local listen port (default: 5201)
-r --resolver             Resolver address; повторяемый → QUIC multipath (Required)
-c --congestion-control   bbr | dcubic (default: dcubic)
-g --gso                  GSO (default: false)
-d --domain               Covert-channel domain (Required)
-t --keep-alive-interval  default: 400
```

## Домен и делегирование

- Регистратор: **Namecheap**, домен **`moskva.live`** (BasicDNS).
- Делегируем поддомен `e.moskva.live` нашему серверу. Glue в реестре НЕ нужен
  (NS-хост внутри той же зоны, которую обслуживает Namecheap).

Записи в Namecheap → Advanced DNS → Host Records:

| Type | Host | Value | TTL |
|------|------|-------|-----|
| A Record | `ns` | `49.13.201.110` | Automatic (~30 мин) |
| NS Record | `e` | `ns.moskva.live.` | Automatic |

Проверка (правильный результат — authoritative отдаёт делегацию; публичный
резолвер на `NS e.moskva.live` вернёт ПУСТО, т.к. наш сервер не отвечает на
обычные DNS-запросы — это норма):
```
dig +short ns.moskva.live                                 # → 49.13.201.110
dig e.moskva.live NS @dns1.registrar-servers.com +noall +authority
                                                          # → e.moskva.live. NS ns.moskva.live.
```

## Hetzner (основной сервер) — что развёрнуто

- Доступ: `ssh -i ~/.ssh/cloud-hetzner-v2 root@49.13.201.110` (Ubuntu, x86_64, eth0 = публичный IP).
- Цель туннеля: builtin inbound `vless-in` (VLESS Reality поверх TCP), `127.0.0.1:8444`
  (`database/database.go:215`).
- systemd-resolved НЕ тронут (держит только `127.0.0.53/54:53`, loopback).

Установленное:
- Бинари: `/usr/local/bin/slipstream-{server,client}`, версия в `/opt/slipstream/VERSION`.
- Self-signed cert: `/opt/slipstream/certs/{cert,key}.pem` (CN=e.moskva.live).
- systemd-юнит `/etc/systemd/system/slipstream-server.service`:
  ```
  ExecStart=/usr/local/bin/slipstream-server -l 5300 -a 127.0.0.1:8444 \
            -d e.moskva.live -c /opt/slipstream/certs/cert.pem -k /opt/slipstream/certs/key.pem
  ```
  (slipstream на :5300, не :53 — иначе конфликт с systemd-resolved.)
- iptables DNAT (UDP-only), публичный `:53` → локальный `:5300`:
  ```
  iptables -t nat -A PREROUTING -i eth0 -p udp --dport 53 -j REDIRECT --to-ports 5300
  ```
  ⚠ В памяти проекта: cold reboot RuVDS/iptables-NAT не персистентен — после
  ребута Hetzner DNAT-правило надо восстанавливать.
- ufw: `allow 53/udp`, `allow 5300/udp`.
- **Hetzner Cloud Firewall** (облачный, перед VM; id `10513002`, name
  `firewall-2053-vpn-1`, управляется Hetzner API / `service/firewall.go`):
  добавлено inbound-правило `udp/53 from 0.0.0.0/0,::/0` (description
  «slipstream DNS PoC»). **Это был реальный блокер** — Cloud FW режет пакеты
  ДО eth0, ufw до них не доходит. Правило добавлено read-modify-write (весь
  существующий набор + одно правило; `set_rules` у Hetzner = replace-all,
  поэтому только так). Всего правил стало 18.

## RuVDS — тестовый плацдарм (российский сетевой путь)

- Доступ: `ssh -i ~/.ssh/russian-vps root@194.87.80.237` (Ubuntu 20.04, x86_64).
- Хостнейм `ruvds-m96mr`. `/etc/resolv.conf` → `8.8.8.8`, `9.9.9.9`.
- **glibc-несовместимость:** официальный бинарь slipstream v0.1.1 требует
  glibc ≥ 2.34 (символы до 2.38) и OpenSSL 3; RuVDS = glibc 2.31 / OpenSSL 1.1.
  Бинарь напрямую НЕ запускается. Решение — **Docker** с образом
  `ghcr.io/endpositive/slipstream-client:v0.1.1`, контейнер на `--network host`.
- **Docker поставлен с `iptables:false`** (`/etc/docker/daemon.json` создан ДО
  установки), чтобы dockerd не трогал iptables — на RuVDS живёт боевой telemt
  Plan B DNAT-relay (11 DNAT + 11 MASQUERADE). Проверено: relay цел, ip_forward=1,
  DOCKER-цепочек в nat нет. Снапшот до установки: `/root/iptables-before-docker.rules`.
- Запуск клиента: `docker run -d --name slip --network host
  ghcr.io/endpositive/slipstream-client:v0.1.1 -r <resolver> -d e.moskva.live -l 7000`.
- Оговорка: RuVDS — хостинг-VPS в РФ, не строго whitelist-сеть доступа
  (отель/корп/мобайл). Валидная проверка российского пути и рекурсивного
  резолвера, но не финальное доказательство для самых жёстких whitelist-сетей.

## Фазы PoC и статус — PoC УСПЕШЕН (2026-05-16)

| Этап | Статус |
|------|--------|
| Делегирование `e.moskva.live` → `ns.moskva.live` → Hetzner | ✅ подтверждено |
| `slipstream-server` на Hetzner (v0.1.1, systemd, DNAT, ufw, Cloud FW) | ✅ задеплоен, active |
| Phase 1 плумбинг (slipstream ↔ sing-box, loopback на Hetzner) | ✅ **PASS** — HTTP 400 за 0.4с |
| Phase 1 по сети (RuVDS → `49.13.201.110:53` direct) | ✅ **PASS** — HTTP 400 за **78мс** |
| Phase 2a recursive через Google `8.8.8.8` | ✅ **PASS** — HTTP 400 за **101мс** |
| Phase 2b recursive через Yandex `77.88.8.8` (РФ-резолвер) | ✅ **PASS** — HTTP 400 за **99мс** |

Вывод: censorship-resistant recursive mode работает. Клиент обращается только к
рекурсивному резолверу (в т.ч. российскому Yandex), IP Hetzner напрямую не
касается, туннель доходит до sing-box за ~100мс. Задержка для DNS-туннеля
отличная (это handshake+запрос; пропускную способность под нагрузкой не мерили).

**Политика резолверов (решение 2026-05-16):** использовать ИСКЛЮЧИТЕЛЬНО
российские / РКН-разрешённые рекурсивные резолверы (Yandex `77.88.8.8`,
`77.88.8.1`; при необходимости — резолверы Ростелекома/провайдера). Google
`8.8.8.8` использовался только как контрольная проверка в PoC и в проде НЕ
применять. Клиентские приложения позволяют задать список резолверов — туда
прописываются только РФ-резолверы.

## Клиентские приложения (исследование 2026-05-16)

Клиенты под Slipstream существуют (вопреки первоначальной оценке «нет GUI»):

- **DNSTT.XYZ app** — Android / Windows / macOS / Linux; поддерживает И dnstt,
  И Slipstream; **явно заявлен self-hosted Slipstream-сервер** + выбор списка
  DNS-резолверов + Smart DNS testing. iOS — «coming soon» (пока нет). Самый
  подходящий кандидат под нашу схему (свой сервер + кастомные РФ-резолверы).
- **SlipNet / «SlipTunnel VPN»** — Android (Google Play, `app.slipnet`) +
  кросс-платформенный CLI (macOS/Linux/Windows). Поддерживает Slipstream
  (QUIC, BBR/DCUBIC, keep-alive, authoritative mode), настройка домена и
  резолверов. Лицензия source-available, без редистрибуции. Без iOS.
- **SlipStreamGUI** (mirzaaghazadeh) — десктопный GUI.
- **MasterDnsVPN** — продвинутый DNS-туннель «лучше dnstt/slipstream»
  (свой протокол, не обязательно совместим).

⚠ **Не подтверждено:** наш сервер — официальный `EndPositive/slipstream` v0.1.1
(pre-1.0, протокол нестабилен). Сторонние клиенты могут целить в `slipstream-rust`
(Mygod) или свой вариант протокола — **interop с нашим v0.1.1 не гарантирован,
надо тестировать эмпирически** перед тем, как на это закладываться.

**iOS — главный пробел.** Нативного Slipstream-клиента под iOS сейчас нет
(DNSTT.XYZ iOS «в разработке»). Для iOS реалистичный путь — параллельно
поднять **dnstt**-сервер (медленнее slipstream, но широкая клиентская
экосистема: HTTP Injector, DNSTT.XYZ) и отдавать iOS через dnstt-транспорт.

### macOS-тест официального клиента (2026-05-16) — PASS

Официальный `slipstream-client:v0.1.1` в Docker на Mac (Apple Silicon),
`-r 77.88.8.8:53` (Yandex, по политике), `-d e.moskva.live`:
- handshake через Yandex прошёл («Connection confirmed»), sing-box ответил
  HTTP 400 через туннель — **цепочка Mac → Yandex → Hetzner → sing-box работает**.
- Латентность 1–6с/запрос, нестабильная. ⚠ Это **НЕ показатель**: amd64-образ
  под qemu-эмуляцией на arm64 + Docker Desktop VM-хоп + рекурсия через Yandex.
  Нативная сборка (`cargo build`, Rust есть на маке) даст реальную цифру —
  для честного замера UX на Mac нужен нативный arm64-бинарь, не эмуляция.

Вывод: official-клиент interop с нашим сервером подтверждён на macOS,
RU-резолвер-политика (Yandex) рабочая. Для продуктового UX на Mac — нативный
бинарь или сторонний нативный клиент (с оговоркой про непроверенный протокол).

### Рабочий VPN end-to-end (2026-05-16) — PASS

slipstream-server перенацелен `-a 127.0.0.1:8444 → 127.0.0.1:2054`
(`vless-in-tlc-ya`: plain TCP + Reality + xtls-rprx-vision, SNI `ya.ru` —
проще xhttp). Связка в Docker на Mac (сеть `slipnet`):
`slip` (slipstream-client, `-r 77.88.8.8` Yandex) ← `sb` (sing-box mixed:1080,
VLESS→slip:7000, reality pubkey `BgLsjp3u...`, sid `207fc82a9f9e741f`,
flow vision, uTLS random).

- Egress через прокси = **49.13.201.110 (Hetzner)**, напрямую — реальный IP.
- Стабильность **8/8**, реальные сайты грузятся (Wikipedia 348КБ OK).
- Throughput ~128 кбит/с, латентность 3–5с. ⚠ **НЕ потолок**: образ slipstream
  только amd64 → qemu-эмуляция на Apple Silicon (бинарь тяжёлый по QUIC-крипто) +
  Docker VM + рекурсия. Нативно (RuVDS) handshake был ~100мс. Реальную скорость
  даст только Meson-сборка из исходников или нативный x86_64-Linux клиент.

Прод-параметры VLESS-клиента (для GUI-теста): server=наш slipstream-локалпорт,
uuid юзера из `/opt/VpnBot/vpn.db`, reality pub `BgLsjp3u0Mjk3BqLs7kopcAOF6KOyx14lxHlP7e_yxo`,
sid `207fc82a9f9e741f`, sni `ya.ru`, flow `xtls-rprx-vision`, fp random,
резолвер — только РФ (Yandex 77.88.8.8).

Замечание про сборку из исходников: slipstream — **C/Meson** проект (не Rust/Go),
submodules + meson + ninja + OpenSSL3/QUIC-deps. Официальных build-инструкций нет.
Нативная сборка нетривиальна — отдельная задача.

### GUI interop-тест: DNSTT.XYZ на macOS (2026-05-16) — PASS

`DNSTT-Client-v2.2.0-macOS-arm64.dmg` (нативный arm64; unsigned — снят карантин
`xattr -dr com.apple.quarantine`). Приложение использует **`Mygod/slipstream-rust`**
(submodule `vendor/slipstream-rust`) — Rust-переписка протокола EndPositive.

Серверная подготовка под SOCKS-модель DNSTT.XYZ: на Hetzner поднят
`slipstream-socks.service` (sing-box socks inbound `127.0.0.1:1080`, direct
egress), slipstream-server перенацелен `-a 127.0.0.1:2054 → 127.0.0.1:1080`
(SOCKS5). Самопроверка официальным клиентом до GUI-теста — PASS (egress Hetzner).

Результат GUI: приложение слушает локально **`127.0.0.1:1080`** (НЕ 7000 как в их
README; 7000 на macOS занят AirPlay/ControlCenter — частая ловушка). Конфиг
`--domain e.moskva.live --resolver 77.88.8.8` применился. Egress через прокси =
**49.13.201.110 (Hetzner)**, HTTP 200, ~1.6–2с.

**Вывод: `slipstream-rust` (DNSTT.XYZ) wire-совместим с нашим официальным
EndPositive slipstream-server v0.1.1.** Готовый GUI-клиент (Android + desktop)
работает с нашим сервером — доставка конечным юзерам решаема без своей разработки
для всех платформ КРОМЕ iOS (там DNSTT.XYZ «coming soon»).

⚠ Стабильность не идеальна: 4/5 запросов, крупная страница (Wikipedia) не
догрузилась. Процесс запустился с `--congestion-control dcubic` (хотя в UI
выбран BBR — возможно не применилось). В проде нужен отдельный нагрузочный/
стабильностный прогон + тюнинг CC/multipath.

### КРИТИЧНО: нестабильность EndPositive v0.1.1 сервера (2026-05-16)

Под длительной/повторной нагрузкой официальный `slipstream-server` v0.1.1
**деградирует**: журнал заполняется `send failed: Bad file descriptor`,
`connect() failed: Bad file descriptor`, «File descriptor N was closed» —
похоже на fd-leak. В деградированном состоянии **ронял ВСЕХ клиентов
одновременно** (Android DNSTT.XYZ, desktop DNSTT.XYZ на маке, официальный
клиент — все `egress=000`). conntrack НЕ при чём (был 7089/65536).
**`systemctl restart slipstream-server` полностью восстанавливает** (сразу
после рестарта официальный клиент 3/3 = 200, ~1.5–2с).

Вывод для прода: **EndPositive v0.1.1 (pre-1.0) не готов к продакшену как есть**
— требует периодического рестарта/watchdog. Рекомендация: серверную часть
перевести на **`Mygod/slipstream-rust`** (активно поддерживаемая Rust-переписка
того же протокола, tokio/async; та же реализация, что в клиентах DNSTT.XYZ/
SlipNet → нативный interop + ожидаемо стабильнее). Это снимает разом и
стабильность, и совместимость с GUI-клиентами.

Диагностический приём: если «перестало работать у всех сразу» — первым делом
`journalctl -u slipstream-server` (ищи Bad file descriptor) и рестарт сервиса,
затем baseline официальным клиентом, и только потом дебажить клиента.

### Android DNSTT.XYZ — причина фейла найдена (adb logcat, 2026-05-16)

`adb logcat` (Redmi Note 7, MIUI) дал однозначно:
```
D/DnsttVpnService: Starting slipstream client
E/DnsttVpnService: Slipstream library not available
E/DnsttVpnService: Failed to start slipstream tunnel
E/SlipstreamTest:  Slipstream library not available
```
**Android-релиз DNSTT.XYZ v2.2.0 НЕ содержит нативную Slipstream-библиотеку**
(`slipstream-rust` .so под Android через cargo-ndk отсутствует). На Android в
этом APK работает только DNSTT (KCP); Slipstream физически нет. macOS-сборка
библиотеку содержит → там работало. Это НЕ сервер/конфиг/Private DNS/сеть.

Гочи диагностики: на macOS нет `timeout` (стриминговый `adb logcat` через
`timeout` не пашет — брать `adb logcat -d`). MIUI logcat доступен (буфер не
блокирован, вопреки распространённому мнению). USB-отладка на HyperOS: About →
7 тапов по «Версия HyperOS», не «Номер сборки».

УТОЧНЕНО: причина — **установлен APK НЕ той архитектуры**. Проверка APK:
- `armeabi-v7a` APK (был установлен на arm64-телефоне): только `libgojni.so`
  (Go-DNSTT), **libslipstream НЕТ** — slipstream-rust в 32-бит сборку не кладут.
- `arm64-v8a` APK: содержит **`lib/arm64-v8a/libslipstream_client.so`** ✓.

Т.е. DNSTT.XYZ Android **поддерживает Slipstream**, но только в `arm64-v8a` APK.
Фикс: ставить arm64-v8a (по `getprop ro.product.cpu.abi`). Гоча MIUI: `adb
install` блокируется (`INSTALL_FAILED_USER_RESTRICTED` — нужен «Install via USB»
в Dev options, требует Mi-аккаунт/SIM); обход — `adb push` APK в /sdcard/Download
и установка тапом вручную.

Альтернатива под Android — **SlipNet / «SlipTunnel VPN»** (Google Play
`app.slipnet`, изначально Slipstream-на-Android).

### Android arm64 APK: lib грузится, но «Tunnel verification timed out»

После установки arm64-v8a APK logcat: `SlipstreamBridge: slipstream-client
binary found ... libslipstream_client.so`, `slipstream-client started
successfully on port 7000`, `Slipstream SOCKS5 proxy verified` → затем
`Verifying tunnel connection via HTTP request... Tunnel verification timed
out → Failed`. Т.е. клиент стартует нормально, но трафик через туннель не
идёт = симптом мёртвого сервера, НЕ баг Android.

### Деградация EndPositive v0.1.1 — повторяется (2-й раз/сессия)

Сервер снова сдох (offic. client egress=000 мгновенно, журнал в Bad file
descriptor) после Android/desktop slipstream-rust подключений. Наблюдение
пользователя: **«запрос с Android будто убивает сервер»** — вероятно
кросс-реализационный edge-case: slipstream-rust клиент (Android/desktop
DNSTT.XYZ) гонит fd-leak в EndPositive C v0.1.1 быстрее, чем родной
EndPositive-клиент. Вывод однозначен: **rust-клиенты + rust-сервер (одна
реализация) снимут И нестабильность, И Android-проблему разом.**

### Миграция на Mygod/slipstream-rust — В РАБОТЕ

Hetzner = Ubuntu 24.04 / glibc 2.39 / x86_64 / OpenSSL 3.0.13 (нет rust/cmake
на боксе). Сборка: контейнер `ubuntu:24.04` на RuVDS (x86_64 нативно, без
qemu) → `cargo build --release -p slipstream-server -p slipstream-client`
(submodule picoquic recursive). Бинарь совместим с Hetzner по glibc/openssl.
План деплоя: новый бинарь + systemd с `Restart=always`/watchdog, conntrack-
тюнинг UDP:53 (per их README), цель SOCKS5 127.0.0.1:1080, домен
e.moskva.live, `--reset-seed` для persist stateless-reset. EndPositive v0.1.1
оставить рядом для отката. Затем валидация offic.+slipstream-rust клиентами
и soak на стабильность.

### Миграция на slipstream-rust — РАЗВЁРНУТА и ВАЛИДИРОВАНА (2026-05-16)

Сборка: контейнер `ubuntu:24.04` на RuVDS с `--network host` (без host-сети
не было egress из-за iptables:false), `cargo build --release -p
slipstream-server -p slipstream-client` (~3.5мин, picoquic submodule).
Бинари ELF x86-64 glibc/openssl3 → перенос RuVDS→Mac→Hetzner.

Деплой Hetzner: `/usr/local/bin/slipstream-server-rust`, systemd
`Restart=always`, авто-ECDSA-cert + `--reset-seed` (переживает рестарты),
ТОТ ЖЕ `:5300` / DNAT 53→5300 / target SOCKS `127.0.0.1:1080` /
`-d e.moskva.live`. Откат сохранён: EndPositive бинарь `/usr/local/bin/
slipstream-server` + юнит `/opt/slipstream-rollback/
slipstream-server.service.endpositive`.

Флаги slipstream-rust = суперсет EndPositive (`-l/-a/-d/-c/-k` + `--reset-seed`
`--max-connections` `--idle-timeout-seconds` `--fallback` `--dns-listen-host`).

Валидация: офиц. клиент через Yandex **6/6=200**, egress Hetzner.
**SOAK: 40/40 за ~320с, 0 «Bad file descriptor», server active** →
fd-leak/деградация EndPositive УСТРАНЕНЫ (старый к этому моменту уже падал).
WARN «stream reset/target read error» — норм QUIC-лайфсайкл, не утечка.

**Android на rust-сервере — РАБОТАЕТ** (2026-05-16): после миграции
DNSTT.XYZ Android (arm64) подключается и тоннелит. Подтверждена гипотеза:
кросс-реализационный баг EndPositive-C ↔ slipstream-rust был причиной;
rust↔rust его снял. Главный прод-блокер закрыт.

### КРИТИЧНО: whitelist-сети — нужен СЕТЕВОЙ резолвер, не фиксированный

Юзер: «при белых списках приложение не работает». Причина: конфиг бьёт в
жёсткий `77.88.8.8` (Yandex), а строгая whitelist-сеть **блокирует UDP/53
к произвольному IP — разрешён только резолвер самой сети** (DHCP/captive/
шлюз). На открытых сетях Yandex доступен (работало), в whitelist — нет.
Фикс: в whitelist-сети ставить DNS-сервер приложения = резолвер этой сети
(адрес из Wi-Fi-деталей / шлюз ~192.168.x.1) — он по делегированию
e.moskva.live доносит до нашего NS. Политика «только РФ-резолверы» — для
открытых сетей; в whitelist вынужденно используется сетевой резолвер (норм,
содержимое в QUIC). Предел: если whitelist-резолвер NXDOMAIN-ит всё кроме
белого списка — туннель там невозможен. Идея для прода: автодетект
системного/DHCP-резолвера в клиенте вместо хардкода.

Полезно: офиц. серверный деплой Slipstream у DNSTT.XYZ —
`github.com/dnstt-xyz/slipstream-socks-deploy` (SOCKS-модель = ровно наша
схема, parity подтверждён).

### РЕАЛЬНЫЙ ТАРГЕТ: РФ-мобила с ограничениями — ПРОБИЛОСЬ (2026-05-16)

Тестер в РФ: на открытом Wi-Fi (без whitelist) — всё ОК; whitelist только
на мобильном интернете. После миграции на slipstream-rust + резолвер Yandex
(`77.88.8.8`) — **на ограниченной РФ-мобиле туннель пробился** («яндекс днс
пробился»). Т.е. целевой сценарий обхода работает на rust-сервере с
РФ-резолвером (политика «только РФ-резолверы» подтверждена в боевой среде).
Ранние провалы на мобиле/whitelist были в т.ч. из-за деградации EndPositive,
не только резолвера. (Подтверждение полного egress=Hetzner на мобиле — за
тестером.)

Диагностика на телефоне без dev-режима: чужой logcat на Android 4.1+ НЕ
читается без adb/root (READ_LOGS). Доступно нетех-юзеру: in-app экран
Logs/⋮/Settings, текст ошибки Connect, результат DNS Test, DNS1/DNS2 +
v4/v6 из «Network Info II». Удалённый тестер — см. [[project-slipstream-remote-tester]].

Но **очень медленно** на мобиле (ожидаемо для DNS-tunnel через один
rate-limited резолвер; не баг сервера — rust держит soak 40/40). Рычаги
ускорения: **multipath (несколько РФ-резолверов в DNS Servers — главный
выигрыш)**, Congestion Control=**BBR** (не dcubic — проверить применение),
GSO on. Потолок технологии: десятки–сотни кбит/с, ~1 Мбит/с лучший случай
на multipath; годен для текста/мессенджеров, не для видео.

Egress=Германия на РФ-мобиле ПОДТВЕРЖДЁН тестером — полный VPN на боевом
таргете работает.

Серверный тюнинг применён (Hetzner, `/etc/sysctl.d/99-slipstream.conf`,
persistent): `nf_conntrack_max=262144`, `rmem_max/wmem_max=16MB`. НЕ менял
`nf_conntrack_udp_timeout` (shared прод-бокс: WG/Hysteria2/telemt/VK TURN —
глобальный UDP-таймаут рискован; запаса conntrack и так много). Откат —
удалить файл. Это для масштаба/стабильности, НЕ главный рычаг скорости.

Главный рычаг скорости = **client multipath**: несколько РФ-резолверов в
DNSTT.XYZ DNS Servers (Yandex 77.88.8.8/.1, операторский, AdGuard RU
94.140.14.14/15.15, Comss 83.220.169.155 — все что проходят Test) →
QUIC-multipath суммирует полосу + обходит rate-limit. + BBR (не dcubic),
GSO on. Потолок ~сотни кбит/с–1Мбит/с (текст/мессенджеры, не видео).

### Ограничение скорости: DNSTT.XYZ без multipath (2026-05-16)

Тестер: кнопка **Test ненадёжна** (ложные fail хотя Yandex реально
работает) — критерий только реальный Connect. **DNSTT.XYZ НЕ поддерживает
multipath** (в логах один `--resolver`) → главный рычаг скорости в этом
клиенте недоступен. SlipNet — тоже без явного multipath UI + требует свой
стек SlipGate (отдельная история, неясный interop). Сервер (slipstream-rust)
multipath поддержал бы — ограничение чисто клиентское.

Доступно в DNSTT.XYZ (модест): Congestion Control=**BBR** (было dcubic,
проверить применение), GSO on, попробовать операторский DNS как
единственный резолвер vs Yandex (замер реальной скоростью, не Test).
Потолок single-resolver ≈ десятки–100+ кбит/с (текст/мессенджеры, не видео).

**Ключевое продуктовое решение:** юзабельная скорость требует
multipath-клиента. Варианты: (а) оценить SlipNet+SlipGate отдельным треком,
(б) форк/доработка GUI под несколько `--resolver` (slipstream-rust сам умеет
multipath — вопрос только UI клиента). Это главный нерешённый вопрос для
продакшена наравне с iOS.

Потом: BBR/операторский-резолвер замер (#10), решение по multipath-клиенту,
iOS через dnstt.

Команда smoke-теста плумбинга (запускалась на Hetzner; `pkill -f slipstream-client`
НЕ использовать — матчит саму ssh-обёртку, kill только по PID):
```
setsid /usr/local/bin/slipstream-client -r 127.0.0.1:5300 -d e.moskva.live -l 7000 \
       >/tmp/slip-client.log 2>&1 & CPID=$!
curl -s -o /dev/null -w '%{http_code}\n' --max-time 6 http://127.0.0.1:7000/   # → 400 (sing-box ответил через туннель)
kill $CPID
```

## Откат (всё обратимо, аддитивно)

На Hetzner:
```
systemctl disable --now slipstream-server
rm /etc/systemd/system/slipstream-server.service && systemctl daemon-reload
iptables -t nat -D PREROUTING -i eth0 -p udp --dport 53 -j REDIRECT --to-ports 5300
ufw delete allow 53/udp ; ufw delete allow 5300/udp
rm -rf /opt/slipstream
# Hetzner Cloud Firewall: удалить правило udp/53 (read-modify-write, тем же
#   способом — забрать текущие правила, убрать udp/53, set_rules полным набором)
# systemd-resolved / sing-box не затрагивались
```

На RuVDS (если откатывать тестовый плацдарм):
```
docker ps -aq --filter ancestor=ghcr.io/endpositive/slipstream-client:v0.1.1 | xargs -r docker rm -f
apt-get remove -y docker.io ; rm /etc/docker/daemon.json
# при сомнениях восстановить iptables: iptables-restore < /root/iptables-before-docker.rules
```

## Открытые вопросы (PoC пройден, дальше)

- **Доставка клиента конечным пользователям** — главный нерешённый вопрос: нет
  mobile/desktop GUI, бинарь требует свежий glibc. Варианты: Docker/обёртка,
  свой порт, инструкция. Без этого ценность для конечного пользователя ≈ 0.
- Пропускная способность под реальной нагрузкой не измерена (тестировали latency
  одного запроса). Заявленные Мбит/с — multipath/direct; через один рекурсивный
  резолвер будет ниже, тюнить multipath (несколько `-r`).
- Строгий whitelist-тест (отель/корп/мобайл с реальным белым списком) не делался —
  RuVDS это российский транзит, не строгая access-сеть.
- Интеграция в код по образцу telemt-зеркала (`service/slipstream*.go` + API +
  тумблер в админке) — если решат продуктизировать.
- DNAT на Hetzner и Docker-обвязка на RuVDS не персистентны к ребуту (iptables NAT
  стирается) — для продакшена нужна персистентность правил.
