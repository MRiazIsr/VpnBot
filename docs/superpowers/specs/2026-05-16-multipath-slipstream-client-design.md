# Multipath Slipstream-клиент — дизайн (фаза 1)

Дата: 2026-05-16
Связано: `docs/slipstream-poc.md` (рабочий PoC, сервер slipstream-rust на Hetzner)

## Контекст и цель

PoC доказан: DNS-туннель Slipstream через российский рекурсивный резолвер
обходит whitelist на РФ-мобиле, выход = Hetzner (Германия). Сервер —
`Mygod/slipstream-rust` slipstream-server на Hetzner (`e.moskva.live`,
SOCKS-таргет, стабилен).

Блокеры для практического использования:
1. Готовый GUI-клиент **DNSTT.XYZ не умеет multipath** (один `--resolver`) →
   скорость ограничена одним rate-limited резолвером.
2. Резолвер захардкожен → в whitelist/мобиле часто недостижим; подбор вручную.
3. Нетех-тестер в РФ не может снять логи (нет adb/dev-режима).

**Цель фазы 1:** форк DNSTT.XYZ с (а) авто-discovery рабочих резолверов,
(б) multipath по всем рабочим, (в) ручным вводом DNS (дополняет авто),
(г) встроенной диагностикой. Целевой пользователь — нетех человек в РФ на
Android-мобиле; тестирование удалённое.

## Scope

**В фазе 1:** форк DNSTT.XYZ (Flutter + Kotlin + встроенный
`libslipstream_client.so` из vendor/slipstream-rust); discovery; multipath
(все рабочие); ручные DNS дополняют авто; fail-closed; one-tap UX +
diagnostics export.

**Вне фазы 1 (отдельные инициативы):**
- Адаптивный авто-выбор транспорта (raw UDP / direct DNS-53 к нашему IP /
  recursive-multipath) — переформулированная «безумная идея», фаза 2.
- iOS (нет slipstream-клиента; путь через dnstt).
- Серверная продуктизация slipstream-rust в кодбазу vpnbot (по образцу telemt).

## Архитектурный подход

Подход **A**: вся логика discovery+multipath в Dart/Kotlin-слое приложения;
`vendor/slipstream-rust` НЕ форкаем (форк близок к upstream, легко ребейзить;
multipath используем нативный — slipstream-rust принимает повторяющиеся
`--resolver`). Страховка **C**: если проба коротким спавном клиента окажется
ненадёжной/медленной — добавить минимальный upstream-дружелюбный флаг
`--probe` в Rust-клиент. **B** (форк Rust целиком) отвергнут — двойное
сопровождение.

## Компоненты

Каждый — одна зона ответственности, тестируется изолированно.

1. **`ResolverEnumerator` (Kotlin)** — собирает кандидатов: системные/DHCP DNS
   подлежащей сети (`ConnectivityManager.LinkProperties.dnsServers`), IP шлюза
   (best-effort), ручные DNS пользователя. v4 и v6. Дедуп, тег источника.
   Выход: `List<Candidate{ip, port=53, source}>`.

2. **`EligibilityGate` (Kotlin/Dart)** — дёшево по кандидату: plain A-запрос
   whitelisted-набора (`yandex.ru, vk.com, ozon.ru, gosuslugi.ru`) +
   анти-hijack (запрос случайного несуществующего имени → ждём NXDOMAIN).
   Выход: passed/failed + флаг `hijacked` + причина. Timeout ~2с/запрос.

3. **`HandshakeProber` (Kotlin)** — по выжившим: короткий спавн встроенного
   slipstream-client (`--resolver <cand> --domain e.moskva.live -l
   <ephemeral>`), успех = «Connection confirmed» в пределах timeout
   (~8–12с), опц. один крошечный SOCKS-запрос → чистый kill по хэндлу.
   Записывает успех + handshake-RTT. Единственный честный критерий.

4. **`MultipathLauncher` (Kotlin)** — берёт ВСЕ рабочие резолверы, запускает
   один долгоживущий slipstream-client с повторяющимися `--resolver`,
   `--domain e.moskva.live`, фикс. `-l <порт>`, `--congestion-control bbr`
   (дефолт, оверрайд), `--gso true`, keep-alive 400.

