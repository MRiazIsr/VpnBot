package bot

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	tele "gopkg.in/telebot.v3"
)

const (
	pollMinPause = time.Second
	pollMaxPause = 30 * time.Second
	pollLogEvery = time.Minute
)

// poller — свой long-poller вместо tele.LongPoller: тот глотает ошибки
// (в том числе 409 Conflict) и крутится без паузы.
type poller struct {
	timeout time.Duration

	// Подменяются в тестах.
	sleep func(d time.Duration, stop <-chan struct{}) bool // false — попросили остановиться
	now   func() time.Time
	logf  func(format string, args ...any)
}

func newPoller(timeout time.Duration) *poller {
	return &poller{
		timeout: timeout,
		sleep: func(d time.Duration, stop <-chan struct{}) bool {
			select {
			case <-time.After(d):
				return true
			case <-stop:
				return false
			}
		},
		now:  time.Now,
		logf: log.Printf,
	}
}

// Poll реализует tele.Poller.
func (p *poller) Poll(b *tele.Bot, dest chan tele.Update, stop chan struct{}) {
	bo := newBackoff(pollMinPause, pollMaxPause)
	offset := 0
	failed := 0
	var lastLog time.Time

	for {
		select {
		case <-stop:
			return
		default:
		}

		updates, err := fetchUpdates(b, offset, p.timeout)
		if err != nil {
			failed++
			switch {
			case failed == 1 && isConflict(err):
				p.logf("bot: polling conflict (409): another instance is polling with this token: %v", err)
				lastLog = p.now()
			case failed == 1:
				p.logf("bot: polling error: %v", err)
				lastLog = p.now()
			case p.now().Sub(lastLog) >= pollLogEvery:
				p.logf("bot: polling still failing (%d errors in a row): %v", failed, err)
				lastLog = p.now()
			}
			if !p.sleep(bo.next(), stop) {
				return
			}
			continue
		}

		if failed > 0 {
			p.logf("bot: polling recovered after %d errors", failed)
			failed = 0
		}
		bo.reset()

		for _, u := range updates {
			offset = u.ID + 1
			select {
			case dest <- u:
			case <-stop:
				return
			}
		}
	}
}

// fetchUpdates запрашивает getUpdates. Ответ, не являющийся JSON (например,
// HTML 502 от прокси), — ошибка: extractOk у telebot в этом случае молчит.
func fetchUpdates(b *tele.Bot, offset int, timeout time.Duration) ([]tele.Update, error) {
	data, err := b.Raw("getUpdates", map[string]any{
		"offset":  offset,
		"timeout": int(timeout / time.Second),
	})
	if err != nil {
		return nil, err
	}
	var resp struct {
		Ok     bool          `json:"ok"`
		Result []tele.Update `json:"result"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("unparsable getUpdates answer: %v (body: %q)", err, bodySnippet(data))
	}
	if !resp.Ok {
		return nil, fmt.Errorf("getUpdates not ok: %q", bodySnippet(data))
	}
	return resp.Result, nil
}

// bodySnippet обрезает тело ответа до 200 байт для сообщения об ошибке.
func bodySnippet(data []byte) string {
	s := strings.TrimSpace(string(data))
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

func isConflict(err error) bool {
	return err != nil && strings.Contains(err.Error(), "(409)")
}

// onBotError — tele.Settings.OnError: пишет ошибки обработчиков в лог.
func onBotError(err error, c tele.Context) {
	if c == nil {
		log.Printf("bot: error: %v", err)
		return
	}
	log.Printf("bot: handler error (update %d): %v", c.Update().ID, err)
}
