package bot

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"vpnbot/database"
)

// TestMain делает настоящие перегенерации недостижимыми из любого теста пакета:
// они пишут в /etc и запускают systemctl.
func TestMain(m *testing.M) {
	safe := func() error { return errors.New("apply called in a test without stubApply") }
	applyFull, applyTelemt = safe, safe
	os.Exit(m.Run())
}

// adminEdits возвращает editMessageText, адресованные сообщению msgID админа.
func adminEdits(f *fakeAPI, msgID int) (texts []string) {
	for _, p := range f.params("editMessageText") {
		if sameChat(p["chat_id"], 1) && fmt.Sprint(p["message_id"]) == fmt.Sprint(msgID) {
			texts = append(texts, fmt.Sprint(p["text"]))
		}
	}
	return texts
}

func countContaining(list []string, sub string) int {
	n := 0
	for _, s := range list {
		if strings.Contains(s, sub) {
			n++
		}
	}
	return n
}

// Каждый тест обязан дождаться завершения поставленных им запросов на применение до
// возврата: рабочая горутина читает applyFull/applyTelemt лениво, а cleanup
// возвращает настоящие функции.
//
// stubApply подменяет applyFull и applyTelemt счётчиками: настоящие
// перегенерации пишут в /etc и запускают systemctl.
func stubApply(t *testing.T) (full, telemt *atomic.Int32) {
	t.Helper()
	full, telemt = new(atomic.Int32), new(atomic.Int32)
	oldFull, oldTelemt := applyFull, applyTelemt
	applyFull = func() error { full.Add(1); return nil }
	applyTelemt = func() error { telemt.Add(1); return nil }
	t.Cleanup(func() { applyFull, applyTelemt = oldFull, oldTelemt })
	return full, telemt
}

func setAdmin(t *testing.T, id int64) {
	t.Helper()
	old := AdminID
	AdminID = id
	t.Cleanup(func() { AdminID = old })
}

func TestApplier_RequestDoesNotBlock(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	a := newApplier(func() error { <-release; return nil }, func() error { return nil })
	done := make(chan struct{})
	go func() { a.request(true, nil); a.request(true, nil); close(done) }()
	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("request заблокировался")
	}
}

func TestApplier_CoalescesBurst(t *testing.T) {
	var full, afters atomic.Int32
	started, release := make(chan struct{}, 8), make(chan struct{})
	a := newApplier(func() error { full.Add(1); started <- struct{}{}; <-release; return nil }, func() error { return nil })
	a.request(true, func(error) { afters.Add(1) })
	<-started // первый проход идёт
	for i := 0; i < 5; i++ {
		a.request(true, func(error) { afters.Add(1) })
	}
	close(release)
	eventually(t, func() bool { return afters.Load() == 6 })
	if full.Load() != 2 {
		t.Fatalf("full=%d, ждали 2", full.Load())
	}
}

func TestApplier_TelemtOnlyBatchSkipsFull(t *testing.T) {
	var full, telemt atomic.Int32
	a := newApplier(func() error { full.Add(1); return nil }, func() error { telemt.Add(1); return nil })
	done := make(chan struct{})
	a.request(false, func(error) { close(done) })
	<-done
	if full.Load() != 0 || telemt.Load() != 1 {
		t.Fatalf("full=%d telemt=%d", full.Load(), telemt.Load())
	}
}

func TestApplier_PassesErrorAndSurvivesPanic(t *testing.T) {
	var mode atomic.Value
	mode.Store("err")
	a := newApplier(func() error {
		if mode.Load() == "panic" {
			panic("boom")
		}
		return errors.New("reload failed")
	}, func() error { return nil })
	got := make(chan error, 1)
	a.request(true, func(err error) { got <- err })
	if err := <-got; err == nil || err.Error() != "reload failed" {
		t.Fatalf("err=%v", err)
	}
	mode.Store("panic")
	a.request(true, func(err error) { got <- err })
	if err := <-got; err == nil || !strings.Contains(err.Error(), "panic") {
		t.Fatalf("err=%v", err)
	}
	panicked := make(chan struct{})
	a.request(true, func(error) { close(panicked); panic("after boom") })
	<-panicked
	mode.Store("err")
	a.request(true, func(err error) { got <- err })
	if err := <-got; err == nil {
		t.Fatal("после паники рабочая горутина умерла")
	}
}