5. **`DiscoveryOrchestrator` (Dart)** — связывает 1→2→3→4, состояние/прогресс
   в UI, кэш по идентичности сети, ретриггеры.

6. **UI (Flutter)** — one-tap Connect (весь пайплайн авто); Advanced —
   ручные DNS, congestion control, GSO, список найденных резолверов;
   «Поделиться диагностикой».

Граница: сервер на Hetzner НЕ меняется (multipath уже держит).

## Алгоритм discovery

**Шаг 1 — Enumeration.** Системные/DHCP DNS читаются у подлежащей сети
(Wi-Fi/Cellular) **ДО поднятия VpnService** (иначе вернётся DNS туннеля).
Плюс шлюз (best-effort) и ручные DNS. IPv4 и IPv6 (`[v6]:53`). Дедуп.

**Шаг 2 — Eligibility gate** (параллельно по кандидатам, timeout ~2с):
plain A-запрос whitelisted-набора → passed, если резолвятся в правдоподобные
IP. Анти-hijack: случайное несуществующее имя → ждём NXDOMAIN; иначе флаг
`hijacked` (не отсекаем жёстко, понижаем приоритет — финальный судья
handshake). При прозрачном перехвате IP кандидата неважен — гейт меряет
эффективный путь.

**Шаг 3 — Mini-handshake** (по выжившим, параллельно, лимит конкуренции
~4–6, timeout ~8–12с): спавн встроенного клиента; успех = «Connection
confirmed»; опц. крошечный SOCKS-запрос для проверки data-path; чистый kill
по хэндлу процесса. Пишем успех + RTT (диагностика/порядок; в multipath
идут ВСЕ рабочие).

**Критично:** пробные сокеты идут МИМО VpnService (`protect()` / подлежащая
сеть) — иначе петля через полуподнятый туннель.

**Результат:** рабочий набор = passed gate И confirmed handshake
(авто + ручные вместе).

**Кэш:** ключ = идентичность сети (Wi-Fi SSID/BSSID или cellular netId) +
отпечаток DNS; TTL. Ретриггер: смена сети (NetworkCallback), ручной refresh,
деградация активного туннеля (все пути падают). Ноль рабочих → fail-closed,
понятная ошибка + `[Повторить]/[Свой DNS]/[Сменить сеть]`.

## Multipath launch & lifecycle

Один долгоживущий slipstream-client со всеми рабочими `--resolver` (нативный
QUIC-multipath). Переиспользуется существующий `DnsttVpnService` + tun2socks
DNSTT.XYZ: трафик → локальный SOCKS → multipath-туннель → SOCKS на Hetzner →
интернет.

**Анти-петля:** исходящие UDP/53 клиента исключены из VpnService (тот же
app-uid, который DNSTT.XYZ уже исключает — подтвердить, что спавн-процесс
наследует).

**Жизненный цикл:**
- *Connect:* discovery → запуск multipath-клиента → ждём ≥1 confirmed путь →
  поднять VpnService/tun2socks → connected.
- *Падение части путей:* slipstream-rust мигрирует на живые. Падение всех →
  re-discovery + relaunch с экспон. backoff.
- *Смена сети:* NetworkCallback (дебаунс) → стоп → re-discovery → relaunch.
- *Stop:* чистый kill по хэндлу + teardown VpnService. Краш → рестарт с
  backoff, лимит ретраев → экран ошибки.

**Поведение при оборванном туннеле: fail-closed (дефолт).** Пока туннель не
поднят/реконнектится — трафик наружу НЕ выпускается (не светит реальный
IP/трафик в подконтрольной сети). Fail-open не дефолт.

## UX (нетех удалённый РФ-юзер)

Принцип: одна кнопка, «само работает», сложность скрыта.

- **Connect** + статусы живым языком: `Поиск рабочего DNS…` →
  `Проверка путей…` → `Подключение…` → **«Защищено · выход: Германия»**.
- Fail-closed реконнект: **«Переподключение… интернет временно заблокирован
  для безопасности»**.
- Ошибки простым языком + действия `[Повторить]/[Свой DNS]/[Сменить сеть]`.
- **Advanced** (скрыто): ручные DNS (дополняют авто), Congestion Control
  (BBR деф/dcubic), GSO, список найденных резолверов с результатом+RTT.
