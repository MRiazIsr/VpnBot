package bot

import (
	"fmt"
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

// lastIncidentList holds the incident list captured just before the set is
// cleared on recovery, so the broadcast button can reference it later.
var (
	lastIncidentMu   sync.Mutex
	lastIncidentList = "сервисы подключения"
)

func setLastIncidentList(s string) {
	lastIncidentMu.Lock()
	lastIncidentList = s
	lastIncidentMu.Unlock()
}

func getLastIncidentList() string {
	lastIncidentMu.Lock()
	defer lastIncidentMu.Unlock()
	return lastIncidentList
}

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
		signals := health.Collect()
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
		setLastIncidentList(list)
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

// registerHealthHandlers wires the recovery broadcast button + its confirm.
// Called from bot.Start(). Only the admin may use them.
func registerHealthHandlers(b *tele.Bot, br *broadcaster) {
	b.Handle(&tele.Btn{Unique: "health_bcast"}, func(c tele.Context) error {
		if c.Sender().ID != AdminID {
			return nil
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
			return nil
		}
		return c.Edit("Рассылка отменена.")
	})

	b.Handle(&tele.Btn{Unique: "health_bcast_yes"}, func(c tele.Context) error {
		if c.Sender().ID != AdminID {
			return nil
		}
		// Получатели, текст и адрес админа берутся здесь: фоновая работа
		// не должна читать глобальные переменные.
		ids := broadcastRecipients()
		text := recoveryText()
		adminID := AdminID
		if !br.start(func() { broadcastRecovery(b, ids, adminID, text, time.Sleep) }) {
			return c.Edit("⏳ Рассылка уже идёт, дождитесь отчёта.")
		}
		return c.Edit("📢 Рассылаю…")
	})
}

func recoveryText() string {
	return fmt.Sprintf(
		"✅ Связь восстановлена. Были перебои с подключением %s — сейчас всё работает.",
		getLastIncidentList())
}

func broadcastRecovery(m messenger, ids []int64, adminID int64, text string, sleep func(time.Duration)) {
	sent, failed := sendToAll(m, ids, text, sleep)
	if adminID != 0 {
		_, _ = m.Send(&tele.User{ID: adminID},
			fmt.Sprintf("📊 Рассылка готова: отправлено %d, ошибок %d.", sent, failed))
	}
}
