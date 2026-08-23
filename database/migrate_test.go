package database

import (
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// newTestDB подменяет глобальный DB на пустую базу в памяти.
func newTestDB(t *testing.T) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open in-memory sqlite: %v", err)
	}
	if err := db.AutoMigrate(&InboundConfig{}); err != nil {
		t.Fatalf("automigrate: %v", err)
	}
	db.Exec("DELETE FROM inbound_configs")
	old := DB
	DB = db
	t.Cleanup(func() { DB = old })
}

func fingerprintOf(t *testing.T, tag string) string {
	t.Helper()
	var ib InboundConfig
	if err := DB.Where("tag = ?", tag).First(&ib).Error; err != nil {
		t.Fatalf("inbound %q not found: %v", tag, err)
	}
	return ib.Fingerprint
}

func TestMigrateFingerprintToChrome_UpdatesRandomAndEmpty(t *testing.T) {
	newTestDB(t)
	DB.Create(&InboundConfig{Tag: "DE-TCP", TLSType: "reality", Fingerprint: "random"})
	DB.Create(&InboundConfig{Tag: "DE-WL", TLSType: "reality", Fingerprint: ""})

	migrateFingerprintToChrome()

	if got := fingerprintOf(t, "DE-TCP"); got != "chrome" {
		t.Fatalf("random must become chrome, got %q", got)
	}
	if got := fingerprintOf(t, "DE-WL"); got != "chrome" {
		t.Fatalf("empty must become chrome, got %q", got)
	}
}

func TestMigrateFingerprintToChrome_LeavesExplicitChoiceAlone(t *testing.T) {
	newTestDB(t)
	DB.Create(&InboundConfig{Tag: "DE-FF", TLSType: "reality", Fingerprint: "firefox"})

	migrateFingerprintToChrome()

	if got := fingerprintOf(t, "DE-FF"); got != "firefox" {
		t.Fatalf("explicit fingerprint must survive, got %q", got)
	}
}

// Fingerprint осмыслен только для Reality: у сертификатных инбаундов (hysteria2)
// он в ссылку не попадает, и трогать его незачем.
func TestMigrateFingerprintToChrome_SkipsNonReality(t *testing.T) {
	newTestDB(t)
	DB.Create(&InboundConfig{Tag: "hy2-in", TLSType: "certificate", Fingerprint: "random"})

	migrateFingerprintToChrome()

	if got := fingerprintOf(t, "hy2-in"); got != "random" {
		t.Fatalf("non-reality inbound must be untouched, got %q", got)
	}
}

func TestMigrateFingerprintToChrome_IsIdempotent(t *testing.T) {
	newTestDB(t)
	DB.Create(&InboundConfig{Tag: "DE-TCP", TLSType: "reality", Fingerprint: "random"})

	migrateFingerprintToChrome()
	var second int64
	DB.Model(&InboundConfig{}).
		Where("tls_type = ? AND fingerprint IN ?", "reality", []string{"", "random"}).
		Count(&second)

	if second != 0 {
		t.Fatalf("second run must find nothing to update, found %d rows", second)
	}
}