func TestApprove_ReturnsBeforeApplyAndNotifiesAfter(t *testing.T) {
	newTestDB(t)
	f := newFakeAPI(t)
	b := newTestBot(t, f)
	registerHandlers(b)
	setAdmin(t, 1)
	stubApply(t)
	release := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	applyFull = func() error { <-release; return nil }

	b.ProcessUpdate(callbackUpdate(1, "approve|777")) // Synchronous: вернулся — обработчик не ждал
	var u database.User
	if database.DB.Where("telegram_id = ?", 777).First(&u).Error != nil || u.Status != "active" {
		t.Fatal("пользователь не создан")
	}
	if len(f.texts(777)) != 0 {
		t.Fatal("человек оповещён до применения настроек")
	}
	if got := f.texts(1); len(got) == 0 || !strings.Contains(got[len(got)-1], "применяю настройки") {
		t.Fatalf("админу: %q", got)
	}
	close(release)
	eventually(t, func() bool { return len(f.texts(777)) == 1 })
	eventually(t, func() bool { return countContaining(adminEdits(f, 10), "одобрен.") == 1 })
	if n := len(f.params("sendMessage")); n != 1 { // только поздравление человеку
		t.Fatalf("sendMessage=%d, ждали 1: итог админу должен быть правкой, а не новым сообщением", n)
	}
	if got := f.params("sendMessage")[0]["chat_id"]; !sameChat(got, 777) {
		t.Fatalf("sendMessage ушёл не человеку: %v", got)
	}
}

func TestApprove_ImmediateSecondPressIsDropped(t *testing.T) {
	newTestDB(t)
	f := newFakeAPI(t)
	b := newTestBot(t, f)
	registerHandlers(b)
	setAdmin(t, 1)
	full, _ := stubApply(t)
	b.ProcessUpdate(callbackUpdate(1, "approve|777"))
	b.ProcessUpdate(callbackUpdate(1, "approve|777"))
	// Ждём, пока отработает применение первого нажатия.
	eventually(t, func() bool {
		return len(f.texts(777)) == 1 && countContaining(adminEdits(f, 10), "одобрен.") == 1 && full.Load() == 1
	})
	if n := len(f.params("answerCallbackQuery")); n != 2 {
		t.Fatalf("ответов на нажатие %d, ждали 2", n)
	}
	if got := countContaining(adminEdits(f, 10), "уже добавлен"); got != 0 {
		t.Fatalf("«уже добавлен» %d раз: повтор не отброшен", got)
	}
	if got := len(f.texts(777)); got != 1 {
		t.Fatalf("человеку %d сообщений, ждали 1", got)
	}
	var n int64
	database.DB.Model(&database.User{}).Where("telegram_id = ?", 777).Count(&n)
	if n != 1 {
		t.Fatalf("пользователей %d", n)
	}
}

func TestApprove_AlreadyAddedUser(t *testing.T) {
	newTestDB(t)
	f := newFakeAPI(t)
	b := newTestBot(t, f)
	registerHandlers(b)
	setAdmin(t, 1)
	full, telemt := stubApply(t)
	u := database.User{Username: "user_777", UUID: "u-777", TelegramID: 777, Status: "active"}
	if err := database.DB.Create(&u).Error; err != nil {
		t.Fatal(err)
	}
	b.ProcessUpdate(callbackUpdate(1, "approve|777"))
	if got := countContaining(adminEdits(f, 10), "уже добавлен"); got != 1 {
		t.Fatalf("«уже добавлен» %d раз, ждали 1", got)
	}
	if len(f.texts(777)) != 0 {
		t.Fatal("человеку что-то отправлено")
	}
	if full.Load() != 0 || telemt.Load() != 0 {
		t.Fatalf("применение запрошено: full=%d telemt=%d", full.Load(), telemt.Load())
	}
}

