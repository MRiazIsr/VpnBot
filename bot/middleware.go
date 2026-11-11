package bot

import (
	"fmt"

	tele "gopkg.in/telebot.v3"
)

// pressKey различает нажатия по человеку, кнопке и её данным.
func pressKey(c tele.Context) string {
	var id int64
	if s := c.Sender(); s != nil {
		id = s.ID
	}
	cb := c.Callback()
	if cb == nil {
		return fmt.Sprintf("%d||", id)
	}
	return fmt.Sprintf("%d|%s|%s", id, cb.Unique, cb.Data)
}

// ackCallback сразу отвечает на нажатие кнопки, чтобы у человека не крутились часики,
// и только потом запускает обработчик.
func ackCallback(next tele.HandlerFunc) tele.HandlerFunc {
	return func(c tele.Context) error {
		if c.Callback() != nil {
			_ = c.Respond()
		}
		return next(c)
	}
}

// dropRepeats отбрасывает быстрые повторные нажатия кнопок; сообщения не трогает.
func dropRepeats(g *pressGuard) tele.MiddlewareFunc {
	return func(next tele.HandlerFunc) tele.HandlerFunc {
		return func(c tele.Context) error {
			if c.Callback() != nil && !g.allow(pressKey(c)) {
				return nil
			}
			return next(c)
		}
	}
}
