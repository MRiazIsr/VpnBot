package handlers

import (
	"net/http"
	"vpnbot/database"

	"github.com/gin-gonic/gin"
)

func GetStats() gin.HandlerFunc {
	return func(c *gin.Context) {
		var totalUsers int64
		database.DB.Model(&database.User{}).Count(&totalUsers)

		var activeUsers int64
		database.DB.Model(&database.User{}).Where("status = ?", "active").Count(&activeUsers)

		var bannedUsers int64
		database.DB.Model(&database.User{}).Where("status = ?", "banned").Count(&bannedUsers)

		var expiredUsers int64
		database.DB.Model(&database.User{}).Where("status = ?", "expired").Count(&expiredUsers)

		var totalTrafficUsed int64
		database.DB.Model(&database.User{}).Select("COALESCE(SUM(traffic_used), 0)").Scan(&totalTrafficUsed)

		c.JSON(http.StatusOK, gin.H{
			"total_users":        totalUsers,
			"active_users":       activeUsers,
			"banned_users":       bannedUsers,
			"expired_users":      expiredUsers,
			"total_traffic_used": totalTrafficUsed,
		})
	}
}
