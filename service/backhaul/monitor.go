package backhaul

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// Switcher — то, через что монитор переключает маршрут. Интерфейс нужен, чтобы
// логику failover можно было прогнать в тестах без sing-box.
type Switcher interface {
	Selected(ctx context.Context, selector string) (string, error)
	Select(ctx context.Context, selector, member string) error
}

// Prober — источник результатов проверки. Тоже за интерфейсом: в тестах
// подменяется, в бою — реальный HTTP через SOCKS5.
type Prober interface {
	Probe(ctx context.Context, cfg ProbeConfig, tier Tier, cls ServiceClass) ProbeResult
}

type liveProber struct{}

func (liveProber) Probe(ctx context.Context, cfg ProbeConfig, tier Tier, cls ServiceClass) ProbeResult {
	return Probe(ctx, cfg, tier, cls)
}

// Monitor — активный health-checker с гистерезисом.
type Monitor struct {
	cfg    *Config
	sw     Switcher
	prober Prober
	log    *slog.Logger
	// DryRun — считать и логировать решения, но не переключать.
	DryRun bool

	mu    sync.Mutex
	state *State
}

// NewMonitor собирает монитор с боевыми зависимостями.
func NewMonitor(cfg *Config, log *slog.Logger) (*Monitor, error) {
	cc, err := NewClashClient(cfg.ClashAPI.Listen, cfg.ClashAPI.Secret)
	if err != nil {
		return nil, err
	}
	st, err := LoadState(cfg.StatePath, cfg.ClassesInUse(), cfg.Tiers)
	if err != nil {
		log.Warn("состояние загружено с ошибкой", "err", err)
	}
	return &Monitor{cfg: cfg, sw: cc, prober: liveProber{}, log: log, state: st}, nil
}

// NewMonitorWith — конструктор для тестов и для нестандартных зависимостей.
func NewMonitorWith(cfg *Config, sw Switcher, p Prober, st *State, log *slog.Logger) *Monitor {
	return &Monitor{cfg: cfg, sw: sw, prober: p, log: log, state: st}
}

// State возвращает копию текущего состояния.
func (m *Monitor) State() State {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := State{Version: m.state.Version, UpdatedAt: m.state.UpdatedAt,
		Classes: map[ServiceClass]ClassState{}}
	for cls, cs := range m.state.Classes {
		tiers := map[string]TierState{}
		for k, v := range cs.Tiers {
			tiers[k] = v
		}
		cs.Tiers = tiers
		cp.Classes[cls] = cs
	}
	return cp
}

// Sync приводит selector'ы sing-box в соответствие с сохранённым состоянием.
//
// Направление здесь принципиально. При рестарте sing-box каждый selector
// молча возвращается на свой `default`, то есть на самый приоритетный
// backhaul — в том числе на тот, который мы ровно перед этим признали
// мёртвым. Если бы монитор просто «подхватывал» то, что видит, каждый
// рестарт relay заново ронял бы пользователей на сломанный маршрут и держал
// их там до накопления FailThreshold провалов.
//
// Поэтому авторитет — сохранённое состояние: оно переустанавливается в
// sing-box. Значение из sing-box принимается только когда своего решения ещё
// нет (первый запуск).
func (m *Monitor) Sync(ctx context.Context) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, cls := range m.cfg.ClassesInUse() {
		sel := SelectorTag(cls)
		member, err := m.sw.Selected(ctx, sel)
		if err != nil {
			m.log.Warn("не удалось прочитать selector", "selector", sel, "err", err)
			continue
		}
		live := tierFromOutboundTag(m.cfg, member, cls)
		cs := m.state.Classes[cls]

		want := cs.Active
		if cs.Forced != "" {
			want = cs.Forced
		}
		if want == "" {
			// Своего решения нет — принимаем то, что уже стоит.
			if live != "" {
				cs.Active = live
				m.state.Classes[cls] = cs
			}
			continue
		}
		if _, ok := m.cfg.TierByName(want); !ok {
			continue
		}
		// Восстанавливать имеет смысл только на включённый tier.
		enabled := false
		for _, t := range m.cfg.SortedTiers() {
			if t.Name == want {
				enabled = true
			}
		}
		if !enabled || want == live {
			continue
		}
		t, _ := m.cfg.TierByName(want)
		if m.DryRun {
			m.log.Info("DRY-RUN: восстановление selector'а пропущено",
				"selector", sel, "live", live, "want", want)
			continue
		}
		if err := m.sw.Select(ctx, sel, t.OutboundTag(cls)); err != nil {
			m.log.Error("не удалось восстановить selector после рестарта",
				"selector", sel, "want", want, "err", err)
			continue
		}
		cs.Active = want
		m.state.Classes[cls] = cs
		m.log.Warn("selector восстановлен в сохранённое положение",
			"selector", sel, "было", live, "стало", want)
	}
}

