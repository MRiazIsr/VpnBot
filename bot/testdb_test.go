package bot

import (
	"testing"
	"vpnbot/database"
)

// При повторном запуске теста (-count>1) база с тем же именем должна быть пустой.
func TestNewTestDB_StartsEmptyOnRerun(t *testing.T) {
	newTestDB(t)
	var n int64
	database.DB.Model(&database.User{}).Count(&n)
	if n != 0 {
		t.Fatalf("база не пуста: %d", n)
	}
	database.DB.Create(&database.User{Username: "u", UUID: "x"})
}
