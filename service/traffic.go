package service

import (
	"context"
	"fmt"
	"log"
	"net"
	"strings"
	"sync"
	"time"
	_ "time/tzdata" // Europe/Moscow без зависимости от tzdata на сервере
	"vpnbot/database"

	"github.com/v2fly/v2ray-core/v4/app/stats/command"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Учёт трафика: V2Ray Stats API sing-box на Hetzner (локально) и на RuVDS
// (через SSH). Счётчики читаются со сбросом, каждый опрос приносит ровно
// трафик с прошлого опроса и пишется в суточные таблицы + users.traffic_used.

// Delta — трафик за интервал опроса.
type Delta struct {
	Up   int64
	Down int64
}

var mskLoc = mustLoadMSK()

func mustLoadMSK() *time.Location {
	loc, err := time.LoadLocation("Europe/Moscow")
	if err != nil {
		return time.FixedZone("MSK", 3*60*60)
	}
	return loc
}

func dayOf(t time.Time) string   { return t.In(mskLoc).Format("2006-01-02") }
func monthOf(t time.Time) string { return t.In(mskLoc).Format("2006-01") }

// CurrentMonth — текущий месяц по Москве в формате "2026-09".
func CurrentMonth() string { return monthOf(time.Now()) }

// parseStats разбирает `user>>>NAME>>>traffic>>>uplink|downlink` и
// `inbound>>>TAG>>>traffic>>>...`. Нулевые и прочие счётчики пропускаются.
func parseStats(stats []*command.Stat) (users, inbounds map[string]Delta) {
	users = map[string]Delta{}
	inbounds = map[string]Delta{}
	for _, s := range stats {
		if s.Value <= 0 {
			continue
		}
		parts := strings.Split(s.Name, ">>>")
		if len(parts) != 4 || parts[2] != "traffic" {
			continue
		}
		var m map[string]Delta
		switch parts[0] {
		case "user":
			m = users
		case "inbound":
			m = inbounds
		default:
			continue
		}
		d := m[parts[1]]
		switch parts[3] {
		case "uplink":
			d.Up += s.Value
		case "downlink":
			d.Down += s.Value
		default:
			continue
		}
		m[parts[1]] = d
	}
	return users, inbounds
}

// recordTraffic одной транзакцией пишет суточные строки (upsert с
// инкрементом) и прибавляет трафик к users.traffic_used. Возвращает
// имена пользователей, которым что-то начислено (для checkLimits).
// Неизвестные пользователи (удалены из БД) пропускаются.
func recordTraffic(server, day string, users, inbounds map[string]Delta) ([]string, error) {
	var touched []string
	err := database.DB.Transaction(func(tx *gorm.DB) error {
		if len(users) > 0 {
			names := make([]string, 0, len(users))
			for n := range users {
				names = append(names, n)
			}
			var found []database.User
			if err := tx.Select("id", "username").Where("username IN ?", names).Find(&found).Error; err != nil {
				return err
			}
			for _, u := range found {
				d := users[u.Username]
				row := database.TrafficDaily{UserID: u.ID, Day: day, Server: server, Upload: d.Up, Download: d.Down}
				if err := tx.Clauses(clause.OnConflict{
					Columns:   []clause.Column{{Name: "user_id"}, {Name: "day"}, {Name: "server"}},
					DoUpdates: incrementAssignments(),
				}).Create(&row).Error; err != nil {
					return err
				}
				if err := tx.Model(&database.User{}).Where("id = ?", u.ID).
					Update("traffic_used", gorm.Expr("traffic_used + ?", d.Up+d.Down)).Error; err != nil {
					return err
				}
				touched = append(touched, u.Username)
			}
		}
		for tag, d := range inbounds {
			row := database.InboundTrafficDaily{Tag: tag, Day: day, Server: server, Upload: d.Up, Download: d.Down}
			if err := tx.Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "tag"}, {Name: "day"}, {Name: "server"}},
				DoUpdates: incrementAssignments(),
			}).Create(&row).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return touched, nil
}

func incrementAssignments() clause.Set {
	return clause.Assignments(map[string]any{
		"upload":   gorm.Expr("upload + excluded.upload"),
		"download": gorm.Expr("download + excluded.download"),
	})
}

// statsQuerier читает счётчики; reset=true обнуляет их на стороне sing-box.
type statsQuerier func(ctx context.Context, reset bool) ([]*command.Stat, error)

// statsPoller — опрос одного сервера. Первое чтение после старта процесса
// выбрасывается: до перехода на reset-режим счётчики sing-box накопительные
// и уже учтены, а после рестарта vpnbot в них лежит максимум один интервал.
type statsPoller struct {
	server string
	query  statsQuerier
	now    func() time.Time

	mu     sync.Mutex
	primed bool
}

func (p *statsPoller) poll() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	stats, err := p.query(ctx, true)
	if err != nil {
		return err
	}
	if !p.primed {
		p.primed = true
		return nil
	}
	users, inbounds := parseStats(stats)
	if len(users) == 0 && len(inbounds) == 0 {
		return nil
	}
	now := time.Now
	if p.now != nil {
		now = p.now
	}
	touched, err := recordTraffic(p.server, dayOf(now()), users, inbounds)
	if err != nil {
		return fmt.Errorf("запись трафика %s: %w", p.server, err)
	}
	for _, u := range touched {
		checkLimits(u)
	}
	return nil
}

