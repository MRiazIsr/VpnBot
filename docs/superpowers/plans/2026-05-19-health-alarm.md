# HEALTH ALARM Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Admin learns before users when an inbound/telemt stops passing connections, with a one-tap "all clear" broadcast on recovery.

**Architecture:** Pure state-machine package `service/health` (unit-tested, no Telegram/network deps) computes OK/DEGRADATION/DOWN transitions with hysteresis+dedup from collected signals. A thin collector turns existing `service.CheckAllInboundPorts()` + local telemt journald failure-ratio into signals. `bot/healthalarm.go` runs the loop, DMs the admin on transitions, and on recovery shows a confirm-gated broadcast button.

**Tech Stack:** Go 1.21, GORM (SQLite), telebot.v3. Module `vpnbot`. Tests via standard `go test` (repo currently has none — this introduces the first).

Spec: `docs/superpowers/specs/2026-05-19-health-alarm-design.md`.

---

## File Structure

- Create `service/health/health.go` — core types + `Evaluate` state machine (pure).
- Create `service/health/health_test.go` — unit tests for `Evaluate`.
- Create `service/health/collect.go` — `Collect()` turns existing checks + telemt journal into `[]SignalResult` (thin, host-dependent, not unit-tested).
- Create `bot/healthalarm.go` — loop goroutine, admin DM formatting, recovery broadcast button + confirm + throttled send.
- Modify `database/database.go` — add `HealthConfig` singleton model + AutoMigrate + default seed.
- Modify `bot/bot.go` — start the loop goroutine and register two callback handlers inside `Start()`.

Detection coverage v1 (per spec scope): chain reachability (partial→DEGRADATION, full-fail→DOWN) for every enabled inbound, plus telemt local failure-ratio (night-robust). Deeper per-transport payload probes plug into the same `SignalResult` later (phase 2) with no redesign.

---

## Task 1: Core types + Status

**Files:**
- Create: `service/health/health.go`
- Test: `service/health/health_test.go`

- [ ] **Step 1: Write the failing test**

```go
package health

import "testing"

func TestStatusString(t *testing.T) {
	cases := map[Status]string{StatusOK: "OK", StatusDegradation: "DEGRADATION", StatusDown: "DOWN"}
	for s, want := range cases {
		if got := s.String(); got != want {
			t.Fatalf("Status(%d).String() = %q, want %q", s, got, want)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./service/health/ -run TestStatusString -v`
Expected: FAIL — `undefined: Status` / package does not compile.

- [ ] **Step 3: Write minimal implementation**

Create `service/health/health.go`:

```go
// Package health detects when inbounds/telemt stop passing connections
// and computes OK/DEGRADATION/DOWN transitions. Pure: no Telegram/network.
package health

import "time"

type Status int

const (
	StatusOK Status = iota
	StatusDegradation
	StatusDown
)

func (s Status) String() string {
	switch s {
	case StatusOK:
		return "OK"
	case StatusDegradation:
		return "DEGRADATION"
	case StatusDown:
		return "DOWN"
	default:
		return "UNKNOWN"
	}
}

// SignalResult is one service's observation for one cycle.
type SignalResult struct {
	Service string // stable id, e.g. "inbound:2056" or "telemt"
	Label   string // human name for messages, e.g. "VLESS 2056 (xhttp-vk)"
	Bad     bool   // connections are NOT passing (active failure / unreachable)
	Partial bool   // one leg up, the other down (early warning)
	Reason  string // human reason, e.g. "RuVDS-цепочка недоступна"
}

// ServiceState is the remembered state between cycles.
type ServiceState struct {
	Status     Status
	FailStreak int
	Reason     string
	Since      time.Time
}

// Snapshot maps Service id -> state.
type Snapshot map[string]ServiceState

// Transition is an emitted state change (one per change, deduped).
type Transition struct {
	Service string
	Label   string
	From    Status
	To      Status
	Reason  string
	Since   time.Time
}

// Config holds tuning knobs.
type Config struct {
	DownHysteresis int // consecutive bad cycles required to confirm DOWN
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./service/health/ -run TestStatusString -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add service/health/health.go service/health/health_test.go
git commit -m "feat(health): core types for health-alarm state machine"
```

