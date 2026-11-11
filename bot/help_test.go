package bot

import (
	"strings"
	"testing"
)

// «Помощь» описывает Happ и те кнопки, которые в боте есть на самом деле.
func TestHelp_DescribesHappAndRealButtons(t *testing.T) {
	newTestDB(t)
	f := newFakeAPI(t)
	b := newTestBot(t, f)
	registerHandlers(b)

	b.ProcessUpdate(textUpdate(11, "🆘 Помощь"))

	got := f.texts(11)
	if len(got) != 1 {
		t.Fatalf("ждали одно сообщение, получили %d: %q", len(got), got)
	}
	for _, want := range []string{"Happ", "🔑 Подключиться", "⭐ Авто-подключение"} {
		if !strings.Contains(got[0], want) {
			t.Errorf("в помощи нет %q", want)
		}
	}
	for _, old := range []string{"Hiddify", "V2Box", "Streisand", `"Ссылка"`} {
		if strings.Contains(got[0], old) {
			t.Errorf("в помощи осталось старое: %q", old)
		}
	}
	if pm := f.params("sendMessage")[0]["parse_mode"]; pm != "Markdown" {
		t.Errorf("parse_mode = %v, ждали Markdown", pm)
	}
}

// Непарный символ разметки — и Telegram отклоняет сообщение целиком.
func TestHelpText_MarkdownIsBalanced(t *testing.T) {
	if n := strings.Count(helpText, "*"); n%2 != 0 {
		t.Errorf("нечётное число * в тексте помощи: %d", n)
	}
	for _, bad := range []string{"_", "`", "["} {
		if strings.Contains(helpText, bad) {
			t.Errorf("в тексте помощи есть %q — в старом Markdown это разметка", bad)
		}
	}
}
