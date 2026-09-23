package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
	"vpnbot/database"

	"github.com/v2fly/v2ray-core/v4/app/stats/command"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// newTrafficTestDB подменяет глобальный DB на отдельную in-memory базу.
func newTrafficTestDB(t *testing.T) {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&database.User{}, &database.TrafficDaily{}, &database.InboundTrafficDaily{}); err != nil {
		t.Fatalf("automigrate: %v", err)
	}
	old := database.DB
	database.DB = db
	t.Cleanup(func() { database.DB = old })
}

func stat(name string, v int64) *command.Stat { return &command.Stat{Name: name, Value: v} }

func TestParseStats(t *testing.T) {
	users, inbounds := parseStats([]*command.Stat{
		stat("user>>>alice>>>traffic>>>uplink", 10),
		stat("user>>>alice>>>traffic>>>downlink", 100),
		stat("user>>>bob>>>traffic>>>downlink", 5),
		stat("inbound>>>DE-TCP>>>traffic>>>uplink", 7),
		stat("inbound>>>DE-TCP>>>traffic>>>downlink", 70),
		stat("outbound>>>direct>>>traffic>>>downlink", 999),
		stat("garbage", 1),
		stat("user>>>zero>>>traffic>>>uplink", 0),
	})
	if users["alice"] != (Delta{Up: 10, Down: 100}) || users["bob"] != (Delta{Down: 5}) {
		t.Fatalf("users = %+v", users)
	}
	if _, ok := users["zero"]; ok {
		t.Fatalf("zero-traffic user must be skipped: %+v", users)
	}
	if len(inbounds) != 1 || inbounds["DE-TCP"] != (Delta{Up: 7, Down: 70}) {
		t.Fatalf("inbounds = %+v", inbounds)
	}
}

