package handlers

import (
	"vpnbot/service"

	"github.com/gin-gonic/gin"
)

// POST /api/singbox/ruvds/setup — полный цикл установки sing-box на RuVDS
func SetupSingboxRuVDS() gin.HandlerFunc {
	return func(c *gin.Context) {
		if err := service.SetupSingboxRuVDS(); err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, gin.H{"message": "sing-box установлен и запущен на RuVDS"})
	}
}

// POST /api/singbox/ruvds/reload — перегенерация конфига и reload
func ReloadSingboxRuVDS() gin.HandlerFunc {
	return func(c *gin.Context) {
		if err := service.GenerateAndReloadRuVDS(); err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, gin.H{"message": "RuVDS sing-box перезагружен"})
	}
}

// POST /api/singbox/ruvds/start
func StartSingboxRuVDS() gin.HandlerFunc {
	return func(c *gin.Context) {
		if err := service.StartSingboxRuVDS(); err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, gin.H{"message": "sing-box запущен на RuVDS"})
	}
}

// POST /api/singbox/ruvds/stop
func StopSingboxRuVDS() gin.HandlerFunc {
	return func(c *gin.Context) {
		if err := service.StopSingboxRuVDS(); err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, gin.H{"message": "sing-box остановлен на RuVDS"})
	}
}

// GET /api/singbox/ruvds/status
func GetSingboxRuVDSStatus() gin.HandlerFunc {
	return func(c *gin.Context) {
		running := service.IsSingboxRuVDSRunning()
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
			"enabled":  service.IsRuVDSEnabled(),
		})
	}
}

// GET /api/singbox/ruvds/config — preview сгенерированного config.json
func PreviewSingboxRuVDSConfig() gin.HandlerFunc {
	return func(c *gin.Context) {
		cfgJSON, err := service.GenerateRuVDSConfig()
		if err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
		c.Data(200, "application/json", cfgJSON)
	}
}

// GET /api/singbox/ruvds/logs — журнал systemd с RuVDS
func GetSingboxRuVDSLogs() gin.HandlerFunc {
	return func(c *gin.Context) {
		out, err := service.SingboxRuVDSLogs(80)
		if err != nil {
			c.JSON(500, gin.H{"error": err.Error(), "logs": out})
			return
		}
		c.Data(200, "text/plain; charset=utf-8", []byte(out))
	}
}

// POST /api/singbox/ruvds/rollback — emergency: остановить sing-box на RuVDS
// + восстановить iptables DNAT (откат к схеме «Client → RuVDS DNAT → Hetzner»).
// Используется когда новая схема сломалась и нужно срочно вернуть старое поведение.
func RollbackRuVDSSingbox() gin.HandlerFunc {
	return func(c *gin.Context) {
		// Останавливаем RuVDS sing-box чтобы не было путаницы с портами
		service.StopSingboxRuVDS()
		// Восстанавливаем iptables DNAT
		if err := service.RestoreRuVDSDNAT(); err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, gin.H{
			"message": "Откат выполнен: RuVDS sing-box остановлен, iptables DNAT восстановлен. Старые подписки снова работают через релэй.",
		})
	}
}