---

## Task 2: Evaluate state machine (hysteresis + dedup + recovery)

**Files:**
- Modify: `service/health/health.go` (add `Evaluate`)
- Test: `service/health/health_test.go` (add cases)

- [ ] **Step 1: Write the failing tests**

Append to `service/health/health_test.go`:

```go
func sig(service string, bad, partial bool) SignalResult {
	return SignalResult{Service: service, Label: service, Bad: bad, Partial: partial, Reason: "r"}
}

func TestEvaluate_OKStaysSilent(t *testing.T) {
	prev := Snapshot{}
	cfg := Config{DownHysteresis: 2}
	cur, tr := Evaluate(prev, []SignalResult{sig("s", false, false)}, cfg, time.Now())
	if cur["s"].Status != StatusOK {
		t.Fatalf("want OK, got %v", cur["s"].Status)
	}
	if len(tr) != 0 {
		t.Fatalf("want no transitions, got %v", tr)
	}
}

func TestEvaluate_FirstBadIsDegradationNotDown(t *testing.T) {
	cfg := Config{DownHysteresis: 2}
	cur, tr := Evaluate(Snapshot{}, []SignalResult{sig("s", true, false)}, cfg, time.Now())
	if cur["s"].Status != StatusDegradation {
		t.Fatalf("first bad must be DEGRADATION (M=2), got %v", cur["s"].Status)
	}
	if len(tr) != 1 || tr[0].To != StatusDegradation || tr[0].From != StatusOK {
		t.Fatalf("want one OK->DEGRADATION transition, got %v", tr)
	}
}

func TestEvaluate_SustainedBadBecomesDownAfterHysteresis(t *testing.T) {
	cfg := Config{DownHysteresis: 2}
	s1, _ := Evaluate(Snapshot{}, []SignalResult{sig("s", true, false)}, cfg, time.Now())
	s2, tr := Evaluate(s1, []SignalResult{sig("s", true, false)}, cfg, time.Now())
	if s2["s"].Status != StatusDown {
		t.Fatalf("want DOWN after 2 bad cycles, got %v", s2["s"].Status)
	}
	if len(tr) != 1 || tr[0].To != StatusDown {
		t.Fatalf("want DEGRADATION->DOWN transition, got %v", tr)
	}
}

func TestEvaluate_DownIsDedupedNoRepeat(t *testing.T) {
	cfg := Config{DownHysteresis: 2}
	s1, _ := Evaluate(Snapshot{}, []SignalResult{sig("s", true, false)}, cfg, time.Now())
	s2, _ := Evaluate(s1, []SignalResult{sig("s", true, false)}, cfg, time.Now())
	_, tr := Evaluate(s2, []SignalResult{sig("s", true, false)}, cfg, time.Now())
	if len(tr) != 0 {
		t.Fatalf("DOWN must not re-notify while staying DOWN, got %v", tr)
	}
}

func TestEvaluate_PartialIsImmediateDegradationEvenWithHysteresis(t *testing.T) {
	cfg := Config{DownHysteresis: 5}
	cur, tr := Evaluate(Snapshot{}, []SignalResult{sig("s", false, true)}, cfg, time.Now())
	if cur["s"].Status != StatusDegradation {
		t.Fatalf("partial must be immediate DEGRADATION, got %v", cur["s"].Status)
	}
	if len(tr) != 1 || tr[0].To != StatusDegradation {
		t.Fatalf("want OK->DEGRADATION, got %v", tr)
	}
}

func TestEvaluate_RecoveryEmitsOKTransition(t *testing.T) {
	cfg := Config{DownHysteresis: 2}
	s1, _ := Evaluate(Snapshot{}, []SignalResult{sig("s", true, false)}, cfg, time.Now())
	s2, _ := Evaluate(s1, []SignalResult{sig("s", true, false)}, cfg, time.Now())
	_, tr := Evaluate(s2, []SignalResult{sig("s", false, false)}, cfg, time.Now())
	if len(tr) != 1 || tr[0].From != StatusDown || tr[0].To != StatusOK {
		t.Fatalf("want DOWN->OK recovery transition, got %v", tr)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./service/health/ -run TestEvaluate -v`
