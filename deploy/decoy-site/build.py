#!/usr/bin/env python3
"""Сборка статического городского справочника moskva.live.

Сайт обслуживает две задачи одновременно:
  1. это настоящий сайт, который открывается в браузере и выглядит живым;
  2. он же — fallback для всего, что не попало в VPN-путь.

Требования, из которых выросла реализация:

* **Только stdlib.** Ни pip, ни venv на боевой машине. Меньше зависимостей —
  меньше поводов сломаться при обновлении.
* **Никакого CMS и никакого рантайма.** На выходе статика, которую отдаёт
  Caddy. Нет ни PHP, ни базы, ни админки — нечего ломать и нечего чинить.
* **Только заголовки и ссылки на источник.** Полные тексты чужих статей не
  копируются: это чужой копирайт, а жалоба регистратору разделегирует домен —
  и разом умрут все ссылки пользователей. Ради приманки такой риск бессмысленен.
* **Отказоустойчивость важнее свежести.** Если лента недоступна, страница
  собирается со старыми данными, а не падает. Пустой или сломанный сайт
  привлекает больше внимания, чем сайт с новостями вчерашнего дня.

Запуск: build.py [--out /var/www/moskva.live] [--no-network]
"""

import argparse
import html
import json
import os
import shutil
import sys
import tempfile
import urllib.error
import urllib.request
import xml.etree.ElementTree as ET
from datetime import datetime, timezone, timedelta

HERE = os.path.dirname(os.path.abspath(__file__))
UA = "Mozilla/5.0 (compatible; moskva.live/1.0; +https://moskva.live/)"
MSK = timezone(timedelta(hours=3))

# Публичные ленты. Берём только заголовок и ссылку на первоисточник.
#
# Список зависит от того, где идёт сборка, и это не мелочь: mos.ru и
# transport.mos.ru отдают заграничным адресам таймаут и HTTP 477 — проверено.
# Сборка живёт на RuVDS (российский адрес), поэтому дефолт — городские
# источники. Набор для Hetzner оставлен рядом на случай переноса сборки.
FEEDS_RU = [
    ("Москва 24", "https://www.m24.ru/rss.xml"),
    ("Мэр Москвы", "https://www.mos.ru/rss/"),
]
# Проверено доступными из Германии: m24.ru, lenta.ru, tass.ru, ria.ru, vedomosti.ru.
FEEDS_ABROAD = [
    ("Москва 24", "https://www.m24.ru/rss.xml"),
    ("Лента.ру", "https://lenta.ru/rss/news"),
]

FEEDS_CONF = "/etc/moskva-live/feeds.conf"


def load_feed_list():
    """Читает feeds.conf вида `Название|URL` по строке; иначе — дефолт."""
    try:
        out = []
        with open(FEEDS_CONF, encoding="utf-8") as f:
            for line in f:
                line = line.strip()
                if not line or line.startswith("#") or "|" not in line:
                    continue
                name, url = line.split("|", 1)
                out.append((name.strip(), url.strip()))
        if out:
            return out
    except OSError:
        pass
    return FEEDS_RU


FEEDS = load_feed_list()

PAGES = [
    ("index.html", "Москва — городской справочник", None),
    ("transport.html", "Транспорт", "Транспорт"),
    ("districts.html", "Округа и районы", "Округа"),
    ("about.html", "О сайте", "О сайте"),
]


def fetch(url, timeout=12):
    req = urllib.request.Request(url, headers={"User-Agent": UA})
    with urllib.request.urlopen(req, timeout=timeout) as r:
        return r.read()


def parse_feed(raw, limit=8):
    """Достаём заголовок + ссылку. Понимаем и RSS, и Atom."""
    items = []
    try:
        root = ET.fromstring(raw)
    except ET.ParseError:
        return items
    for it in root.iter():
        tag = it.tag.split("}")[-1]
        if tag not in ("item", "entry"):
            continue
        title = link = None
        for ch in it:
            ctag = ch.tag.split("}")[-1]
            if ctag == "title" and ch.text:
                title = " ".join(ch.text.split())
            elif ctag == "link":
                link = ch.text.strip() if ch.text else ch.get("href")
        if title and link:
            items.append((title, link))
        if len(items) >= limit:
            break
    return items


def load_news(cache_path, offline=False):
    """Свежие заголовки, а при неудаче — прошлые из кэша."""
    fresh = []
    if not offline:
        for name, url in FEEDS:
            try:
                items = parse_feed(fetch(url))
            except (urllib.error.URLError, OSError, TimeoutError) as e:
                print(f"  лента {name}: недоступна ({e}) — пропускаем", file=sys.stderr)
                continue
            if items:
                fresh.append({"source": name, "items": items[:6]})

    if fresh:
        try:
            with open(cache_path, "w", encoding="utf-8") as f:
                json.dump({"at": datetime.now(MSK).isoformat(), "blocks": fresh}, f,
                          ensure_ascii=False, indent=1)
        except OSError:
            pass
        return fresh, datetime.now(MSK)

    try:
        with open(cache_path, encoding="utf-8") as f:
            cached = json.load(f)
        print("  свежих лент нет — берём кэш", file=sys.stderr)
        return cached.get("blocks", []), datetime.fromisoformat(cached["at"])
    except (OSError, ValueError, KeyError):
        return [], None


