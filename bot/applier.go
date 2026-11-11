package bot

import (
	"fmt"
	"log"
	"sync"
	"vpnbot/service"
)

// applyFull — полная перегенерация: sing-box, пользователи telemt, конфиг telemt.
// Переменная, чтобы тесты могли подменить вызовы, пишущие в /etc и дёргающие systemctl.
var applyFull = func() error {
	err := service.GenerateAndReload()
	service.SyncTelemetUsers()
	service.GenerateAndReloadTelemet()
	return err
}

// applyTelemt — перегенерация только конфига telemt.
var applyTelemt = func() error {
	service.GenerateAndReloadTelemet()
	return nil
}

type applyRequest struct {
	full  bool
	after func(err error)
}

// applier выполняет медленные перегенерации настроек в одной фоновой горутине,
// чтобы обработчик нажатия отвечал сразу.
type applier struct {
	full, telemt func() error

	mu      sync.Mutex
	pending []applyRequest
	wake    chan struct{}
}

func newApplier(full, telemt func() error) *applier {
	a := &applier{full: full, telemt: telemt, wake: make(chan struct{}, 1)}
	go a.loop()
	return a
}

// request ставит запрос в очередь и не блокирует. after (может быть nil)
// вызывается из фоновой горутины после перегенерации.
func (a *applier) request(full bool, after func(err error)) {
	a.mu.Lock()
	a.pending = append(a.pending, applyRequest{full: full, after: after})
	a.mu.Unlock()
	select {
	case a.wake <- struct{}{}:
	default:
	}
}

func (a *applier) loop() {
	for range a.wake {
		a.mu.Lock()
		batch := a.pending
		a.pending = nil
		a.mu.Unlock()
		if len(batch) == 0 {
			continue
		}

		needFull := false
		for _, r := range batch {
			if r.full {
				needFull = true
				break
			}
		}
		run := a.telemt
		if needFull {
			run = a.full
		}
		err := safeRun(run)
		if err != nil {
			log.Printf("bot: apply settings failed: %v", err)
		}
		for _, r := range batch {
			if r.after != nil {
				safeAfter(r.after, err)
			}
		}
	}
}

func safeRun(f func() error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	return f()
}

func safeAfter(f func(error), err error) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("bot: apply callback panic: %v", r)
		}
	}()
	f(err)
}