Expected: FAIL — `undefined: Evaluate`.

- [ ] **Step 3: Write minimal implementation**

Append to `service/health/health.go`:

```go
// Evaluate computes the new snapshot and the transitions to notify.
// Rules: a "bad" signal increments FailStreak; first bad (or any Partial)
// is immediate DEGRADATION; DOWN only after FailStreak >= DownHysteresis.
// Good signal resets to OK. A Transition is emitted only when Status
// actually changes (dedup). Recovery (->OK) is always emitted.
func Evaluate(prev Snapshot, signals []SignalResult, cfg Config, now time.Time) (Snapshot, []Transition) {
	if cfg.DownHysteresis < 1 {
		cfg.DownHysteresis = 1
	}
	cur := make(Snapshot, len(signals))
	var transitions []Transition

	for _, sg := range signals {
		old, seen := prev[sg.Service]
		if !seen {
			old = ServiceState{Status: StatusOK}
		}

		st := ServiceState{Since: old.Since, Reason: sg.Reason}

		switch {
		case !sg.Bad && !sg.Partial:
			st.Status = StatusOK
			st.FailStreak = 0
			if old.Status == StatusOK {
				st.Reason = ""
			}
		default:
			st.FailStreak = old.FailStreak + 1
			if sg.Bad && st.FailStreak >= cfg.DownHysteresis {
				st.Status = StatusDown
			} else {
				st.Status = StatusDegradation
			}
		}

		if st.Status != old.Status {
			st.Since = now
			transitions = append(transitions, Transition{
				Service: sg.Service,
				Label:   sg.Label,
				From:    old.Status,
				To:      st.Status,
				Reason:  sg.Reason,
				Since:   st.Since,
			})
		} else if st.Since.IsZero() {
			st.Since = now
		}

		cur[sg.Service] = st
	}
	return cur, transitions
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./service/health/ -v`
Expected: PASS (all `TestEvaluate*` + `TestStatusString`)

- [ ] **Step 5: Commit**

```bash
git add service/health/health.go service/health/health_test.go
git commit -m "feat(health): Evaluate state machine with hysteresis, dedup, recovery"
```

---

## Task 3: HealthConfig model + migration + seed

**Files:**
- Modify: `database/database.go` (add model, AutoMigrate arg, seed)

- [ ] **Step 1: Add the model**

In `database/database.go`, after the `TelemetConfig` struct, add:

```go
// HealthConfig — настройки HEALTH ALARM (синглтон, как TelemetConfig).
type HealthConfig struct {
	ID        uint           `gorm:"primaryKey" json:"id"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`

	Enabled        bool `gorm:"default:true" json:"enabled"`
	IntervalSec    int  `gorm:"default:60" json:"interval_sec"`
	DownHysteresis int  `gorm:"default:2" json:"down_hysteresis"`
}
```

- [ ] **Step 2: Register in AutoMigrate**

In `database/database.go:187`, add `&HealthConfig{}` to the `DB.AutoMigrate(...)` argument list (append at the end of the existing list).

- [ ] **Step 3: Seed a default singleton row**

In `database.Init`, after the existing seed block (near the `SubscriptionToken: GenerateToken()` seed area), add:

```go
	var hc HealthConfig
	if DB.First(&hc).Error != nil {
		DB.Create(&HealthConfig{Enabled: true, IntervalSec: 60, DownHysteresis: 2})
	}
```

