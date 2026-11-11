package bot

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"vpnbot/database"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	tele "gopkg.in/telebot.v3"
)

// testToken — заведомо фальшивый токен; в тестах используется только он.
const testToken = "123456789:TEST_TOKEN_not_real_0123456789abcdef"

const (
	defaultMessageJSON = `{"ok":true,"result":{"message_id":1,"date":0,"chat":{"id":1,"type":"private"}}}`
	defaultTrueJSON    = `{"ok":true,"result":true}`
	defaultUpdatesJSON = `{"ok":true,"result":[]}`
)

type fakeCall struct {
	method string
	params map[string]any
}

// fakeAPI — поддельный Telegram Bot API. Каждый вызов записывается при
// получении, до запуска обработчика; сами обработчики работают без мьютекса.
type fakeAPI struct {
	srv      *httptest.Server
	mu       sync.Mutex
	calls    []fakeCall
	handlers map[string]func(p map[string]any) (int, string)
	scripts  map[string][]string
}

func newFakeAPI(t *testing.T) *fakeAPI {
	t.Helper()
	f := &fakeAPI{
		handlers: map[string]func(p map[string]any) (int, string){},
		scripts:  map[string][]string{},
	}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeAPI) serve(w http.ResponseWriter, r *http.Request) {
	method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
	params := parseParams(r)

	f.mu.Lock()
	f.calls = append(f.calls, fakeCall{method: method, params: params})
	h := f.handlers[method]
	var scripted string
	hasScript := false
	if h == nil && len(f.scripts[method]) > 0 {
		scripted, f.scripts[method] = f.scripts[method][0], f.scripts[method][1:]
		hasScript = true
	}
	f.mu.Unlock()

	status, body := http.StatusOK, ""
	switch {
	case h != nil:
		status, body = h(params)
	case hasScript:
		body = scripted
	default:
		body = defaultAnswer(method)
		if method == "getUpdates" {
			time.Sleep(20 * time.Millisecond)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

func defaultAnswer(method string) string {
	switch method {
	case "answerCallbackQuery":
		return defaultTrueJSON
	case "getUpdates":
		return defaultUpdatesJSON
	}
	return defaultMessageJSON
}

// parseParams читает JSON-тело или multipart-форму; на прочее не падает.
func parseParams(r *http.Request) map[string]any {
	params := map[string]any{}
	mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if mt == "multipart/form-data" {
		if err := r.ParseMultipartForm(32 << 20); err == nil && r.MultipartForm != nil {
			for k, v := range r.MultipartForm.Value {
				if len(v) > 0 {
					params[k] = v[0]
				}
			}
		}
		return params
	}
	data, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(data, &params)
	if params == nil {
		params = map[string]any{}
	}
	return params
}

// on задаёт свой ответ на метод; h вызывается без мьютекса fakeAPI.
// Если h блокируется, его надо освобождать в t.Cleanup, зарегистрированном ПОСЛЕ
// newFakeAPI (cleanup идут LIFO, а httptest.Server.Close ждёт текущие обработчики).
func (f *fakeAPI) on(method string, h func(p map[string]any) (int, string)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.handlers[method] = h
}

// script задаёт ответы по очереди, затем действуют ответы по умолчанию.
func (f *fakeAPI) script(method string, answers ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.scripts[method] = append(f.scripts[method], answers...)
}

func (f *fakeAPI) methods() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.calls))
	for _, c := range f.calls {
		out = append(out, c.method)
	}
	return out
}

func (f *fakeAPI) params(method string) []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []map[string]any
	for _, c := range f.calls {
		if c.method == method {
			out = append(out, c.params)
		}
	}
	return out
}

func sameChat(v any, chatID int64) bool {
	switch x := v.(type) {
	case string:
		return x == strconv.FormatInt(chatID, 10)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64) == strconv.FormatInt(chatID, 10)
	}
	return false
}

// texts — поле text из sendMessage и editMessageText для данного chat_id.
func (f *fakeAPI) texts(chatID int64) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.calls {
		if c.method != "sendMessage" && c.method != "editMessageText" {
			continue
		}
		if !sameChat(c.params["chat_id"], chatID) {
			continue
		}
		if s, ok := c.params["text"].(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func newTestBot(t *testing.T, f *fakeAPI) *tele.Bot {
	t.Helper()
	b, err := tele.NewBot(tele.Settings{
		URL:         f.srv.URL,
		Token:       testToken,
		Offline:     true,
		Synchronous: true,
		OnError:     onBotError,
	})
	if err != nil {
		t.Fatalf("NewBot: %v", err)
	}
	return b
}

// newTestDB подменяет глобальный DB на отдельную in-memory базу.
func newTestDB(t *testing.T) {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&database.User{}, &database.InboundConfig{}, &database.TelemetConfig{},
		&database.TelemetUser{}, &database.HealthConfig{}); err != nil {
		t.Fatalf("automigrate: %v", err)
	}
	old := database.DB
	database.DB = db
	t.Cleanup(func() {
		database.DB = old
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
}

// eventually ждёт выполнения условия до 2 секунд.
func eventually(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("условие не выполнилось за 2 с")
}

func callbackUpdate(sender int64, data string) tele.Update {
	return tele.Update{ID: 1, Callback: &tele.Callback{
		ID:      "cb",
		Sender:  &tele.User{ID: sender},
		Data:    "\f" + data,
		Message: &tele.Message{ID: 10, Chat: &tele.Chat{ID: sender}},
	}}
}

func textUpdate(sender int64, text string) tele.Update {
	return tele.Update{ID: 1, Message: &tele.Message{
		ID:     1,
		Text:   text,
		Sender: &tele.User{ID: sender},
		Chat:   &tele.Chat{ID: sender, Type: tele.ChatPrivate},
	}}
}

// logBuf — потокобезопасный приёмник вывода log: горутины рассылки пишут в него параллельно с тестом.
type logBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *logBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *logBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// captureLog перенаправляет вывод log в буфер до конца теста.
func captureLog(t *testing.T) *logBuf {
	t.Helper()
	lb := &logBuf{}
	old := log.Writer()
	log.SetOutput(lb)
	t.Cleanup(func() { log.SetOutput(old) })
	return lb
}