def load_weather(cache_path, offline=False):
    """Погода с open-meteo: без ключа, без регистрации, свободная лицензия."""
    if not offline:
        url = ("https://api.open-meteo.com/v1/forecast?latitude=55.7558&longitude=37.6173"
               "&current=temperature_2m,wind_speed_10m,relative_humidity_2m"
               "&timezone=Europe%2FMoscow")
        try:
            data = json.loads(fetch(url))
            cur = data["current"]
            w = {"t": round(cur["temperature_2m"]),
                 "wind": round(cur["wind_speed_10m"]),
                 "hum": cur["relative_humidity_2m"]}
            try:
                with open(cache_path, "w", encoding="utf-8") as f:
                    json.dump(w, f)
            except OSError:
                pass
            return w
        except (urllib.error.URLError, OSError, ValueError, KeyError, TimeoutError) as e:
            print(f"  погода недоступна ({e})", file=sys.stderr)
    try:
        with open(cache_path, encoding="utf-8") as f:
            return json.load(f)
    except (OSError, ValueError):
        return None


def layout(title, nav_active, body, updated):
    nav = []
    for fn, _t, label in PAGES:
        if not label:
            continue
        cls = ' class="active"' if label == nav_active else ""
        nav.append(f'<a href="/{fn}"{cls}>{label}</a>')
    stamp = updated.strftime("%d.%m.%Y, %H:%M") if updated else "—"
    return f"""<!doctype html>
<html lang="ru">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{html.escape(title)}</title>
<meta name="description" content="Городской справочник по Москве: транспорт, округа, погода, городские новости.">
<link rel="stylesheet" href="/style.css">
<link rel="icon" href="/favicon.svg" type="image/svg+xml">
</head>
<body>
<header>
  <a class="brand" href="/">moskva<span>.live</span></a>
  <nav>{''.join(nav)}</nav>
</header>
<main>
{body}
</main>
<footer>
  <p>Обновлено: {stamp} МСК</p>
  <p>Справочный сайт о Москве. Заголовки новостей ведут на сайты первоисточников.</p>
</footer>
</body>
</html>
"""


def render_index(news, weather, updated):
    w = ""
    if weather:
        w = f"""
  <aside class="weather">
    <h2>Погода в Москве</h2>
    <p class="temp">{weather['t']:+d}&nbsp;°C</p>
    <p class="meta">Ветер {weather['wind']} км/ч · влажность {weather['hum']}%</p>
    <p class="src">Данные: <a href="https://open-meteo.com/" rel="nofollow noopener">Open-Meteo</a></p>
  </aside>"""

    blocks = []
    for b in news:
        lis = "".join(
            f'<li><a href="{html.escape(u)}" rel="nofollow noopener">{html.escape(t)}</a></li>'
            for t, u in b["items"])
        blocks.append(f'<section class="feed"><h3>{html.escape(b["source"])}</h3><ul>{lis}</ul></section>')
    news_html = "".join(blocks) or "<p class='empty'>Ленты временно недоступны.</p>"

    body = f"""
  <h1>Москва — городской справочник</h1>
  <p class="lead">Короткая справка по городу: как устроен транспорт, из чего состоит
  административное деление, какая сейчас погода и что пишут городские источники.</p>
{w}
  <h2>Городские новости</h2>
  <p class="note">Ниже — заголовки из открытых лент. Полные тексты не публикуются:
  каждая ссылка ведёт на сайт источника.</p>
  <div class="feeds">{news_html}</div>

  <h2>Разделы</h2>
  <ul class="cards">
    <li><a href="/transport.html"><b>Транспорт</b><span>Метро, МЦК, МЦД, наземный транспорт, оплата проезда</span></a></li>
    <li><a href="/districts.html"><b>Округа и районы</b><span>Административное деление города</span></a></li>
    <li><a href="/about.html"><b>О сайте</b><span>Что это и откуда данные</span></a></li>
  </ul>
"""
    return layout("Москва — городской справочник", None, body, updated)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--out", default="/var/www/moskva.live")
    ap.add_argument("--cache", default="/var/lib/moskva-live")
    ap.add_argument("--no-network", action="store_true",
                    help="собрать из кэша, не ходя в сеть (для тестов)")
    args = ap.parse_args()

    os.makedirs(args.cache, exist_ok=True)
    news, updated = load_news(os.path.join(args.cache, "news.json"), args.no_network)
    weather = load_weather(os.path.join(args.cache, "weather.json"), args.no_network)
    if updated is None:
        updated = datetime.now(MSK)

    # Пишем во временный каталог и подменяем одним rename: посетитель никогда
    # не увидит полусобранный сайт.
    tmp = tempfile.mkdtemp(prefix=".build-", dir=os.path.dirname(os.path.abspath(args.out)))
    try:
        for fn, title, label in PAGES:
            if fn == "index.html":
                out = render_index(news, weather, updated)
            else:
                frag_path = os.path.join(HERE, "pages", fn)
                with open(frag_path, encoding="utf-8") as f:
                    out = layout(title, label, f.read(), updated)
            with open(os.path.join(tmp, fn), "w", encoding="utf-8") as f:
                f.write(out)

        for asset in ("style.css", "robots.txt", "favicon.svg"):
            shutil.copy(os.path.join(HERE, "static", asset), os.path.join(tmp, asset))

        os.chmod(tmp, 0o755)
        for f in os.listdir(tmp):
            os.chmod(os.path.join(tmp, f), 0o644)

        old = args.out + ".old"
        shutil.rmtree(old, ignore_errors=True)
        if os.path.exists(args.out):
            os.rename(args.out, old)
        os.rename(tmp, args.out)
        shutil.rmtree(old, ignore_errors=True)
        tmp = None
    finally:
        if tmp:
            shutil.rmtree(tmp, ignore_errors=True)

    print(f"собрано в {args.out}: страниц {len(PAGES)}, лент {len(news)}, "
          f"погода {'есть' if weather else 'нет'}")


if __name__ == "__main__":
    main()