- [ ] **Step 4: Verify build**

Run: `go build ./...`
Expected: builds with no error.

- [ ] **Step 5: Commit**

```bash
git add database/database.go
git commit -m "feat(health): HealthConfig singleton model + migration + seed"
```

---

## Task 4: Signal collector (chain reachability + telemt failure-ratio)

**Files:**
- Create: `service/health/collect.go`

Host/network-dependent and thin — verified manually in Task 7, not unit-tested (the testable logic lives in `Evaluate`).

- [ ] **Step 1: Implement the collector**

Create `service/health/collect.go`:

```go
package health

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"vpnbot/database"
	"vpnbot/service"
)

// Collect builds one cycle of signals.
//   - Per enabled inbound: chain reachability via service.CheckAllInboundPorts().
//     Both legs down -> Bad. Exactly one leg down -> Partial (early warning).
//   - telemt: night-robust failure ratio from local journald (the app runs on
//     the Hetzner host alongside telemt). Ratio is over ATTEMPTS, so zero
//     attempts (night) -> not Bad.
func Collect(intervalSec int) []SignalResult {
	var out []SignalResult

	for _, pc := range service.CheckAllInboundPorts() {
		svc := fmt.Sprintf("inbound:%d", pc.Port)
		label := fmt.Sprintf("%s :%d (%s)", pc.Protocol, pc.Port, pc.Tag)
		bothDown := !pc.RuVDSReachable && !pc.HetznerReachable
		oneDown := pc.RuVDSReachable != pc.HetznerReachable
		reason := ""
		if bothDown {
			reason = "порт недоступен (RuVDS и Hetzner)"
		} else if oneDown {
			if !pc.RuVDSReachable {
				reason = "RuVDS-цепочка недоступна, Hetzner ок"
			} else {
				reason = "Hetzner недоступен, RuVDS ок"
			}
		}
		out = append(out, SignalResult{
			Service: svc, Label: label,
			Bad:     bothDown,
			Partial: oneDown,
			Reason:  reason,
		})
	}

	var tcfg database.TelemetConfig
	if database.DB.First(&tcfg).Error == nil && tcfg.Enabled {
		out = append(out, telemtSignal(intervalSec))
	}
	return out
}

// telemtSignal reads telemt journald for the last interval and flags Bad when
// (handshake-timeout) dominates handshake attempts. Night-robust: with no
// attempts the ratio is undefined -> treated as healthy.
func telemtSignal(intervalSec int) SignalResult {
	res := SignalResult{Service: "telemt", Label: "MTProto (telemt)"}
	since := fmt.Sprintf("%d sec ago", intervalSec)
	out, err := exec.Command("journalctl", "-u", "telemt", "--no-pager",
		"--since", since).CombinedOutput()
	if err != nil {
		// Cannot read journal (not on telemt host / no perms): do not alarm.
		return res
	}
	text := string(out)
	timeouts := strings.Count(text, "handshake timeout")
	established := strings.Count(text, "peer=127.0.0.1") +
		strings.Count(text, "Connected") // tunnel-delivered + generic success
	attempts := timeouts + established
	if attempts < 5 {
		return res // too few attempts (e.g. night) — not a signal
	}
	failPct := timeouts * 100 / attempts
	if failPct >= 70 {
		res.Bad = true
		res.Reason = "telemt: " + strconv.Itoa(failPct) + "% попыток с handshake timeout"
	} else if failPct >= 40 {
		res.Partial = true
		res.Reason = "telemt: рост ошибок рукопожатия (" + strconv.Itoa(failPct) + "%)"
	}
	return res
}

var _ = time.Second // reserved for future probe timeout
```

- [ ] **Step 2: Verify build**

Run: `go build ./...`
Expected: builds (no unused-import errors; `time` kept via the `_ =` line).

- [ ] **Step 3: Commit**