- **«Поделиться диагностикой»**: текстовый блоб (найденные резолверы,
  результаты gate/handshake, хвост лога клиента, тип сети, IP v4/v6) →
  основной канал полевого фидбэка (решает «нетех-тестер без adb»).
- DNSTT(KCP)-ветка не трогается (минимальный diff); дефолтный поток —
  Slipstream multipath.

## Error handling & краевые случаи

- Ноль рабочих → fail-closed + действия, без утечек.
- Все пути умерли → re-discovery + relaunch, backoff, лимит → ошибка.
- Гонки смены сети → дебаунс, чистый teardown, защита от двойных процессов.
- Утечки/батарея проб → watchdog+kill по хэндлу, лимит конкуренции, отмена
  при уходе в фон; дешёвый gate отсекает мусор до дорогих проб.
- IPv6-only → `[v6]:53`; рекурсия через v6-резолвер до нашего NS — норма.
- Прозрачный перехват DNS → ловит «эффективный путь» проба; режет EDNS →
  handshake не пройдёт (корректно).
- Captive portal → gate ловит hijack → подсказка «нужен вход в сеть».
- Ordering → upstream-DNS и пробы на protected-сокетах ДО/независимо от
  VpnService; пробы никогда через tun.
- Кэш → ключ = сеть + отпечаток DNS; TTL; ручной refresh всегда перепробует.
- Невалидный ручной DNS → валидация IP/host[:port] v4/v6, мягкий отказ.
- Триаж в диагностике: gate ок но handshake падает у всех (вкл.
  заведомо-рабочий) → вероятно сервер; gate падает везде → сеть.

## Тестирование

- **Unit (Kotlin/Dart):** `ResolverEnumerator` (моки LinkProperties),
  `EligibilityGate` (моки DNS: pass/hijack/captive/timeout), кэш-ключ+TTL,
  валидация ручного DNS, сборка multipath-аргументов (повтор `--resolver`,
  `[v6]:53`, BBR/GSO).
- **Integration против РЕАЛЬНОГО сервера** (Hetzner, `e.moskva.live`):
  `HandshakeProber`+`MultipathLauncher` end-to-end, **сервер не мокается
  никогда** (урок: обычный DNS-тест даёт ложные результаты, единственная
  правда — реальный handshake). Харнесс: встроенный клиент → реальный сервер
  через заведомо-рабочий резолвер → «Connection confirmed» + egress=Hetzner.
- **Полевая матрица** (удалённый тестер, через «Поделиться диагностикой»):
  открытый Wi-Fi (baseline), РФ-мобила+whitelist (главный таргет),
  Wi-Fi+whitelist, IPv6-only мобила, captive. Критерий: discovery ≥1,
  connect, выход=Германия, fail-closed корректен.
- CI: сборка APK + unit; integration — gated/manual (нужны сеть+сервер).

## Сборка / доставка

- Форк-репо DNSTT.XYZ. **Обязательно arm64-v8a APK с
  `libslipstream_client.so`** (armeabi-v7a его не включает — уже била).
- Свой keystore, self-signed → sideload. Нетех-РФ-тестеру: прямая ссылка на
  APK + «разрешить установку» + гоча MIUI `INSTALL_FAILED_USER_RESTRICTED`
  → установка тапом вручную.
- **Пин коммита `vendor/slipstream-rust` = тот же, что у серверной сборки**
  (протокол pre-1.0, клиент/сервер должны быть protocol-aligned — жёсткое
  требование; записать коммит при сборке сервера и при сборке клиента).
- Обновления — новая ссылка на APK, без стора.

## Жёсткие требования / constraints

- Клиент и сервер используют **один и тот же протокол-коммит**
  slipstream-rust.
- Discovery-пробы и upstream-DNS-чтение — строго мимо VpnService.
- Серверный handshake-путь не мокается в тестах.
- Fail-closed по умолчанию.
- Целевая ABI с slipstream-либой: arm64-v8a (минимум).

## Открытые вопросы

Нет. Все ключевые решения зафиксированы в ходе брейншторма
(форма=форк, проба=реальный handshake, источники=системные/DHCP +
whitelisted-домен-гейт, multipath=все рабочие, ручной=дополняет авто,
fail-closed=дефолт, подход=A+C-страховка).
