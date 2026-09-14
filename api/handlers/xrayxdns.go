package handlers

import (
	"strconv"
	"vpnbot/service"

	"github.com/gin-gonic/gin"
)

// POST /api/xray/xdns/setup
func SetupXrayXDNS() gin.HandlerFunc {
	return func(c *gin.Context) {
		if err := service.SetupXrayXDNS(); err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, gin.H{"message": "XDNS установлен и запущен на Hetzner", "version": service.XrayVersion})
	}
}

// POST /api/xray/xdns/reload
func ReloadXrayXDNS() gin.HandlerFunc {
	return func(c *gin.Context) {
		if err := service.GenerateAndReload(); err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, gin.H{"message": "XDNS перегенерирован"})
	}
}

// POST /api/xray/xdns/start
func StartXrayXDNS() gin.HandlerFunc {
	return func(c *gin.Context) {
		if err := service.StartXrayXDNS(); err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, gin.H{"message": "XDNS запущен"})
	}
}

// POST /api/xray/xdns/stop
func StopXrayXDNS() gin.HandlerFunc {
	return func(c *gin.Context) {
		if err := service.StopXrayXDNS(); err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, gin.H{"message": "XDNS остановлен"})
	}
}

// GET /api/xray/xdns/status
func GetXrayXDNSStatus() gin.HandlerFunc {
	return func(c *gin.Context) {
		status := "stopped"
		if service.IsXrayXDNSRunning() {
			status = "running"
		}
		c.JSON(200, gin.H{
			"status":            status,
			"installed_version": service.XrayXDNSVersion(),
			"pinned_version":    service.XrayVersion,
			"port53_owner":      service.Port53Owner(),
		})
	}
}

// GET /api/xray/xdns/config
func PreviewXrayXDNSConfig() gin.HandlerFunc {
	return func(c *gin.Context) {
		cfg, err := service.GenerateXDNSConfig()
		if err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
		if cfg == nil {
			c.Status(204)
			return
		}
		c.Data(200, "application/json", cfg)
	}
}

// GET /api/xray/xdns/logs?lines=50
func GetXrayXDNSLogs() gin.HandlerFunc {
	return func(c *gin.Context) {
		lines, _ := strconv.Atoi(c.DefaultQuery("lines", "50"))
		out, err := service.XrayXDNSLogs(lines)
		if err != nil {
			c.JSON(500, gin.H{"error": err.Error(), "logs": out})
			return
		}
		c.JSON(200, gin.H{"logs": out})
	}
}
