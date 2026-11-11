package bot

import (
	"sync"
	"time"
)

// repeatWindow — сколько секунд повторное нажатие той же кнопки тем же человеком
// считается случайным дублем и игнорируется.
const repeatWindow = 3 * time.Second

// pressGuard помнит, когда кнопка была принята в последний раз.
type pressGuard struct {
	mu     sync.Mutex
	window time.Duration
	now    func() time.Time
	seen   map[string]time.Time
}

func newPressGuard(window time.Duration, now func() time.Time) *pressGuard {
	return &pressGuard{window: window, now: now, seen: map[string]time.Time{}}
}

// allow возвращает true для первого нажатия и false для повторов, пока с первого
// принятого нажатия прошло меньше окна; повтор окно не продлевает.
func (g *pressGuard) allow(key string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	t := g.now()
	for k, at := range g.seen {
		if t.Sub(at) >= g.window {
			delete(g.seen, k)
		}
	}
	if _, dup := g.seen[key]; dup {
		return false
	}
	g.seen[key] = t
	return true
}
