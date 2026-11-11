package bot

import (
	"errors"
	"reflect"
	"testing"
	"time"

	tele "gopkg.in/telebot.v3"
)

func TestBackoff_DoublesAndCaps(t *testing.T) {
	b := newBackoff(2*time.Second, 10*time.Second)
	want := []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second, 10 * time.Second, 10 * time.Second}
	for i, w := range want {
		if got := b.next(); got != w {
			t.Fatalf("шаг %d: %v", i, got)
		}
	}
	b.reset()
	if got := b.next(); got != 2*time.Second {
		t.Fatalf("после reset: %v", got)
	}
}

func TestConnectWithRetry_RetriesUntilSuccess(t *testing.T) {
	calls := 0
	var slept []time.Duration
	dial := func() (*tele.Bot, error) {
		calls++
		if calls <= 3 {
			return nil, errors.New("dial tcp: i/o timeout")
		}
		return tele.NewBot(tele.Settings{Token: "x", Offline: true})
	}
	b := connectWithRetry(dial, newBackoff(connectMinPause, connectMaxPause), func(d time.Duration) { slept = append(slept, d) })
	if b == nil || calls != 4 {
		t.Fatalf("b=%v calls=%d", b, calls)
	}
	if !reflect.DeepEqual(slept, []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second}) {
		t.Fatalf("slept=%v", slept)
	}
}

func TestRegisterHandlers_WorksOnOfflineBot(t *testing.T) {
	b, _ := tele.NewBot(tele.Settings{Token: "x", Offline: true})
	registerHandlers(b) // не должен паниковать и не должен ходить в сеть или базу
}
