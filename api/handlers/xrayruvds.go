package handlers

import (
	"strconv"
	"vpnbot/service"

	"github.com/gin-gonic/gin"
)

// POST /api/xray/ruvds/setup — установка Xray-сайдкара на RuVDS (пин service.XrayVersion)
func SetupXrayRuVDS() gin.HandlerFunc {
	return func(c *gin.Context) {
		if err := service.SetupXrayRuVDS(); err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, gin.H{"message": "Xray установлен на RuVDS", "version": service.XrayVersion})
	}
}

// POST /api/xray/ruvds/reload — перегенерация sing-box + Xray и деплой
func ReloadXrayRuVDS() gin.HandlerFunc {
	return func(c *gin.Context) {
		if err := service.GenerateAndReloadRuVDS(); err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, gin.H{"message": "RuVDS sing-box и Xray перезагружены"})
	}
}

// POST /api/xray/ruvds/start
func StartXrayRuVDS() gin.HandlerFunc {
	return func(c *gin.Context) {
		if err := service.StartXrayRuVDS(); err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, gin.H{"message": "Xray запущен на RuVDS"})
	}
}

// POST /api/xray/ruvds/stop
func StopXrayRuVDS() gin.HandlerFunc {
	return func(c *gin.Context) {
		if err := service.StopXrayRuVDS(); err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, gin.H{"message": "Xray остановлен на RuVDS"})
	}
}

// GET /api/xray/ruvds/status
func GetXrayRuVDSStatus() gin.HandlerFunc {
	return func(c *gin.Context) {
		status := "stopped"
		if service.IsXrayRuVDSRunning() {
			status = "running"
		}
		if !service.IsPortForwardConfigured() {
			status = "not_configured"
		}
		c.JSON(200, gin.H{
			"status":            status,
			"installed_version": service.XrayRuVDSVersion(),
			"pinned_version":    service.XrayVersion,
			"ruvds_ip":          service.GetRuVDSIP(),
		})
	}
}

// GET /api/xray/ruvds/config — preview конфига Xray (204, если масок нет)
func PreviewXrayRuVDSConfig() gin.HandlerFunc {
	return func(c *gin.Context) {
		cfg, err := service.GenerateXrayRuVDSConfig()
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

// GET /api/xray/ruvds/logs?lines=50
func GetXrayRuVDSLogs() gin.HandlerFunc {
	return func(c *gin.Context) {
		lines, _ := strconv.Atoi(c.DefaultQuery("lines", "50"))
		out, err := service.XrayRuVDSLogs(lines)
		if err != nil {
			c.JSON(500, gin.H{"error": err.Error(), "logs": out})
			return
		}
		c.JSON(200, gin.H{"logs": out})
	}
}
