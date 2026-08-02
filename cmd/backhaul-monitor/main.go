// backhaul-monitor — демон и CLI управления тройным backhaul'ом на RuVDS.
//
// Запускается на RuVDS (а не на Hetzner) намеренно: проверки идут через
// локальные SOCKS5-адаптеры на 127.0.0.1, и failover обязан работать даже
// когда управляющий канал до Hetzner лежит.
//
//	backhaul-monitor -config /etc/vpnbot/backhaul.json                 # демон
//	backhaul-monitor -config ... -once                                 # один цикл
//	backhaul-monitor -config ... -dry-run                              # решения без переключений
//	backhaul-monitor -config ... -status                               # состояние
//	backhaul-monitor -config ... -probe                                # разовая проверка всех backhaul'ов
//	backhaul-monitor -config ... -force vless=secondary                # ручной пин
//	backhaul-monitor -config ... -force mtproto=                       # снять пин
//	backhaul-monitor -config ... -render-relay > /etc/sing-box-relay/config.json
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"vpnbot/service/backhaul"
)

func main() {
	var (
		configPath  = flag.String("config", "/etc/vpnbot/backhaul.json", "путь к backhaul.json")
		dryRun      = flag.Bool("dry-run", false, "считать и логировать решения, но не переключать")
		once        = flag.Bool("once", false, "один цикл проверки и выход")
		status      = flag.Bool("status", false, "напечатать сохранённое состояние и выйти")
		probeOnly   = flag.Bool("probe", false, "разово проверить все backhaul'ы и напечатать результат")
		force       = flag.String("force", "", "ручной пин: class=tier (пустой tier снимает пин)")
		renderRelay = flag.Bool("render-relay", false, "напечатать sing-box-конфиг relay и выйти")
		checkOnly   = flag.Bool("check", false, "проверить конфиг и выйти")
	)
	flag.Parse()

	cfg, err := backhaul.LoadConfig(*configPath)
	if err != nil {
		fatal(err)
	}

	if *checkOnly {
		if _, err := backhaul.RenderRelayConfig(cfg); err != nil {
			fatal(err)
		}
		fmt.Println("OK: конфиг валиден")
		return
	}

	if *renderRelay {
		raw, err := backhaul.RenderRelayConfig(cfg)
		if err != nil {
			fatal(err)
		}
		os.Stdout.Write(raw)
		return
	}

	logger := newLogger(cfg.LogPath)

	if *status {
		st, err := backhaul.LoadState(cfg.StatePath, cfg.ClassesInUse(), cfg.Tiers)
		if err != nil {
			logger.Warn("состояние прочитано с ошибкой", "err", err)
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(st); err != nil {
			fatal(err)
		}
		return
	}

	if *probeOnly {
		runProbeOnly(cfg)
		return
	}

	m, err := backhaul.NewMonitor(cfg, logger)
	if err != nil {
		fatal(err)
	}
	m.DryRun = *dryRun

	ctx, cancel := signal.NotifyContext(context.Background(),
		syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if *force != "" {
		cls, tier, err := parseForce(*force, cfg)
		if err != nil {
			fatal(err)
		}
		if err := m.Force(ctx, cls, tier); err != nil {
			fatal(err)
		}
		return
	}

	if *once {
		m.Sync(ctx)
		for _, d := range m.Tick(ctx) {
			raw, _ := json.Marshal(d)
			fmt.Println(string(raw))
		}
		return
	}

	if err := m.Run(ctx); err != nil && ctx.Err() == nil {
		fatal(err)
	}
}

func runProbeOnly(cfg *backhaul.Config) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	failed := false
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	for _, cls := range cfg.ClassesInUse() {
		for _, t := range cfg.SortedTiers() {
			res := backhaul.Probe(ctx, cfg.Probe, t, cls)
			_ = enc.Encode(res)
			if !res.OK {
				failed = true
			}
		}
	}
	if failed {
		os.Exit(1)
	}
}

// parseForce разбирает "vless=secondary" / "mtproto=".
func parseForce(s string, cfg *backhaul.Config) (backhaul.ServiceClass, string, error) {
	parts := strings.SplitN(s, "=", 2)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("-force ожидает вид class=tier, получено %q", s)
	}
	cls := backhaul.ServiceClass(strings.TrimSpace(parts[0]))
	known := false
	for _, c := range cfg.ClassesInUse() {
		if c == cls {
			known = true
		}
	}
	if !known {
		return "", "", fmt.Errorf("неизвестный класс %q", cls)
	}
	tier := strings.TrimSpace(parts[1])
	if tier != "" {
		if _, ok := cfg.TierByName(tier); !ok {
			return "", "", fmt.Errorf("неизвестный tier %q", tier)
		}
	}
	return cls, tier, nil
}

// newLogger — structured-логи в JSON. В файл, если задан; иначе в stderr
// (systemd подхватит в журнал).
func newLogger(path string) *slog.Logger {
	var w io.Writer = os.Stderr
	if path != "" {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
		if err != nil {
			fmt.Fprintf(os.Stderr, "не открыть %s (%v), логи идут в stderr\n", path, err)
		} else {
			w = io.MultiWriter(os.Stderr, f)
		}
	}
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: slog.LevelInfo}))
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "ОШИБКА:", err)
	os.Exit(1)
}
