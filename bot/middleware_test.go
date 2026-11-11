package bot

import (
	"testing"

	tele "gopkg.in/telebot.v3"
)

func TestEveryCallbackIsAnsweredFirst(t *testing.T) {
	for _, data := range []string{"approve|777", "conn_sub", "conn_sub_qr", "conn_link|1", "conn_qr|1", "conn_tg_proxy",
		"conn_tg_proxy_qr", "conn_file", "status_refresh", "health_bcast", "health_bcast_no", "health_bcast_yes"} {
		t.Run(data, func(t *testing.T) {
			newTestDB(t)
			stubApply(t)
			setAdmin(t, 1)
			f := newFakeAPI(t)
			b := newTestBot(t, f)
			registerHandlers(b)
			// 42 — не админ и не пользователь: approve и health_* для него no-op,
			// conn_tg_proxy* возвращают ошибку до применения настроек — apply не ставится.
			b.ProcessUpdate(callbackUpdate(42, data))
			if m := f.methods(); len(m) == 0 || m[0] != "answerCallbackQuery" {
				t.Fatalf("вызовы: %v", m)
			}
		})
	}
}

func TestRepeatedPress_AnsweredButHandledOnce(t *testing.T) {
	newTestDB(t)
	f := newFakeAPI(t)
	b := newTestBot(t, f)
	registerHandlers(b)
	b.ProcessUpdate(callbackUpdate(42, "conn_file"))
	b.ProcessUpdate(callbackUpdate(42, "conn_file"))
	if n := len(f.params("answerCallbackQuery")); n != 2 {
		t.Fatalf("ответов на нажатие %d", n)
	}
	if n := len(f.texts(42)); n != 1 {
		t.Fatalf("сообщений %d", n)
	}
	b.ProcessUpdate(callbackUpdate(43, "conn_file")) // другой человек — не повтор
	if n := len(f.texts(43)); n != 1 {
		t.Fatalf("второму человеку сообщений %d", n)
	}
}

func TestStaleButton_StillAnswered(t *testing.T) {
	newTestDB(t)
	f := newFakeAPI(t)
	b := newTestBot(t, f)
	registerHandlers(b)
	b.ProcessUpdate(callbackUpdate(42, "button_removed_long_ago|5"))
	if m := f.methods(); len(m) != 1 || m[0] != "answerCallbackQuery" {
		t.Fatalf("вызовы: %v", m)
	}
}

func TestUpdateWithoutSender_DoesNotCrash(t *testing.T) {
	newTestDB(t)
	f := newFakeAPI(t)
	b := newTestBot(t, f)
	registerHandlers(b)
	b.ProcessUpdate(tele.Update{ID: 1, Message: &tele.Message{ID: 1, Text: "/start", Chat: &tele.Chat{ID: -100, Type: tele.ChatChannel}}})
	// тест проходит, если процесс не упал с паникой
}
