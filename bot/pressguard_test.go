package bot

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestPressGuard_WindowCountsFromFirstPress(t *testing.T) {
	now := time.Unix(100, 0)
	g := newPressGuard(3*time.Second, func() time.Time { return now })
	if !g.allow("a") {
		t.Fatal("первое нажатие отброшено")
	}
	now = now.Add(2900 * time.Millisecond)
	if g.allow("a") {
		t.Fatal("повтор через 2.9 с пропущен")
	}
	if !g.allow("b") {
		t.Fatal("другая кнопка отброшена")
	}
	now = now.Add(100 * time.Millisecond) // ровно 3 с от первого нажатия "a"
	if !g.allow("a") {
		t.Fatal("нажатие через 3 с отброшено: повтор продлил окно")
	}
}

func TestPressGuard_ForgetsOldKeys(t *testing.T) {
	now := time.Unix(100, 0)
	g := newPressGuard(3*time.Second, func() time.Time { return now })
	for i := 0; i < 1000; i++ {
		g.allow(fmt.Sprint(i))
	}
	now = now.Add(4 * time.Second)
	g.allow("x")
	if n := len(g.seen); n != 1 {
		t.Fatalf("в памяти %d записей", n)
	}
}

func TestPressGuard_ConcurrentSameKey(t *testing.T) {
	g := newPressGuard(3*time.Second, time.Now)
	var ok atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if g.allow("k") {
				ok.Add(1)
			}
		}()
	}
	wg.Wait()
	if ok.Load() != 1 {
		t.Fatalf("пропущено %d", ok.Load())
	}
}