// grpcStatsQuerier — QueryStats через gRPC с заданным dialer'ом.
func grpcStatsQuerier(dial func(ctx context.Context, addr string) (net.Conn, error), cleanup func()) statsQuerier {
	return func(ctx context.Context, reset bool) ([]*command.Stat, error) {
		if cleanup != nil {
			defer cleanup()
		}
		opts := []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())}
		if dial != nil {
			opts = append(opts, grpc.WithContextDialer(dial))
		}
		conn, err := grpc.DialContext(ctx, ApiAddr, opts...)
		if err != nil {
			return nil, err
		}
		defer conn.Close()
		resp, err := command.NewStatsServiceClient(conn).QueryStats(ctx, &command.QueryStatsRequest{Pattern: "", Reset_: reset})
		if err != nil {
			return nil, err
		}
		return resp.Stat, nil
	}
}

var hetznerPoller = &statsPoller{server: "hetzner", query: grpcStatsQuerier(nil, nil)}

var ruvdsPoller = &statsPoller{server: "ruvds", query: func(ctx context.Context, reset bool) ([]*command.Stat, error) {
	client, err := sshConnect()
	if err != nil {
		return nil, fmt.Errorf("SSH: %w", err)
	}
	dial := func(ctx context.Context, addr string) (net.Conn, error) { return client.Dial("tcp", addr) }
	return grpcStatsQuerier(dial, func() { client.Close() })(ctx, reset)
}}

// UpdateTrafficViaAPI — опрос локального (Hetzner) sing-box.
func UpdateTrafficViaAPI() error {
	return hetznerPoller.poll()
}

// UpdateTrafficRuVDS — опрос sing-box на RuVDS через SSH (если зеркало включено).
func UpdateTrafficRuVDS() error {
	if !IsRuVDSEnabled() {
		return nil
	}
	return ruvdsPoller.poll()
}

// StartTrafficPolling — фоновые опросы: Hetzner каждые 10 с, RuVDS каждые 60 с.
func StartTrafficPolling() {
	go func() {
		for range time.Tick(10 * time.Second) {
			if err := UpdateTrafficViaAPI(); err != nil {
				log.Println("traffic hetzner:", err)
			}
		}
	}()
	go func() {
		for range time.Tick(60 * time.Second) {
			if err := UpdateTrafficRuVDS(); err != nil {
				log.Println("traffic ruvds:", err)
			}
		}
	}()
}

// --- Агрегации для бота и API ---

// UserMonthTraffic — трафик пользователя за месяц ("2026-09") по всем серверам.
func UserMonthTraffic(userID uint, month string) Delta {
	var d struct{ Up, Down int64 }
	database.DB.Model(&database.TrafficDaily{}).
		Select("COALESCE(SUM(upload),0) AS up, COALESCE(SUM(download),0) AS down").
		Where("user_id = ? AND day LIKE ?", userID, month+"-%").Scan(&d)
	return Delta{Up: d.Up, Down: d.Down}
}

// UserUploadTotal — отправлено пользователем за всю историю учёта.
func UserUploadTotal(userID uint) int64 {
	var up int64
	database.DB.Model(&database.TrafficDaily{}).Select("COALESCE(SUM(upload),0)").
		Where("user_id = ?", userID).Scan(&up)
	return up
}

// MonthTraffic — строка помесячной истории.
type MonthTraffic struct {
	Month string `json:"month"`
	Up    int64  `json:"upload"`
	Down  int64  `json:"download"`
}

// UserMonthlyHistory — последние limit месяцев, новые первыми.
func UserMonthlyHistory(userID uint, limit int) []MonthTraffic {
	rows := []MonthTraffic{}
	database.DB.Model(&database.TrafficDaily{}).
		Select("substr(day,1,7) AS month, SUM(upload) AS up, SUM(download) AS down").
		Where("user_id = ?", userID).Group("month").Order("month DESC").Limit(limit).Scan(&rows)
	return rows
}

// UserMonthRow — строка топа за месяц.
type UserMonthRow struct {
	UserID   uint   `json:"user_id"`
	Username string `json:"username"`
	Up       int64  `json:"month_upload"`
	Down     int64  `json:"month_download"`
	Total    int64  `json:"total"` // users.traffic_used — за всё время
}

// TopUsersForMonth — пользователи с трафиком за месяц, по убыванию.
func TopUsersForMonth(month string, limit int) []UserMonthRow {
	rows := []UserMonthRow{}
	database.DB.Table("traffic_dailies AS t").
		Select("t.user_id, u.username, SUM(t.upload) AS up, SUM(t.download) AS down, u.traffic_used AS total").
		Joins("JOIN users u ON u.id = t.user_id").
		Where("t.day LIKE ?", month+"-%").
		Group("t.user_id, u.username, u.traffic_used").
		Order("SUM(t.upload + t.download) DESC").Limit(limit).Scan(&rows)
	return rows
}

// InboundDayRow — трафик подключения за день.
type InboundDayRow struct {
	Day    string `json:"day"`
	Tag    string `json:"tag"`
	Server string `json:"server"`
	Up     int64  `json:"upload"`
	Down   int64  `json:"download"`
}

// InboundTrafficRange — по дням за [from, to] включительно ("2026-09-01").
func InboundTrafficRange(from, to string) []InboundDayRow {
	rows := []InboundDayRow{}
	database.DB.Model(&database.InboundTrafficDaily{}).
		Select("day, tag, server, upload AS up, download AS down").
		Where("day >= ? AND day <= ?", from, to).Order("day, tag, server").Scan(&rows)
	return rows
}