func TestRecordTraffic_IncrementsSameDayAndSplitsDays(t *testing.T) {
	newTrafficTestDB(t)
	database.DB.Create(&database.User{Username: "alice", UUID: "u1", TrafficUsed: 1000})

	u := map[string]Delta{"alice": {Up: 10, Down: 100}, "ghost": {Down: 1}}
	in := map[string]Delta{"DE-TCP": {Up: 10, Down: 100}}
	for i := 0; i < 2; i++ {
		if _, err := recordTraffic("hetzner", "2026-09-23", u, in); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := recordTraffic("hetzner", "2026-09-24", u, in); err != nil {
		t.Fatal(err)
	}
	if _, err := recordTraffic("ruvds", "2026-09-24", u, nil); err != nil {
		t.Fatal(err)
	}

	var rows []database.TrafficDaily
	database.DB.Order("day, server").Find(&rows)
	if len(rows) != 3 {
		t.Fatalf("want 3 daily rows, got %+v", rows)
	}
	if rows[0].Day != "2026-09-23" || rows[0].Upload != 20 || rows[0].Download != 200 {
		t.Fatalf("same-day upsert must increment: %+v", rows[0])
	}
	var alice database.User
	database.DB.Where("username = ?", "alice").First(&alice)
	if alice.TrafficUsed != 1000+4*110 {
		t.Fatalf("traffic_used = %d, want %d", alice.TrafficUsed, 1000+4*110)
	}
	var ib []database.InboundTrafficDaily
	database.DB.Order("day").Find(&ib)
	if len(ib) != 2 || ib[0].Download != 200 || ib[1].Download != 100 {
		t.Fatalf("inbound rows = %+v", ib)
	}
}

func TestStatsPoller_DiscardsFirstReadAndResets(t *testing.T) {
	newTrafficTestDB(t)
	database.DB.Create(&database.User{Username: "alice", UUID: "u1"})

	var resets []bool
	p := &statsPoller{
		server: "hetzner",
		now:    func() time.Time { return time.Date(2026, 9, 23, 21, 30, 0, 0, time.UTC) }, // 00:30 MSK 24.09
		query: func(ctx context.Context, reset bool) ([]*command.Stat, error) {
			resets = append(resets, reset)
			return []*command.Stat{stat("user>>>alice>>>traffic>>>downlink", 500)}, nil
		},
	}
	for i := 0; i < 3; i++ {
		if err := p.poll(); err != nil {
			t.Fatal(err)
		}
	}
	for _, r := range resets {
		if !r {
			t.Fatalf("every query must reset counters, got %v", resets)
		}
	}
	var alice database.User
	database.DB.Where("username = ?", "alice").First(&alice)
	if alice.TrafficUsed != 1000 {
		t.Fatalf("first read must be discarded: traffic_used = %d, want 1000", alice.TrafficUsed)
	}
	var row database.TrafficDaily
	database.DB.First(&row)
	if row.Day != "2026-09-24" {
		t.Fatalf("day must be in MSK, got %q", row.Day)
	}
}

func TestTrafficAggregates(t *testing.T) {
	newTrafficTestDB(t)
	database.DB.Create(&database.User{Username: "alice", UUID: "u1", SubscriptionToken: "t1", TrafficUsed: 5000})
	database.DB.Create(&database.User{Username: "bob", UUID: "u2", SubscriptionToken: "t2", TrafficUsed: 50})
	var alice, bob database.User
	database.DB.Where("username = ?", "alice").First(&alice)
	database.DB.Where("username = ?", "bob").First(&bob)

	recordTraffic("hetzner", "2026-08-31", map[string]Delta{"alice": {Up: 1, Down: 9}}, nil)
	recordTraffic("hetzner", "2026-09-01", map[string]Delta{"alice": {Up: 2, Down: 20}}, map[string]Delta{"DE-TCP": {Down: 20}})
	recordTraffic("ruvds", "2026-09-02", map[string]Delta{"alice": {Up: 3, Down: 30}, "bob": {Down: 7}}, map[string]Delta{"RU": {Down: 37}})

	if got := UserMonthTraffic(alice.ID, "2026-09"); got != (Delta{Up: 5, Down: 50}) {
		t.Fatalf("month = %+v", got)
	}
	if got := UserUploadTotal(alice.ID); got != 6 {
		t.Fatalf("upload total = %d", got)
	}

	hist := UserMonthlyHistory(alice.ID, 6)
	if len(hist) != 2 || hist[0].Month != "2026-09" || hist[0].Down != 50 || hist[1].Month != "2026-08" {
		t.Fatalf("history = %+v", hist)
	}

	top := TopUsersForMonth("2026-09", 10)
	if len(top) != 2 || top[0].Username != "alice" || top[0].Total != alice.TrafficUsed+65 || top[1].Username != "bob" {
		t.Fatalf("top = %+v", top)
	}

	ib := InboundTrafficRange("2026-09-01", "2026-09-30")
	if len(ib) != 2 || ib[0].Tag != "DE-TCP" || ib[1].Tag != "RU" || ib[1].Server != "ruvds" {
		t.Fatalf("inbounds = %+v", ib)
	}
}

func TestMonthOf(t *testing.T) {
	if got := monthOf(time.Date(2026, 9, 30, 22, 0, 0, 0, time.UTC)); got != "2026-10" {
		t.Fatalf("MSK month boundary: got %q", got)
	}
}

// Без v2ray_api на RuVDS трафик RU-подключений не учитывается вовсе.
func TestGenerateRuVDSConfig_HasStatsAPI(t *testing.T) {
	newTrafficTestDB(t)
	if err := database.DB.AutoMigrate(&database.WireGuardConfig{}, &database.InboundConfig{}); err != nil {
		t.Fatal(err)
	}
	database.DB.Create(&database.WireGuardConfig{Enabled: true, RuVDSPrivateKey: "priv", HetznerPublicKey: "pub"})
	database.DB.Create(&database.User{Username: "alice", UUID: "550e8400-e29b-41d4-a716-446655440000", SubscriptionToken: "t1", Status: "active"})
	database.DB.Create(&database.InboundConfig{Tag: "RU-TCP", Protocol: "vless", ListenPort: 2060, TLSType: "reality", Enabled: true, ExitOutbound: "direct"})
	database.DB.Create(&database.InboundConfig{Tag: "RU-MASK", Protocol: "mask", ListenPort: 2071, MaskInnerTag: "RU-TCP", Enabled: true})

	b, err := GenerateRuVDSConfig()
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Experimental struct {
			V2RayAPI struct {
				Listen string `json:"listen"`
				Stats  struct {
					Enabled  bool     `json:"enabled"`
					Inbounds []string `json:"inbounds"`
					Users    []string `json:"users"`
				} `json:"stats"`
			} `json:"v2ray_api"`
		} `json:"experimental"`
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatal(err)
	}
	s := cfg.Experimental.V2RayAPI
	if s.Listen != ApiAddr || !s.Stats.Enabled {
		t.Fatalf("v2ray_api missing: %s", b)
	}
	if len(s.Stats.Users) != 1 || s.Stats.Users[0] != "alice" {
		t.Fatalf("users = %v", s.Stats.Users)
	}
	if len(s.Stats.Inbounds) != 1 || s.Stats.Inbounds[0] != "RU-TCP" {
		t.Fatalf("inbounds must list sing-box inbounds only, got %v", s.Stats.Inbounds)
	}
}
