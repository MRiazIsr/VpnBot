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

// GET /api/telemt/ruvds/logs — журнал systemd + текущий toml с RuVDS
func GetTelemtRuVDSLogs() gin.HandlerFunc {
	return func(c *gin.Context) {
		out, err := service.TelemtRuVDSLogs(80)
		if err != nil {
			c.JSON(500, gin.H{"error": err.Error(), "logs": out})
			return
		}
		c.Data(200, "text/plain; charset=utf-8", []byte(out))
	}
}

// GET /api/telemt/ruvds/diagnose — структурированный отчёт о состоянии telemt
// для расследования жалоб клиентов на нестабильность (версия + конфиг +
// счётчик handshake timeouts + проба egress до Telegram DC).
func DiagnoseTelemtRuVDS() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !service.IsPortForwardConfigured() {
			c.JSON(400, gin.H{"error": "RUVDS_IP не задан"})
			return
		}
		d, err := service.GetTelemtRuVDSDiagnostics()
		if err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, d)
	}
}

// POST /api/telemt/ruvds/upgrade — форс-апгрейд telemt до TelemtPinnedVersion.
// Останавливает сервис, бэкапит бинарник, скачивает новый, запускает; при сбое — откат.
func UpgradeTelemtRuVDS() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !service.IsPortForwardConfigured() {
			c.JSON(400, gin.H{"error": "RUVDS_IP не задан"})
			return
		}
		if err := service.UpgradeTelemtRuVDS(); err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, gin.H{
			"message": "telemt обновлён на " + service.TelemtPinnedVersion,
			"version": service.TelemtPinnedVersion,
		})
	}
}
