package logredact

import (
	"bytes"
	"log"
	"strings"
	"testing"

	tele "gopkg.in/telebot.v3"
)

const tok = "123456789:FAKE_fake_FAKE_fake_FAKE_fake_FAKE_0" // выдуманный, по форме как настоящий

func TestRedact_ExactSecret(t *testing.T) {
	in := `Post "https://api.telegram.org/bot` + tok + `/getMe": dial tcp: i/o timeout`
	want := `Post "https://api.telegram.org/bot<redacted>/getMe": dial tcp: i/o timeout`
	if got := Redact(in, tok); got != want {
		t.Fatalf("got %q", got)
	}
}

func TestRedact_TokenShapeWithoutKnownSecret(t *testing.T) {
	got := Redact("x /bot" + tok + "/sendMessage y") // секрет не передан
	if strings.Contains(got, tok) || !strings.Contains(got, "/bot<redacted>/sendMessage") {
		t.Fatalf("got %q", got)
	}
}

func TestRedact_EmptyAndShortSecretsIgnored(t *testing.T) {
	if got := Redact("hello world", "", "lo"); got != "hello world" {
		t.Fatalf("got %q", got)
	}
}

func TestWriter_ReportsOriginalLength(t *testing.T) {
	var buf bytes.Buffer
	line := []byte("err: bot" + tok + " failed\n")
	n, err := NewWriter(&buf, tok).Write(line)
	if n != len(line) || err != nil {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if strings.Contains(buf.String(), tok) {
		t.Fatal("token leaked")
	}
}

func TestWriter_RealTelebotError(t *testing.T) {
	_, err := tele.NewBot(tele.Settings{URL: "http://127.0.0.1:1", Token: tok})
	if err == nil || !strings.Contains(err.Error(), tok) {
		t.Fatalf("ожидалась ошибка с токеном, got %v", err)
	}
	var buf bytes.Buffer
	log.New(NewWriter(&buf, tok), "", 0).Println("bot:", err)
	if strings.Contains(buf.String(), tok) {
		t.Fatal("token leaked")
	}
}
