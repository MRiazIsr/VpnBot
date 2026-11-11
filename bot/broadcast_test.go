package bot

import (
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
	"vpnbot/database"
)

const flood = `{"ok":false,"error_code":429,"description":"Too Many Requests: retry after 7","parameters":{"retry_after":7}}`
const blocked = `{"ok":false,"error_code":403,"description":"Forbidden: bot was blocked by the user"}`

// addUser создаёт пользователя с уникальными именем, UUID и токеном подписки.
func addUser(t *testing.T, telegramID int64, status string) {
	t.Helper()
	n := fmt.Sprintf("u%d-%s", telegramID, status)
	u := database.User{Username: n, UUID: "uuid-" + n, SubscriptionToken: "tok-" + n, TelegramID: telegramID, Status: status}
	if err := database.DB.Create(&u).Error; err != nil {
		t.Fatal(err)
	}
}

// holdRecipients: sendMessage всем, кроме chat_id=1 (админ), ждёт release.
// Освобождение — в t.Cleanup после newFakeAPI, чтобы упавший тест не вешал сервер.
func holdRecipients(t *testing.T, f *fakeAPI) (release func()) {
	t.Helper()
	ch := make(chan struct{})
	var once sync.Once
	release = func() { once.Do(func() { close(ch) }) }
	t.Cleanup(release)
	f.on("sendMessage", func(p map[string]any) (int, string) {
		if fmt.Sprint(p["chat_id"]) != "1" {
			<-ch
		}
		return 200, defaultMessageJSON
	})
	return release
}

func TestBroadcastRecipients_OnlyActiveWithTelegram(t *testing.T) {
	newTestDB(t)
	addUser(t, 11, "active")
	addUser(t, 0, "active")
	addUser(t, 13, "banned")
	addUser(t, 14, "expired")
	if got := broadcastRecipients(); !reflect.DeepEqual(got, []int64{11}) {
		t.Fatalf("got %v", got)
	}
}

func TestSendToAll_PausesAfterEachRecipient(t *testing.T) {
	f := newFakeAPI(t)
	b := newTestBot(t, f)
	var slept []time.Duration
	sent, failed := sendToAll(b, []int64{1, 2, 3}, "привет", func(d time.Duration) { slept = append(slept, d) })
	if sent != 3 || failed != 0 {
		t.Fatalf("sent=%d failed=%d", sent, failed)
	}
	if !reflect.DeepEqual(slept, []time.Duration{broadcastPause, broadcastPause, broadcastPause}) {
		t.Fatalf("slept=%v", slept)
	}
}

func TestSendToAll_RetriesAfterFlood(t *testing.T) {
	f := newFakeAPI(t)
	b := newTestBot(t, f)
	tries := 0
	f.on("sendMessage", func(p map[string]any) (int, string) { // получателю 2 первый раз — 429
		if fmt.Sprint(p["chat_id"]) == "2" {
			tries++
			if tries == 1 {
				return 200, flood
			}
		}
		return 200, defaultMessageJSON
	})
	var slept []time.Duration
	sent, failed := sendToAll(b, []int64{1, 2, 3}, "привет", func(d time.Duration) { slept = append(slept, d) })
	if sent != 3 || failed != 0 || tries != 2 {
		t.Fatalf("sent=%d failed=%d tries=%d", sent, failed, tries)
	}
	if !reflect.DeepEqual(slept, []time.Duration{broadcastPause, 7 * time.Second, broadcastPause, broadcastPause}) {
		t.Fatalf("slept=%v", slept)
	}
}

func TestSendToAll_GivesUpAfterThreeFloods(t *testing.T) {
	f := newFakeAPI(t)
	b := newTestBot(t, f)
	tries := 0
	f.on("sendMessage", func(p map[string]any) (int, string) {
		if fmt.Sprint(p["chat_id"]) == "2" {
			tries++
			return 200, flood
		}
		return 200, defaultMessageJSON
	})
	sent, failed := sendToAll(b, []int64{1, 2, 3}, "привет", func(time.Duration) {})
	if sent != 2 || failed != 1 || tries != broadcastAttempts {
		t.Fatalf("sent=%d failed=%d tries=%d", sent, failed, tries)
	}
}