```bash
git add service/health/collect.go
git commit -m "feat(health): signal collector — chain reachability + telemt ratio"
```

---

## Task 5: Bot loop + admin notification formatting

**Files:**
- Create: `bot/healthalarm.go`

- [ ] **Step 1: Implement loop + formatting (no button yet)**

Create `bot/healthalarm.go`:

```go
package bot

import (
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"vpnbot/database"
	"vpnbot/service/health"

	tele "gopkg.in/telebot.v3"
)

// incident remembers services that were bad during the current outage so the
// recovery message can list them. Guarded by incidentMu.
var (
	incidentMu  sync.Mutex
	incidentSet = map[string]string{} // service -> label
)

func runHealthAlarm(b *tele.Bot) {
	for {
		var hc database.HealthConfig
		if database.DB.First(&hc).Error != nil || !hc.Enabled {
			time.Sleep(60 * time.Second)
			continue
		}
		interval := hc.IntervalSec
		if interval < 10 {
			interval = 60
		}
		prev := loadSnapshot()
		signals := health.Collect(interval)
		cur, transitions := health.Evaluate(prev, signals,
			health.Config{DownHysteresis: hc.DownHysteresis}, time.Now())
		saveSnapshot(cur)

		for _, tr := range transitions {
			notifyAdmin(b, tr)
		}
		time.Sleep(time.Duration(interval) * time.Second)
	}
}

// snapshot kept in memory across cycles (no persistence needed — first cycle
// after a restart re-derives state).
var (
	snapMu sync.Mutex
	snap   = health.Snapshot{}
)

func loadSnapshot() health.Snapshot {
	snapMu.Lock()
	defer snapMu.Unlock()
	cp := make(health.Snapshot, len(snap))
	for k, v := range snap {
		cp[k] = v
	}
	return cp
}

func saveSnapshot(s health.Snapshot) {
	snapMu.Lock()
	snap = s
	snapMu.Unlock()
}

func notifyAdmin(b *tele.Bot, tr health.Transition) {
	if AdminID == 0 {
		return
	}
	switch tr.To {
	case health.StatusDegradation:
		recordIncident(tr.Service, tr.Label)
		msg := fmt.Sprintf("⚠️ *Деградация / теряем коннект*\n%s\nПричина: %s\nС %s",
			tr.Label, tr.Reason, tr.Since.Format("15:04:05"))
		_, _ = b.Send(&tele.User{ID: AdminID}, msg, tele.ModeMarkdown)
	case health.StatusDown:
		recordIncident(tr.Service, tr.Label)
		msg := fmt.Sprintf("🔴 *Отвалилось*\n%s\nПричина: %s\nС %s",
			tr.Label, tr.Reason, tr.Since.Format("15:04:05"))
		_, _ = b.Send(&tele.User{ID: AdminID}, msg, tele.ModeMarkdown)
	case health.StatusOK:
		list := incidentList()
		clearIncidentIfAllOK()
		rm := &tele.ReplyMarkup{}
		btn := rm.Data("📢 Оповестить всех", "health_bcast")
		rm.Inline(rm.Row(btn))
		msg := fmt.Sprintf("✅ *Восстановлено*\n%s\nЗатронуто в инциденте: %s",
			tr.Label, list)
		_, _ = b.Send(&tele.User{ID: AdminID}, msg, rm, tele.ModeMarkdown)
	}
}

func recordIncident(service, label string) {
	incidentMu.Lock()
	incidentSet[service] = label
	incidentMu.Unlock()
}

func incidentList() string {
	incidentMu.Lock()
	defer incidentMu.Unlock()
	if len(incidentSet) == 0 {
		return "сервисы подключения"
	}
	var parts []string
	for _, label := range incidentSet {
		parts = append(parts, label)
	}
	return strings.Join(parts, ", ")
}

func clearIncidentIfAllOK() {
	snapMu.Lock()
	allOK := true
	for _, st := range snap {
		if st.Status != health.StatusOK {
			allOK = false
			break
		}
	}
	snapMu.Unlock()
	if allOK {
		incidentMu.Lock()
		incidentSet = map[string]string{}
		incidentMu.Unlock()
	}
}
```