// tierFromOutboundTag — обратное преобразование "bh-primary-vless" → "primary".
func tierFromOutboundTag(cfg *Config, tag string, cls ServiceClass) string {
	for _, t := range cfg.Tiers {
		if t.OutboundTag(cls) == tag {
			return t.Name
		}
	}
	return ""
}

// Tick — один цикл: проверить все backhaul'ы, обновить состояние, принять и
// применить решения. Возвращает решения по каждому классу.
func (m *Monitor) Tick(ctx context.Context) []Decision {
	now := time.Now()
	classes := m.cfg.ClassesInUse()
	tiers := m.cfg.SortedTiers()

	// Проверки идут параллельно: последовательный обход трёх backhaul'ов на
	// двух классах с таймаутом 15 с мог бы занять полторы минуты, а интервал
	// опроса — 20 с.
	type key struct {
		cls  ServiceClass
		tier string
	}
	results := make(map[key]ProbeResult, len(classes)*len(tiers))
	var wg sync.WaitGroup
	var rmu sync.Mutex
	for _, cls := range classes {
		for _, t := range tiers {
			wg.Add(1)
			go func(cls ServiceClass, t Tier) {
				defer wg.Done()
				res := m.prober.Probe(ctx, m.cfg.Probe, t, cls)
				rmu.Lock()
				results[key{cls, t.Name}] = res
				rmu.Unlock()
			}(cls, t)
		}
	}
	wg.Wait()

	m.mu.Lock()
	defer m.mu.Unlock()

	// Ручной пин ставится отдельной командой CLI, которая пишет его в файл
	// состояния. Перечитываем перед принятием решений, иначе демон затрёт
	// пин своим Save() и автоматика уведёт маршрут обратно.
	m.reloadForcedLocked()

	decisions := make([]Decision, 0, len(classes))
	for _, cls := range classes {
		cs := m.state.Classes[cls]
		if cs.Tiers == nil {
			cs.Tiers = map[string]TierState{}
		}
		for _, t := range tiers {
			res := results[key{cls, t.Name}]
			before := cs.Tiers[t.Name]
			after := ApplyProbe(now, m.cfg.Monitor, before, res)
			cs.Tiers[t.Name] = after

			m.log.Info("probe",
				"class", string(cls), "tier", t.Name, "ok", res.OK,
				"phase", res.Phase, "reason", res.Reason,
				"down_bytes", res.DownBytes, "up_bytes", res.UpBytes,
				"down_bps", res.DownBps, "up_bps", res.UpBps,
				"total_ms", res.TotalMs,
				"consec_ok", after.ConsecOK, "consec_fail", after.ConsecFail,
				"healthy", after.Healthy)
			if before.Healthy != after.Healthy {
				m.log.Warn("изменилось здоровье backhaul'а",
					"class", string(cls), "tier", t.Name,
					"healthy", after.Healthy, "reason", after.LastReason)
			}
		}
		m.state.Classes[cls] = cs

		d := Decide(now, m.cfg, cls, cs)
		decisions = append(decisions, d)

		if !d.Switch {
			if d.Held {
				m.log.Info("переключение придержано", "class", string(cls),
					"kind", d.Kind, "want", d.To, "reason", d.Reason)
			}
			continue
		}
		if m.DryRun {
			m.log.Info("DRY-RUN: переключение не выполняется",
				"class", string(cls), "kind", d.Kind, "from", d.From, "to", d.To, "reason", d.Reason)
			continue
		}
		if err := m.applyLocked(ctx, cls, d, now); err != nil {
			m.log.Error("переключение не удалось", "class", string(cls),
				"to", d.To, "err", err)
			continue
		}
	}

	if err := m.state.Save(m.cfg.StatePath); err != nil {
		m.log.Error("не удалось сохранить состояние", "path", m.cfg.StatePath, "err", err)
	}
	return decisions
}