func TestSendToAll_BlockedUserDoesNotStopOthers(t *testing.T) {
	f := newFakeAPI(t)
	b := newTestBot(t, f)
	f.on("sendMessage", func(p map[string]any) (int, string) {
		if fmt.Sprint(p["chat_id"]) == "1" {
			return 200, blocked
		}
		return 200, defaultMessageJSON
	})
	sent, failed := sendToAll(b, []int64{1, 2, 3}, "привет", func(time.Duration) {})
	if sent != 2 || failed != 1 {
		t.Fatalf("sent=%d failed=%d", sent, failed)
	}
}

func TestBroadcastCommand_RunsInBackgroundAndRefusesSecond(t *testing.T) {
	newTestDB(t)
	setAdmin(t, 1)
	addUser(t, 11, "active")
	addUser(t, 12, "active")
	f := newFakeAPI(t)
	release := holdRecipients(t, f)
	b := newTestBot(t, f)
	registerHandlers(b)
	b.ProcessUpdate(textUpdate(1, "/broadcast привет")) // Synchronous: вернулся — значит не ждал рассылку
	b.ProcessUpdate(textUpdate(1, "/broadcast ещё раз"))
	got := f.texts(1)
	if len(got) != 2 || !strings.Contains(got[0], "Рассылка запущена, получателей: 2") || !strings.Contains(got[1], "Рассылка уже идёт") {
		t.Fatalf("админу: %q", got)
	}
	release()
	eventually(t, func() bool { g := f.texts(1); return len(g) == 3 && strings.Contains(g[2], "Отправлено: 2") })
}

func TestHealthBroadcastYes_RefusedWhileBroadcastRuns(t *testing.T) {
	newTestDB(t)
	setAdmin(t, 1)
	addUser(t, 11, "active")
	addUser(t, 12, "active")
	f := newFakeAPI(t)
	release := holdRecipients(t, f)
	b := newTestBot(t, f)
	registerHandlers(b)
	b.ProcessUpdate(textUpdate(1, "/broadcast привет"))
	b.ProcessUpdate(callbackUpdate(1, "health_bcast_yes"))
	got := f.texts(1)
	if len(got) != 2 || !strings.Contains(got[1], "Рассылка уже идёт, дождитесь отчёта.") {
		t.Fatalf("админу: %q", got)
	}
	for _, id := range []int64{11, 12} {
		for _, s := range f.texts(id) {
			if strings.Contains(s, "Связь восстановлена") {
				t.Fatalf("получатель %d получил рассылку о восстановлении", id)
			}
		}
	}
	release()
	eventually(t, func() bool { g := f.texts(1); return len(g) == 3 && strings.Contains(g[2], "Отправлено: 2") })
}

func TestHealthBroadcastYes_SendsToActiveAndReports(t *testing.T) {
	newTestDB(t)
	setAdmin(t, 1)
	addUser(t, 11, "active")
	addUser(t, 12, "active")
	f := newFakeAPI(t)
	b := newTestBot(t, f)
	registerHandlers(b)
	b.ProcessUpdate(callbackUpdate(1, "health_bcast_yes"))
	eventually(t, func() bool {
		g := f.texts(1)
		return len(g) == 2 && strings.Contains(g[1], "Рассылка готова: отправлено 2, ошибок 0.")
	})
	for _, id := range []int64{11, 12} {
		got := f.texts(id)
		if len(got) != 1 || !strings.Contains(got[0], "✅ Связь восстановлена. Были перебои с подключением") {
			t.Fatalf("получатель %d: %q", id, got)
		}
	}
}