- [ ] **Step 2: Verify build**

Run: `go build ./...`
Expected: builds with no error.

- [ ] **Step 3: Commit**

```bash
git add bot/healthalarm.go
git commit -m "feat(health): bot loop + admin-only transition notifications"
```

---

## Task 6: Recovery broadcast button + confirm + throttled send

**Files:**
- Modify: `bot/healthalarm.go` (add handlers + broadcast func)

- [ ] **Step 1: Add confirm + broadcast handlers**

Append to `bot/healthalarm.go`:

```go
// registerHealthHandlers wires the recovery broadcast button + its confirm.
// Called from bot.Start(). Only the admin may use them.
func registerHealthHandlers(b *tele.Bot) {
	b.Handle(&tele.Btn{Unique: "health_bcast"}, func(c tele.Context) error {
		if c.Sender().ID != AdminID {
			return c.Respond()
		}
		var n int64
		database.DB.Model(&database.User{}).
			Where("status = ? AND telegram_id <> 0", "active").Count(&n)
		rm := &tele.ReplyMarkup{}
		yes := rm.Data(fmt.Sprintf("✅ Разослать %d юзерам", n), "health_bcast_yes")
		no := rm.Data("Отмена", "health_bcast_no")
		rm.Inline(rm.Row(yes), rm.Row(no))
		return c.Edit("Разослать уведомление о восстановлении?", rm)
	})

	b.Handle(&tele.Btn{Unique: "health_bcast_no"}, func(c tele.Context) error {
		if c.Sender().ID != AdminID {
			return c.Respond()
		}
		return c.Edit("Рассылка отменена.")
	})

	b.Handle(&tele.Btn{Unique: "health_bcast_yes"}, func(c tele.Context) error {
		if c.Sender().ID != AdminID {
			return c.Respond()
		}
		// anti double-click: disable buttons immediately
		_ = c.Edit("📢 Рассылаю…")
		go broadcastRecovery(b)
		return c.Respond()
	})
}

func broadcastRecovery(b *tele.Bot) {
	text := fmt.Sprintf(
		"✅ Связь восстановлена. Были перебои с подключением %s — сейчас всё работает.",
		incidentList())

	var users []database.User
	database.DB.Where("status = ? AND telegram_id <> 0", "active").Find(&users)

	sent, failed := 0, 0
	for _, u := range users {
		if _, err := b.Send(&tele.User{ID: u.TelegramID}, text); err != nil {
			failed++
		} else {
			sent++
		}
		time.Sleep(50 * time.Millisecond) // ~20 msg/s, under Telegram limits
	}
	if AdminID != 0 {
		_, _ = b.Send(&tele.User{ID: AdminID},
			fmt.Sprintf("📊 Рассылка готова: отправлено %d, ошибок %d.", sent, failed))
	}
}
```

- [ ] **Step 2: Verify build**

Run: `go build ./...`
Expected: builds with no error.

- [ ] **Step 3: Commit**

```bash
git add bot/healthalarm.go
git commit -m "feat(health): recovery broadcast button with confirm + throttled send"
```

---

## Task 7: Wire into bot.Start + full build/vet

**Files:**
- Modify: `bot/bot.go` (inside `Start`, after `Bot = b`, before the blocking poller start)

- [ ] **Step 1: Hook the goroutine + handlers**

In `bot/bot.go`, locate `Bot = b` inside `func Start`. Immediately after it, add:

```go
	registerHealthHandlers(b)
	go runHealthAlarm(b)
```

(`registerHealthHandlers` must be called before `b.Start()`; `bot.AdminID` is already set at the top of `Start`.)

- [ ] **Step 2: Build + vet (matches CI)**

