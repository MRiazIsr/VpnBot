package bot

import (
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	tele "gopkg.in/telebot.v3"
)

const conflict = `{"ok":false,"error_code":409,"description":"Conflict: terminated by other getUpdates request"}`

// runPoller запускает Poll с подменёнными sleep и logf; останавливает его в Cleanup.
func runPoller(t *testing.T, f *fakeAPI) (chan tele.Update, func() []time.Duration, func() string) {
	t.Helper()
	b := newTestBot(t, f)
	p := newPoller(time.Second)
	var mu sync.Mutex
	var durs []time.Duration
	var lines []string
	p.sleep = func(d time.Duration, stop <-chan struct{}) bool {
		mu.Lock()
		durs = append(durs, d)
		mu.Unlock()
		return true
	}
	p.logf = func(format string, a ...any) {
		mu.Lock()
		lines = append(lines, fmt.Sprintf(format, a...))
		mu.Unlock()
	}
	dest := make(chan tele.Update)
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() { p.Poll(b, dest, stop); close(done) }()
	t.Cleanup(func() {
		close(stop)
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("Poll не остановился")
		}
	})
	return dest,
		func() []time.Duration { mu.Lock(); defer mu.Unlock(); return append([]time.Duration(nil), durs...) },
		func() string { mu.Lock(); defer mu.Unlock(); return strings.Join(lines, "\n") }
}

func TestPoller_DeliversUpdatesAndAdvancesOffset(t *testing.T) {
	f := newFakeAPI(t)
	f.script("getUpdates", `{"ok":true,"result":[{"update_id":7,"message":{"message_id":1,"text":"hi","chat":{"id":5}}}]}`)
	dest, _, _ := runPoller(t, f)
	if u := <-dest; u.ID != 7 {
		t.Fatalf("id=%d", u.ID)
	}
	eventually(t, func() bool { p := f.params("getUpdates"); return len(p) >= 2 && fmt.Sprint(p[1]["offset"]) == "8" })
}

func TestPoller_LogsConflictAndBacksOff(t *testing.T) {
	f := newFakeAPI(t)
	f.script("getUpdates", conflict, conflict, conflict)
	_, slept, logs := runPoller(t, f)
	eventually(t, func() bool { return strings.Contains(logs(), "polling recovered after 3 errors") })
	if !strings.Contains(logs(), "polling conflict (409): another instance") {
		t.Fatalf("logs: %s", logs())
	}
	if got := slept(); !reflect.DeepEqual(got[:3], []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}) {
		t.Fatalf("slept=%v", got)
	}
}

func TestPoller_PauseResetsAfterSuccess(t *testing.T) {
	f := newFakeAPI(t)
	f.script("getUpdates", conflict, conflict, `{"ok":true,"result":[]}`, conflict)
	_, slept, _ := runPoller(t, f)
	eventually(t, func() bool { return len(slept()) >= 3 })
	if got := slept()[2]; got != time.Second {
		t.Fatalf("после успеха пауза %v, ждали 1s", got)
	}
}

func TestPoller_NonJSONAnswerIsAnErrorNotASpin(t *testing.T) {
	f := newFakeAPI(t)
	f.on("getUpdates", func(map[string]any) (int, string) { return 502, "<html>Bad Gateway</html>" })
	_, slept, logs := runPoller(t, f)
	eventually(t, func() bool { return len(slept()) >= 2 })
	if !strings.Contains(logs(), "bot: polling error:") {
		t.Fatalf("logs: %s", logs())
	}
	if n := len(f.params("getUpdates")); n > len(slept())+1 {
		t.Fatalf("%d запросов на %d пауз — цикл без паузы", n, len(slept()))
	}
}

func TestPoller_StopsDuringPause(t *testing.T) {
	f := newFakeAPI(t)
	f.on("getUpdates", func(map[string]any) (int, string) { return 200, conflict })
	p := newPoller(time.Second)
	p.logf = func(string, ...any) {}
	p.sleep = func(time.Duration, <-chan struct{}) bool { return false }
	done := make(chan struct{})
	go func() { p.Poll(newTestBot(t, f), make(chan tele.Update), make(chan struct{})); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Poll не вышел")
	}
}

func TestPoller_RepeatsAreLoggedOncePerMinute(t *testing.T) {
	f := newFakeAPI(t)
	f.on("getUpdates", func(map[string]any) (int, string) { return 200, conflict })
	clock := time.Unix(0, 0)
	var mu sync.Mutex
	var lines []string
	p := newPoller(time.Second)
	p.now = func() time.Time { mu.Lock(); defer mu.Unlock(); return clock }
	p.logf = func(format string, a ...any) {
		mu.Lock()
		lines = append(lines, fmt.Sprintf(format, a...))
		mu.Unlock()
	}
	n := 0
	p.sleep = func(time.Duration, <-chan struct{}) bool { // 100 ошибок за 50 с, затем скачок на 61 с, ещё одна — и стоп
		mu.Lock()
		defer mu.Unlock()
		n++
		if n < 100 {
			clock = clock.Add(500 * time.Millisecond)
		} else if n == 100 {
			clock = clock.Add(61 * time.Second)
		}
		return n <= 101
	}
	p.Poll(newTestBot(t, f), make(chan tele.Update), make(chan struct{}))
	if len(lines) != 2 || !strings.Contains(lines[1], "polling still failing (") {
		t.Fatalf("lines=%q", lines)
	}
}
