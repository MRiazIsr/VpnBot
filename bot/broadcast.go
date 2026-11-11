package bot

import (
	"errors"
	"log"
	"sync"
	"time"
	"vpnbot/database"

	tele "gopkg.in/telebot.v3"
)

const (
	// broadcastPause — пауза после каждого получателя (~20 сообщений в секунду).
	broadcastPause = 50 * time.Millisecond
	// broadcastAttempts — сколько раз пробуем отправить одному получателю при 429.
	broadcastAttempts = 3
)

// messenger — то, что нужно рассылке от бота; ему удовлетворяет *tele.Bot.
type messenger interface {
	Send(to tele.Recipient, what interface{}, opts ...interface{}) (*tele.Message, error)
}

// broadcastRecipients — telegram_id действующих пользователей по возрастанию id.
func broadcastRecipients() []int64 {
	var ids []int64
	database.DB.Model(&database.User{}).
		Where("status = ? AND telegram_id <> 0", "active").
		Order("id ASC").Pluck("telegram_id", &ids)
	return ids
}

// sendToAll шлёт text каждому из ids с паузой после каждого получателя.
// На 429 ждёт сколько просит Telegram и повторяет (всего не больше
// broadcastAttempts попыток); любая другая ошибка — получатель в failed.
func sendToAll(m messenger, ids []int64, text string, sleep func(time.Duration)) (sent, failed int) {
	for _, id := range ids {
		ok := false
		for attempt := 1; attempt <= broadcastAttempts; attempt++ {
			_, err := m.Send(&tele.User{ID: id}, text)
			if err == nil {
				ok = true
				break
			}
			var flood tele.FloodError
			if !errors.As(err, &flood) || attempt == broadcastAttempts {
				break
			}
			sleep(time.Duration(flood.RetryAfter) * time.Second)
		}
		if ok {
			sent++
		} else {
			failed++
		}
		sleep(broadcastPause)
	}
	return sent, failed
}

// broadcaster не даёт запустить две рассылки одновременно.
// Нулевое значение готово к работе.
type broadcaster struct {
	mu      sync.Mutex
	running bool
}

// start запускает run в новой горутине; false — если рассылка уже идёт.
// Паника в run пишется в лог, признак «идёт» снимается в любом случае.
func (br *broadcaster) start(run func()) bool {
	br.mu.Lock()
	if br.running {
		br.mu.Unlock()
		return false
	}
	br.running = true
	br.mu.Unlock()

	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("bot: broadcast panic: %v", r)
			}
			br.mu.Lock()
			br.running = false
			br.mu.Unlock()
		}()
		run()
	}()
	return true
}
