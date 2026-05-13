package handlers

import (
	"vpnbot/database"
	"vpnbot/service"

	"github.com/gin-gonic/gin"
)

// GET /api/wireguard/config
func GetWireGuardConfig() gin.HandlerFunc {
	return func(c *gin.Context) {
		cfg, err := service.GetWireGuardConfig()
		if err != nil {
			c.JSON(200, database.WireGuardConfig{
				ListenPort:  51820,
				HetznerWGIP: "10.8.0.1",
				RuVDSWGIP:   "10.8.0.2",
				MTU:         1408,
			})
			return
		}
		c.JSON(200, cfg)
	}
}

// PUT /api/wireguard/config
func UpdateWireGuardConfig() gin.HandlerFunc {
	return func(c *gin.Context) {
		var input struct {
			Enabled     bool   `json:"enabled"`
			ListenPort  int    `json:"listen_port"`
			HetznerWGIP string `json:"hetzner_wg_ip"`
			RuVDSWGIP   string `json:"ruvds_wg_ip"`
			MTU         int    `json:"mtu"`
		}
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(400, gin.H{"error": "Invalid input"})
			return
		}
		if input.ListenPort == 0 {
			input.ListenPort = 51820
		}
		if input.HetznerWGIP == "" {
			input.HetznerWGIP = "10.8.0.1"
		}
		if input.RuVDSWGIP == "" {
			input.RuVDSWGIP = "10.8.0.2"
		}
		if input.MTU == 0 {
			input.MTU = 1408
		}

		var cfg database.WireGuardConfig
		if err := database.DB.First(&cfg).Error; err != nil {
			cfg = database.WireGuardConfig{
				Enabled:     input.Enabled,
				ListenPort:  input.ListenPort,
				HetznerWGIP: input.HetznerWGIP,
				RuVDSWGIP:   input.RuVDSWGIP,
				MTU:         input.MTU,
			}
			database.DB.Create(&cfg)
		} else {
			cfg.Enabled = input.Enabled
			cfg.ListenPort = input.ListenPort
			cfg.HetznerWGIP = input.HetznerWGIP
			cfg.RuVDSWGIP = input.RuVDSWGIP
			cfg.MTU = input.MTU
			database.DB.Save(&cfg)
		}
		c.JSON(200, cfg)
	}
}

// POST /api/wireguard/setup — полный цикл настройки
func SetupWireGuard() gin.HandlerFunc {
	return func(c *gin.Context) {
		if err := service.SetupWireGuard(); err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, gin.H{"message": "WireGuard настроен и запущен на Hetzner"})
	}
}

// POST /api/wireguard/restart
func RestartWireGuard() gin.HandlerFunc {
	return func(c *gin.Context) {
		if err := service.RestartHetznerWG(); err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, gin.H{"message": "wg-quick@wg0 перезапущен"})
	}
}

// POST /api/wireguard/stop
func StopWireGuard() gin.HandlerFunc {
	return func(c *gin.Context) {
		if err := service.StopHetznerWG(); err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, gin.H{"message": "wg-quick@wg0 остановлен"})
	}
}

// GET /api/wireguard/status
func GetWireGuardStatus() gin.HandlerFunc {
	return func(c *gin.Context) {
		st, err := service.GetWGStatus()
		if err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, st)
	}
}
