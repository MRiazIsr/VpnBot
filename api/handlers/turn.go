package handlers

import (
	"vpnbot/database"
	"vpnbot/service"

	"github.com/gin-gonic/gin"
)

// GET /api/turn/config — получить настройки TURN
func GetTurnConfig() gin.HandlerFunc {
	return func(c *gin.Context) {
		var cfg database.TurnConfig
		if err := database.DB.First(&cfg).Error; err != nil {
			c.JSON(200, database.TurnConfig{
				TunnelPort:  56000,
				ForwardPort: 8444,
				Streams:     16,
			})
			return
		}
		c.JSON(200, cfg)
	}
}

// PUT /api/turn/config — обновить настройки
func UpdateTurnConfig() gin.HandlerFunc {
	return func(c *gin.Context) {
		var input struct {
			Enabled     bool   `json:"enabled"`
			VKToken     string `json:"vk_token"`
			VKJoinLink  string `json:"vk_join_link"`
			TunnelPort  int    `json:"tunnel_port"`
			ForwardPort int    `json:"forward_port"`
			Streams     int    `json:"streams"`
		}
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(400, gin.H{"error": "Invalid input"})
			return
		}

		if input.TunnelPort == 0 {
			input.TunnelPort = 56000
		}
		if input.ForwardPort == 0 {
			input.ForwardPort = 8444
		}
		if input.Streams == 0 {
			input.Streams = 16
		}

		var cfg database.TurnConfig
		result := database.DB.First(&cfg)

		if result.Error != nil {
			cfg = database.TurnConfig{
				Enabled:     input.Enabled,
				VKToken:     input.VKToken,
				VKJoinLink:  input.VKJoinLink,
				TunnelPort:  input.TunnelPort,
				ForwardPort: input.ForwardPort,
				Streams:     input.Streams,
			}
			database.DB.Create(&cfg)
		} else {
			cfg.Enabled = input.Enabled
			if input.VKToken != "" {
				cfg.VKToken = input.VKToken
			}
			if input.VKJoinLink != "" {
				cfg.VKJoinLink = input.VKJoinLink
			}
			cfg.TunnelPort = input.TunnelPort
			cfg.ForwardPort = input.ForwardPort
			cfg.Streams = input.Streams
			database.DB.Save(&cfg)
		}

		c.JSON(200, cfg)
	}
}

// POST /api/turn/setup — полная установка
func SetupTurn() gin.HandlerFunc {
	return func(c *gin.Context) {
		if err := service.SetupTurnProxy(); err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, gin.H{"message": "VK TURN tunnel настроен и запущен"})
	}
}

// POST /api/turn/start — запустить сервис
func StartTurn() gin.HandlerFunc {
	return func(c *gin.Context) {
		if err := service.StartTurnProxy(); err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, gin.H{"message": "VK TURN tunnel запущен"})
	}
}

// POST /api/turn/stop — остановить сервис
func StopTurn() gin.HandlerFunc {
	return func(c *gin.Context) {
		if err := service.StopTurnProxy(); err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, gin.H{"message": "VK TURN tunnel остановлен"})
	}
}

// GET /api/turn/status — статус сервиса
func GetTurnStatus() gin.HandlerFunc {
	return func(c *gin.Context) {
		running := service.IsTurnProxyRunning()
		status := "stopped"
		if running {
			status = "running"
		}

		var cfg database.TurnConfig
		database.DB.First(&cfg)

		c.JSON(200, gin.H{
			"status":       status,
			"vk_join_link": cfg.VKJoinLink,
			"tunnel_port":  cfg.TunnelPort,
			"forward_port": cfg.ForwardPort,
			"streams":      cfg.Streams,
			"status_msg":   cfg.StatusMsg,
		})
	}
}

// POST /api/turn/create-call — создать VK-звонок
func CreateVKCall() gin.HandlerFunc {
	return func(c *gin.Context) {
		var cfg database.TurnConfig
		if err := database.DB.First(&cfg).Error; err != nil {
			c.JSON(400, gin.H{"error": "TURN конфиг не найден"})
			return
		}

		if cfg.VKToken == "" {
			c.JSON(400, gin.H{"error": "VK токен не задан"})
			return
		}

		joinLink, callID, err := service.CreateVKCall(cfg.VKToken)
		if err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}

		cfg.VKJoinLink = joinLink
		cfg.VKCallID = callID
		database.DB.Save(&cfg)

		c.JSON(200, gin.H{
			"join_link": joinLink,
			"call_id":   callID,
		})
	}
}

// POST /api/turn/test-creds — протестировать credential harvesting
func TestTurnCreds() gin.HandlerFunc {
	return func(c *gin.Context) {
		var cfg database.TurnConfig
		if err := database.DB.First(&cfg).Error; err != nil || cfg.VKJoinLink == "" {
			c.JSON(400, gin.H{"error": "VK ссылка не задана"})
			return
		}

		turnServer, err := service.TestTurnCreds(cfg.VKJoinLink)
		if err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}

		c.JSON(200, gin.H{
			"turn_server": turnServer,
			"message":     "Credentials работают",
		})
	}
}