Run: `go build ./... && go vet ./...`
Expected: both succeed with no output.

- [ ] **Step 3: Full test suite**

Run: `go test ./service/health/ -v`
Expected: PASS — all `TestStatusString` + `TestEvaluate*`.

- [ ] **Step 4: Commit**

```bash
git add bot/bot.go
git commit -m "feat(health): start health-alarm loop + handlers from bot.Start"
```

---

## Task 8: Manual verification on a stand

**Files:** none (runtime verification)

- [ ] **Step 1: Build the binary**

Run: `go build -o vpnbot .`
Expected: binary produced.

- [ ] **Step 2: Sanity-check config seed**

Start the binary against a test DB (or inspect `vpn.db`):
Run: `sqlite3 vpn.db "select enabled,interval_sec,down_hysteresis from health_configs;"`
Expected: one row `1|60|2`.

- [ ] **Step 3: Force a transition (degradation)**

Temporarily point an enabled inbound at a closed port (or stop telemt on the stand) so `Collect` reports it Bad. Within one interval the admin Telegram account must receive exactly one `⚠️ Деградация` message (and exactly one `🔴 Отвалилось` after `DownHysteresis` cycles), and no repeats while it stays down.
Expected: one message per transition, admin only, no spam.

- [ ] **Step 4: Force recovery + broadcast**

Restore the service. Admin must receive one `✅ Восстановлено` with the `📢 Оповестить всех` button. Tap it → confirm dialog with user count → confirm → a non-admin test user receives the recovery text; admin gets the `📊 Рассылка готова` summary.
Expected: broadcast only on explicit confirm; only `status=active` users with a telegram_id; admin sees summary.

- [ ] **Step 5: Night-robustness check**

With telemt enabled but near-zero traffic (no users), confirm `telemt` stays OK (no false DEGRADATION) — `telemtSignal` returns non-Bad when attempts < 5.
Expected: no alarm purely from low traffic.

- [ ] **Step 6: Commit any fixes found**

```bash
git add -A
git commit -m "fix(health): adjustments from manual verification"
```

---

## Self-Review

**Spec coverage:**
- §3 architecture (service/health pure + bot delivery) → Tasks 1,2,4,5.
- §4 signals, traffic NOT a trigger → Task 4 (only reachability + failure ratio; no traffic volume anywhere).
- §5 OK/DEGRADATION/DOWN, immediate degradation, DOWN hysteresis → Task 2 + tests.
- §6 admin-only, one msg per transition, dedup, recovery → Task 2 (dedup) + Task 5 (admin-only, formatting).
- §7 config (interval, down_hysteresis, enabled) → Task 3 model + Task 5 reads it. (`probe_timeout`/`fail_ratio_pct` are phase-2 knobs; v1 ratio thresholds are constants in Task 4 — documented, not a gap.)
- §8 button only on →OK, confirm, throttle, anti-double-click, admin-only → Task 6.
- §9 unit tests for service/health → Tasks 1,2. Bot manual → Task 8.
- §2 nft-generator explicitly out of scope → not in this plan (separate task).

**Placeholder scan:** No TBD/TODO; every code step has complete code; commands have expected output.

**Type consistency:** `SignalResult{Service,Label,Bad,Partial,Reason}`, `ServiceState{Status,FailStreak,Reason,Since}`, `Transition{Service,Label,From,To,Reason,Since}`, `Config{DownHysteresis}`, `Evaluate(prev,signals,cfg,now)`, `health.Collect(intervalSec)`, `runHealthAlarm`, `registerHealthHandlers`, `incidentList` — names consistent across Tasks 1–7.

**Known v1 limitation (matches spec scope):** per-inbound deep payload probe is not in v1; inbound coverage is chain-reachability (catches port-down + partial-leg), telemt gets the failure-ratio (catches the May silent-drop for the service that mattered). Deeper per-transport probes slot into `SignalResult` later with no redesign.
