package handlers

import (
	"vpnbot/service"

	"github.com/gin-gonic/gin"
)

// POST /api/telemt/ruvds/setup
func SetupTelemtRuVDS() gin.HandlerFunc {
	return func(c *gin.Context) {
		if err := service.SetupTelemtRuVDS(); err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, gin.H{"message": "telemt установлен и запущен на RuVDS"})
	}
}

// POST /api/telemt/ruvds/reload
func ReloadTelemtRuVDS() gin.HandlerFunc {
	return func(c *gin.Context) {
		if err := service.GenerateAndReloadTelemtRuVDS(); err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, gin.H{"message": "telemt на RuVDS перезагружен"})
	}
}

// POST /api/telemt/ruvds/start
func StartTelemtRuVDS() gin.HandlerFunc {
	return func(c *gin.Context) {
		if err := service.StartTelemtRuVDS(); err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, gin.H{"message": "telemt запущен на RuVDS"})
	}
}

// POST /api/telemt/ruvds/stop
func StopTelemtRuVDS() gin.HandlerFunc {
	return func(c *gin.Context) {
		if err := service.StopTelemtRuVDS(); err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, gin.H{"message": "telemt остановлен на RuVDS"})
	}
}

// GET /api/telemt/ruvds/status
func GetTelemtRuVDSStatus() gin.HandlerFunc {
	return func(c *gin.Context) {
		running := service.IsTelemtRuVDSRunning()
		status := "stopped"
		if running {
			status = "running"
		}
		if !service.IsPortForwardConfigured() {
			status = "not_configured"
		}
		c.JSON(200, gin.H{
			"status":   status,
			"ruvds_ip": service.GetRuVDSIP(),
		})
	}
}
