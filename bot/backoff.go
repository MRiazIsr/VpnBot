package bot

import "time"

// backoff отдаёт растущие паузы: min, 2*min, 4*min ... но не больше max.
type backoff struct {
	min, max, cur time.Duration
}

func newBackoff(min, max time.Duration) *backoff {
	return &backoff{min: min, max: max}
}

// next возвращает очередную паузу и удваивает следующую.
func (b *backoff) next() time.Duration {
	if b.cur == 0 {
		b.cur = b.min
	}
	d := b.cur
	b.cur *= 2
	if b.cur > b.max {
		b.cur = b.max
	}
	return d
}

// reset возвращает паузу к минимальной.
func (b *backoff) reset() { b.cur = 0 }
