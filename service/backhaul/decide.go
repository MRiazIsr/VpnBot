package backhaul

import (
	"fmt"
	"time"
)

// ApplyProbe — чистый переход состояния одного tier'а по результату проверки.
//
// Гистерезис здесь и только здесь:
//   - падение фиксируется лишь после FailThreshold провалов подряд;
//   - подъём — лишь после RecoverThreshold успехов подряд.
//
// Один случайный таймаут не роняет маршрут; один случайный успех на умирающем
// канале не возвращает его в строй.
func ApplyProbe(now time.Time, m MonitorConfig, ts TierState, res ProbeResult) TierState {
	if res.OK {
		ts.ConsecOK++
		ts.ConsecFail = 0
		ts.LastOK = now
		ts.LastReason = ""
		ts.LastDownBps = res.DownBps
		ts.LastUpBps = res.UpBps
		if !ts.Healthy && ts.ConsecOK >= m.RecoverThreshold {
			ts.Healthy = true
			ts.HealthySince = now
		}
		if ts.Healthy && ts.HealthySince.IsZero() {
			ts.HealthySince = now
		}
		return ts
	}

	ts.ConsecFail++
	ts.ConsecOK = 0
	ts.LastFail = now
	ts.LastReason = res.Phase + ": " + res.Reason
	if ts.Healthy && ts.ConsecFail >= m.FailThreshold {
		ts.Healthy = false
		ts.HealthySince = time.Time{}
	}
	return ts
}

// Decision — что монитор намерен сделать с одним selector'ом.
type Decision struct {
	Class ServiceClass `json:"class"`
	From  string       `json:"from"`
	To    string       `json:"to"`
	// Switch — надо ли реально дёргать API sing-box.
	Switch bool `json:"switch"`
	// Kind: failover | recovery | init | none
	Kind   string `json:"kind"`
	Reason string `json:"reason"`
	// Held — если переключение назрело, но заблокировано hold-down/стабилизацией.
	Held bool `json:"held,omitempty"`
}

// Decide — чистая функция выбора маршрута для одного класса.
//
// Правила:
//  1. Ручной пин (Forced) сильнее всего — автоматика молчит.
//  2. Целевой tier = самый приоритетный из здоровых (primary → secondary → emergency).
//  3. Если текущий tier мёртв, уход вниз происходит немедленно: hold-down не
//     держит пользователей на заведомо нерабочем канале (демпфирование уже
//     обеспечено FailThreshold).
//  4. Возврат вверх (на более приоритетный) требует одновременно:
//     — прошёл hold-down с прошлого переключения;
//     — целевой tier непрерывно здоров не меньше StableBeforeReturnSec.
//     Это и есть анти-флаппинг: подмигивающий primary не будет дёргать selector.
//  5. Если здоровых нет вообще — не трогаем ничего, остаёмся где были.
func Decide(now time.Time, cfg *Config, cls ServiceClass, cs ClassState) Decision {
	d := Decision{Class: cls, From: cs.Active, Kind: "none"}
	tiers := cfg.SortedTiers()

	if cs.Forced != "" {
		d.To = cs.Forced
		d.Reason = "ручной пин: " + cs.Forced
		if cs.Active != cs.Forced {
			d.Switch = true
			d.Kind = "forced"
		}
		return d
	}

	if cs.Active == "" {
		// Холодный старт: встаём на самый приоритетный из здоровых, а если
		// проверок ещё не было — на primary.
		target := tiers[0].Name
		for _, t := range tiers {
			if cs.Tiers[t.Name].Healthy {
				target = t.Name
				break
			}
		}
		d.To, d.Switch, d.Kind = target, true, "init"
		d.Reason = "инициализация"
		return d
	}

	// Активный tier мог быть отключён правкой конфига (например, резидентный
	// узел ещё не появился). Тогда уходим с него сразу, не дожидаясь hold-down:
	// адаптера под ним больше нет.
	stillEnabled := false
	for _, t := range tiers {
		if t.Name == cs.Active {
			stillEnabled = true
		}
	}
	if !stillEnabled {
		target := tiers[0].Name
		for _, t := range tiers {
			if cs.Tiers[t.Name].Healthy {
				target = t.Name
				break
			}
		}
		d.To, d.Switch, d.Kind = target, true, "failover"
		d.Reason = fmt.Sprintf("tier %s отключён в конфиге, уходим на %s", cs.Active, target)
		return d
	}

	var best string
	var bestRank int
	for _, t := range tiers {
		if cs.Tiers[t.Name].Healthy {
			best, bestRank = t.Name, t.Rank
			break
		}
	}
	activeRank := 1 << 30
	for _, t := range tiers {
		if t.Name == cs.Active {
			activeRank = t.Rank
		}
	}
	activeHealthy := cs.Tiers[cs.Active].Healthy

	if best == "" {
		d.To = cs.Active
		d.Reason = "здоровых backhaul'ов нет — остаёмся на текущем"
		return d
	}
	if best == cs.Active {
		d.To = cs.Active
		d.Reason = "текущий backhaul остаётся лучшим"
		return d
	}

	d.To = best
	if bestRank > activeRank {
		// Уходим вниз по приоритету — это failover.
		if activeHealthy {
			// Текущий жив, но перестал быть лучшим — такого быть не должно,
			// потому что best берётся по возрастанию rank. Страхуемся.
			d.To = cs.Active
			d.Reason = "текущий жив и приоритетнее — не понижаемся"
			return d
		}
		d.Switch, d.Kind = true, "failover"
		d.Reason = fmt.Sprintf("%s мёртв (%s), уходим на %s",
			cs.Active, cs.Tiers[cs.Active].LastReason, best)
		return d
	}

	// bestRank < activeRank — возврат вверх.
	d.Kind = "recovery"
	holdLeft := time.Duration(cfg.Monitor.HoldDownSec)*time.Second - now.Sub(cs.LastSwitchAt)
	if !cs.LastSwitchAt.IsZero() && holdLeft > 0 {
		d.Held = true
		d.Reason = fmt.Sprintf("%s здоров, но hold-down ещё %s", best, holdLeft.Truncate(time.Second))
		d.To = cs.Active
		return d
	}
	hs := cs.Tiers[best].HealthySince
	stableFor := now.Sub(hs)
	need := time.Duration(cfg.Monitor.StableBeforeReturnSec) * time.Second
	if hs.IsZero() || stableFor < need {
		d.Held = true
		d.Reason = fmt.Sprintf("%s здоров лишь %s из требуемых %s",
			best, stableFor.Truncate(time.Second), need)
		d.To = cs.Active
		return d
	}
	d.Switch = true
	d.Reason = fmt.Sprintf("%s стабильно здоров %s — возвращаемся", best, stableFor.Truncate(time.Second))
	return d
}