func TestApprove_ApplyFailureIsReportedToAdminNotUser(t *testing.T) {
	newTestDB(t)
	f := newFakeAPI(t)
	b := newTestBot(t, f)
	registerHandlers(b)
	setAdmin(t, 1)
	stubApply(t)
	applyFull = func() error { return errors.New("sing-box check failed") }
	b.ProcessUpdate(callbackUpdate(1, "approve|777"))
	eventually(t, func() bool {
		g := f.texts(1)
		return len(g) > 0 && strings.Contains(g[len(g)-1], "настройки не применились: sing-box check failed")
	})
	if len(f.texts(777)) != 0 {
		t.Fatal("человек оповещён, хотя настройки не применились")
	}
}

func TestApprove_NotAdminIsIgnored(t *testing.T) {
	newTestDB(t)
	f := newFakeAPI(t)
	b := newTestBot(t, f)
	registerHandlers(b)
	setAdmin(t, 1)
	full, _ := stubApply(t)
	b.ProcessUpdate(callbackUpdate(42, "approve|777"))
	var n int64
	database.DB.Model(&database.User{}).Count(&n)
	if n != 0 || full.Load() != 0 {
		t.Fatalf("users=%d full=%d", n, full.Load())
	}
}

// seedTelemt создаёт активного пользователя 42 и включённый telemt.
func seedTelemt(t *testing.T, withSecret bool) {
	t.Helper()
	u := database.User{Username: "user_42", UUID: "u-42", TelegramID: 42, Status: "active"}
	if err := database.DB.Create(&u).Error; err != nil {
		t.Fatal(err)
	}
	cfg := database.TelemetConfig{Enabled: true, Port: 443}
	if err := database.DB.Create(&cfg).Error; err != nil {
		t.Fatal(err)
	}
	if withSecret {
		tu := database.TelemetUser{UserID: u.ID, TelemetConfigID: cfg.ID, Label: u.Username, Secret: "00112233445566778899aabbccddeeff"}
		if err := database.DB.Create(&tu).Error; err != nil {
			t.Fatal(err)
		}
	}
}

func TestTgProxy_NewSecretDefersLinkUntilReload(t *testing.T) {
	newTestDB(t)
	f := newFakeAPI(t)
	b := newTestBot(t, f)
	registerHandlers(b)
	stubApply(t)
	seedTelemt(t, false)
	release := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	applyTelemt = func() error { <-release; return nil }

	b.ProcessUpdate(callbackUpdate(42, "conn_tg_proxy"))
	if got := f.texts(42); len(got) != 1 || !strings.Contains(got[0], "Готовлю ссылку") {
		t.Fatalf("got %q", got)
	}
	close(release)
	eventually(t, func() bool {
		g := f.texts(42)
		return len(g) == 2 && strings.Contains(g[1], "Основная ссылка")
	})
}

func TestTgProxy_ExistingSecretAnswersAtOnce(t *testing.T) {
	newTestDB(t)
	f := newFakeAPI(t)
	b := newTestBot(t, f)
	registerHandlers(b)
	_, telemt := stubApply(t)
	seedTelemt(t, true)
	b.ProcessUpdate(callbackUpdate(42, "conn_tg_proxy"))
	if got := f.texts(42); len(got) != 1 || !strings.Contains(got[0], "Основная ссылка") {
		t.Fatalf("got %q", got)
	}
	if telemt.Load() != 0 {
		t.Fatal("лишняя перезагрузка telemt")
	}
}

func TestTgProxyQR_NewSecretSendsPhotoOnlyAfterReload(t *testing.T) {
	newTestDB(t)
	f := newFakeAPI(t)
	b := newTestBot(t, f)
	registerHandlers(b)
	stubApply(t)
	seedTelemt(t, false)
	release := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	applyTelemt = func() error { <-release; return nil }

	b.ProcessUpdate(callbackUpdate(42, "conn_tg_proxy_qr"))
	if got := f.texts(42); len(got) != 1 || !strings.Contains(got[0], "Готовлю ссылку") {
		t.Fatalf("got %q", got)
	}
	if n := len(f.params("sendPhoto")); n != 0 {
		t.Fatalf("QR отправлен до применения настроек: %d", n)
	}
	close(release)
	eventually(t, func() bool { return len(f.params("sendPhoto")) == 1 })
}
