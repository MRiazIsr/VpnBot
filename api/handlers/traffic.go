package handlers

import (
	"net/http"
	"regexp"
	"strconv"
	"time"
	"vpnbot/database"
	"vpnbot/service"

	"github.com/gin-gonic/gin"
)

var (
	monthRe = regexp.MustCompile(`^\d{4}-\d{2}$`)
	dayRe   = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
)

// GET /api/traffic/users?month=YYYY-MM — трафик пользователей за месяц
// (по умолчанию текущий, MSK) + всего за всё время.
func GetTrafficUsers() gin.HandlerFunc {
	return func(c *gin.Context) {
		month := c.DefaultQuery("month", service.CurrentMonth())
		if !monthRe.MatchString(month) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "month must be YYYY-MM"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"month": month, "users": service.TopUsersForMonth(month, 1000)})
	}
}

// GET /api/traffic/users/:id — всего + помесячная история (до 24 месяцев).
func GetTrafficUser() gin.HandlerFunc {
	return func(c *gin.Context) {
		id, err := strconv.ParseUint(c.Param("id"), 10, 64)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
			return
		}
		var user database.User
		if err := database.DB.First(&user, id).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "User not found"})
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"user_id":       user.ID,
			"username":      user.Username,
			"total":         user.TrafficUsed,
			"traffic_limit": user.TrafficLimit,
			"months":        service.UserMonthlyHistory(user.ID, 24),
		})
	}
}

// GET /api/traffic/inbounds?from=YYYY-MM-DD&to=YYYY-MM-DD — трафик по
// подключениям по дням (по умолчанию последние 7 дней).
func GetTrafficInbounds() gin.HandlerFunc {
	return func(c *gin.Context) {
		now := time.Now()
		from := c.DefaultQuery("from", now.AddDate(0, 0, -6).Format("2006-01-02"))
		to := c.DefaultQuery("to", now.AddDate(0, 0, 1).Format("2006-01-02"))
		if !dayRe.MatchString(from) || !dayRe.MatchString(to) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "from/to must be YYYY-MM-DD"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"from": from, "to": to, "rows": service.InboundTrafficRange(from, to)})
	}
}