// reloadForcedLocked подтягивает с диска только поле Forced. Остальное
// состояние — авторитетно в памяти демона.
func (m *Monitor) reloadForcedLocked() {
	onDisk, err := LoadState(m.cfg.StatePath, m.cfg.ClassesInUse(), m.cfg.Tiers)
	if err != nil || onDisk == nil {
		return
	}
	for cls, diskCS := range onDisk.Classes {
		cs, ok := m.state.Classes[cls]
		if !ok {
			continue
		}
		if cs.Forced != diskCS.Forced {
			m.log.Warn("ручной пин изменён извне",
				"class", string(cls), "was", cs.Forced, "now", diskCS.Forced)
			cs.Forced = diskCS.Forced
			m.state.Classes[cls] = cs
		}
	}
}

// applyLocked дёргает API sing-box. Вызывается под m.mu.
func (m *Monitor) applyLocked(ctx context.Context, cls ServiceClass, d Decision, now time.Time) error {
	t, ok := m.cfg.TierByName(d.To)
	if !ok {
		return fmt.Errorf("неизвестный tier %q", d.To)
	}
	sel := SelectorTag(cls)
	if err := m.sw.Select(ctx, sel, t.OutboundTag(cls)); err != nil {
		return err
	}
	cs := m.state.Classes[cls]
	cs.Active = d.To
	cs.LastSwitchAt = now
	m.state.Classes[cls] = cs
	m.log.Warn("МАРШРУТ ПЕРЕКЛЮЧЁН",
		"class", string(cls), "kind", d.Kind,
		"from", d.From, "to", d.To, "selector", sel,
		"member", t.OutboundTag(cls), "reason", d.Reason)
	return nil
}

// Force — ручной пин класса на конкретный tier. tier="" снимает пин и
// возвращает автоматику.
func (m *Monitor) Force(ctx context.Context, cls ServiceClass, tier string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	cs, ok := m.state.Classes[cls]
	if !ok {
		return fmt.Errorf("класс %q не обслуживается", cls)
	}
	if tier == "" {
		cs.Forced = ""
		m.state.Classes[cls] = cs
		m.log.Warn("ручной пин снят, автоматика включена", "class", string(cls))
		return m.state.Save(m.cfg.StatePath)
	}
	t, ok := m.cfg.TierByName(tier)
	if !ok {
		return fmt.Errorf("неизвестный tier %q", tier)
	}
	cs.Forced = tier
	m.state.Classes[cls] = cs
	if !m.DryRun {
		if err := m.sw.Select(ctx, SelectorTag(cls), t.OutboundTag(cls)); err != nil {
			return err
		}
		cs.Active = tier
		cs.LastSwitchAt = time.Now()
		m.state.Classes[cls] = cs
	}
	m.log.Warn("РУЧНОЕ ПЕРЕКЛЮЧЕНИЕ", "class", string(cls), "tier", tier, "dry_run", m.DryRun)
	return m.state.Save(m.cfg.StatePath)
}

// Run — основной цикл. Завершается по отмене контекста.
func (m *Monitor) Run(ctx context.Context) error {
	m.Sync(ctx)
	tk := time.NewTicker(time.Duration(m.cfg.Monitor.IntervalSec) * time.Second)
	defer tk.Stop()

	m.log.Info("монитор запущен",
		"interval_sec", m.cfg.Monitor.IntervalSec,
		"fail_threshold", m.cfg.Monitor.FailThreshold,
		"recover_threshold", m.cfg.Monitor.RecoverThreshold,
		"hold_down_sec", m.cfg.Monitor.HoldDownSec,
		"stable_before_return_sec", m.cfg.Monitor.StableBeforeReturnSec,
		"dry_run", m.DryRun)

	m.Tick(ctx)
	for {
		select {
		case <-ctx.Done():
			m.log.Info("монитор остановлен")
			return ctx.Err()
		case <-tk.C:
			m.Tick(ctx)
		}
	}
}
