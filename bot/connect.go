package bot

import (
	"log"
	"time"

	tele "gopkg.in/telebot.v3"
)

const (
	connectMinPause = 2 * time.Second
	connectMaxPause = 2 * time.Minute
)

// connectWithRetry вызывает dial, пока он не удастся, делая растущие паузы
// между попытками. Telegram может быть недоступен — процесс при этом жить должен.
func connectWithRetry(dial func() (*tele.Bot, error), bo *backoff, sleep func(time.Duration)) *tele.Bot {
	failed := 0
	for {
		b, err := dial()
		if err == nil {
			if failed > 0 {
				log.Printf("bot: connected to Telegram after %d failed attempts", failed)
			}
			bo.reset()
			return b
		}
		failed++
		pause := bo.next()
		log.Printf("bot: Telegram unreachable, retry in %s: %v", pause, err)
		sleep(pause)
	}
}
