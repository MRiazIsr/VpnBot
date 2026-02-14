package handlers

import (
	"encoding/base64"
	"fmt"
	"os"
	"strings"
	"vpnbot/database"
	"vpnbot/service"

	"github.com/gin-gonic/gin"
)

func GetSubscription() gin.HandlerFunc {
	return func(c *gin.Context) {
		token := c.Param("token")

		var user database.User
		if err := database.DB.Where("subscription_token = ?", token).First(&user).Error; err != nil {
			c.String(404, "Not found")
			return
		}

		if user.Status != "active" {
			c.String(404, "Not found")
			return
		}

		serverIP := os.Getenv("SERVER_IP")
		if serverIP == "" {
			serverIP = "49.13.201.110"
		}

		var inbounds []database.InboundConfig
		database.DB.Where("enabled = ?", true).Order("sort_order").Find(&inbounds)

		links := []string{}
		for _, ib := range inbounds {
			links = append(links, service.GenerateLinkForInbound(ib, user, serverIP))
		}

		body := base64.StdEncoding.EncodeToString([]byte(strings.Join(links, "\n")))

		c.Header("Content-Type", "text/plain")
		c.Header("Profile-Update-Interval", "6")
		c.Header("Subscription-Userinfo", fmt.Sprintf("upload=0; download=%d; total=%d", user.TrafficUsed, user.TrafficLimit))
		c.String(200, body)
	}
}
