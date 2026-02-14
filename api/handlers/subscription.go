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

		var settings database.SystemSettings
		database.DB.First(&settings)

		serverIP := os.Getenv("SERVER_IP")
		if serverIP == "" {
			serverIP = "49.13.201.110"
		}

		mainAddr := serverIP
		if settings.ServerDomain != "" {
			mainAddr = settings.ServerDomain
		}

		links := []string{
			service.GenerateLink(user, settings, mainAddr),
			service.GenerateLinkAntiCensorship(user, settings, serverIP),
			service.GenerateLinkGRPC(user, settings, serverIP),
			service.GenerateLinkHysteria2(user, serverIP),
		}

		body := base64.StdEncoding.EncodeToString([]byte(strings.Join(links, "\n")))

		c.Header("Content-Type", "text/plain")
		c.Header("Profile-Update-Interval", "6")
		c.Header("Subscription-Userinfo", fmt.Sprintf("upload=0; download=%d; total=%d", user.TrafficUsed, user.TrafficLimit))
		c.String(200, body)
	}
}

func GetSubscriptionBypass() gin.HandlerFunc {
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

		var settings database.SystemSettings
		database.DB.First(&settings)

		serverIP := os.Getenv("SERVER_IP")
		if serverIP == "" {
			serverIP = "49.13.201.110"
		}

		bypassAddr := settings.BypassDomain
		if bypassAddr == "" {
			bypassAddr = serverIP
		}

		links := []string{
			service.GenerateLinkBypass(user, settings, bypassAddr),
			service.GenerateLinkAntiCensorship(user, settings, serverIP),
			service.GenerateLinkGRPC(user, settings, serverIP),
			service.GenerateLinkHysteria2(user, serverIP),
		}

		body := base64.StdEncoding.EncodeToString([]byte(strings.Join(links, "\n")))

		c.Header("Content-Type", "text/plain")
		c.Header("Profile-Update-Interval", "6")
		c.Header("Subscription-Userinfo", fmt.Sprintf("upload=0; download=%d; total=%d", user.TrafficUsed, user.TrafficLimit))
		c.String(200, body)
	}
}
