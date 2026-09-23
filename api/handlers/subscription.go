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

// inSubscription — попадает ли инбаунд в подписки /sub и /sub-ruvds.
// Не попадают (но остаются в боте):
//   - mask и xdns: ссылку с fm / JSON-конфиг понимают только Xray-клиенты
//     (v2rayNG/Happ/Streisand), у части базы Hiddify — строку в подписке они
//     не откроют; раздаются кнопкой в боте;
//   - инбаунды с выходом direct (RU*): российский IP, Instagram/WhatsApp/
//     Telegram через них не работают, а пользователь видит «VPN сломан».
func inSubscription(ib database.InboundConfig) bool {
	if ib.Protocol == "mask" || ib.Protocol == "xdns" {
		return false
	}
	return ib.ExitOutbound != "direct"
}

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
			c.String(500, "SERVER_IP is not configured")
			return
		}

		var inbounds []database.InboundConfig
		database.DB.Where("enabled = ?", true).Order("sort_order").Find(&inbounds)

		links := []string{}
		for _, ib := range inbounds {
			if !inSubscription(ib) {
				continue
			}
			if link := service.GenerateLinkForInbound(ib, user, serverIP); link != "" {
				links = append(links, link)
			}
		}

		body := base64.StdEncoding.EncodeToString([]byte(strings.Join(links, "\n")))

		c.Header("Content-Type", "text/plain")
		c.Header("Profile-Update-Interval", "6")
		c.Header("Subscription-Userinfo", fmt.Sprintf("upload=0; download=%d; total=%d", user.TrafficUsed, user.TrafficLimit))
		c.String(200, body)
	}
}

// GetSubscriptionRuVDS — подписка с ссылками, указывающими на RuVDS-фронт.
// Используется когда WG-туннель настроен и sing-box работает на RuVDS.
// Существующий /sub/:token остаётся неизменным для backward compat.
func GetSubscriptionRuVDS() gin.HandlerFunc {
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

		ruvdsIP := service.GetRuVDSIP()
		if ruvdsIP == "" {
			c.String(503, "RuVDS not configured")
			return
		}

		var inbounds []database.InboundConfig
		database.DB.Where("enabled = ?", true).Order("sort_order").Find(&inbounds)

		links := []string{}
		for _, ib := range inbounds {
			if !inSubscription(ib) {
				continue
			}
			// Используем RuVDS IP вместо ServerAddress/SERVER_IP — клиент пойдёт на RuVDS.
			ibCopy := ib
			ibCopy.ServerAddress = "" // сбрасываем override чтобы serverAddr-параметр сработал
			if link := service.GenerateLinkForInbound(ibCopy, user, ruvdsIP); link != "" {
				links = append(links, link)
			}
		}

		body := base64.StdEncoding.EncodeToString([]byte(strings.Join(links, "\n")))

		c.Header("Content-Type", "text/plain")
		c.Header("Profile-Update-Interval", "6")
		c.Header("Subscription-Userinfo", fmt.Sprintf("upload=0; download=%d; total=%d", user.TrafficUsed, user.TrafficLimit))
		c.String(200, body)
	}
}
