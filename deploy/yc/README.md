# Secondary backhaul: RuVDS → Yandex Cloud → Hetzner (WSS)

## Почему не Yandex Cloud CDN

Задача формулировалась как «WSS через Yandex Cloud CDN». По актуальной
официальной документации это сейчас невозможно сделать самостоятельно:

* **WebSocket в CDN включается только через обращение в поддержку.** Раздел
  [Решение проблем в Cloud CDN](https://yandex.cloud/ru/docs/cdn/troubleshooting)
  прямо говорит: «Чтобы включить протокол WebSocket, обратитесь в поддержку»,
  указав сценарий использования, задачи и примерный объём трафика. Селф-сервис
  переключателя в консоли/API нет.
* **Источник должен быть доменным именем, не IP**, и CDN не поддерживает IPv6
  к источнику ([Источники и группы источников](https://yandex.cloud/ru/docs/cdn/concepts/origins)).
* **Источник обязан ответить за 5 секунд**, иначе клиент получает `504`. Для
  长-lived WebSocket это допущение как минимум небезопасное.

Итог: CDN — это кэширующий HTTP-слой, а нам нужен прозрачный长-lived
двунаправленный поток. Ставить прод на компонент, который включается тикетом и
режет источник по 5-секундному таймауту, нельзя.

## Что используем вместо

**Ближайший поддерживаемый сервис — Yandex Compute Cloud**: обычная виртуальная
машина в `ru-central1`, на ней Caddy, который терминирует наш TLS на нашем
домене и проксирует WebSocket на Hetzner.

Почему именно она:

* Compute — единственный сервис YC, который позволяет обслуживать произвольный
  长-lived TCP/WebSocket-поток и ходить к внешнему (не-YC) бэкенду.
* Application Load Balancer WebSocket умеет
  ([HTTP-роутеры](https://yandex.cloud/ru/docs/application-load-balancer/concepts/http-router)),
  но его бэкенд-группы состоят из ресурсов внутри VPC — указать Hetzner
  напрямую нельзя. То есть ALB всё равно потребует ту же виртуалку, только с
  лишним слоем и лишними деньгами.
* Network Load Balancer — L4, но целевые группы тоже только внутри VPC.

Схема остаётся CDN-ready: если поддержка YC включит WebSocket на аккаунте,
CDN-ресурс можно поставить перед этой же виртуалкой (источник по FQDN, HTTPS),
ничего не меняя ни на RuVDS, ни на Hetzner.

## Ручные действия в Yandex Cloud

Их нельзя выполнить из репозитория — нужен доступ к консоли/аккаунту.

1. **Создать ВМ** в `ru-central1` (хватает 2 vCPU / 2 GB, `standard-v3`,
   прерываемая — не надо, нужен стабильный аптайм). Образ — Ubuntu 24.04 LTS.
2. **Выдать публичный IPv4**, лучше статический
   (`VPC → IP-адреса → Зарезервировать`), иначе адрес слетит при остановке ВМ.
3. **Группа безопасности**: входящие `tcp/443` и `tcp/80` из `0.0.0.0/0`
   (80 нужен только для выпуска сертификата Let's Encrypt), `tcp/22` — со
   своего адреса. Исходящие — разрешить всё.
4. **DNS**: A-запись `cdn-ru.myvpn-api.online` → публичный IP этой ВМ.
   Домен наш; чужие домены и SNI не используются нигде.
5. Прописать полученный IP в `deploy/backhaul/params.env` как `YC_VM_IP`.
6. Запустить на ВМ `deploy/yc/install.sh` (см. ниже).

Если позже захочется всё-таки CDN — открыть тикет в поддержку YC с описанием
сценария (WebSocket-туннель, оценка трафика), после включения создать
CDN-ресурс с источником `cdn-ru.myvpn-api.online` по HTTPS и переключить
A-запись на CDN.

## Установка на ВМ

```bash
scp deploy/yc/install.sh deploy/backhaul/params.env ubuntu@<YC_VM_IP>:/tmp/
ssh ubuntu@<YC_VM_IP> 'sudo bash /tmp/install.sh /tmp/params.env'
```

Проверка снаружи:

```bash
curl -sS -o /dev/null -w '%{http_code}\n' https://cdn-ru.myvpn-api.online/healthz   # 404 — это нормально
curl -sS -i -N --http1.1 \
  -H 'Connection: Upgrade' -H 'Upgrade: websocket' \
  -H 'Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==' -H 'Sec-WebSocket-Version: 13' \
  https://cdn-ru.myvpn-api.online/bhws | head -1     # ожидаем 101
```
