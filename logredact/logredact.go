// Package logredact вырезает секреты (токен бота) из вывода log.
package logredact

import (
	"io"
	"regexp"
	"strings"
)

const (
	placeholder = "<redacted>"
	minSecret   = 8
)

// Форма токена Telegram: <id>:<35 символов>.
var tokenShape = regexp.MustCompile(`\d{6,}:[A-Za-z0-9_-]{30,}`)

// Redact заменяет на <redacted> каждое вхождение секретов (короче 8 символов
// игнорируются), затем всё, что похоже по форме на токен бота.
func Redact(s string, secrets ...string) string {
	for _, sec := range secrets {
		if len(sec) >= minSecret {
			s = strings.ReplaceAll(s, sec, placeholder)
		}
	}
	return tokenShape.ReplaceAllString(s, placeholder)
}

type writer struct {
	w       io.Writer
	secrets []string
}

// NewWriter возвращает writer, пропускающий каждую запись через Redact.
func NewWriter(w io.Writer, secrets ...string) io.Writer {
	return &writer{w: w, secrets: secrets}
}

func (r *writer) Write(p []byte) (int, error) {
	if _, err := r.w.Write([]byte(Redact(string(p), r.secrets...))); err != nil {
		return 0, err
	}
	// Длина исходного среза: иначе log сочтёт запись неполной.
	return len(p), nil
}
