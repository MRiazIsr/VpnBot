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
