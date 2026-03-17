package handlers

import (
	"vpnbot/database"
	"vpnbot/service"

	"github.com/gin-gonic/gin"
)

// GET /api/telemt/config — получить настройки TelemetConfig
func GetTelemetConfig() gin.HandlerFunc {
	return func(c *gin.Context) {
		var cfg database.TelemetConfig
		if err := database.DB.First(&cfg).Error; err != nil {
			// Конфига ещё нет — возвращаем дефолтный
			c.JSON(200, database.TelemetConfig{
				Port:      443,
				TLSDomain: "dl.google.com",
			})
			return
		}
		c.JSON(200, cfg)
	}
}

// POST /api/telemt/config — создать/обновить настройки
func UpdateTelemetConfig() gin.HandlerFunc {
	return func(c *gin.Context) {
		var input struct {
			Enabled       bool   `json:"enabled"`
			Port          int    `json:"port"`
			TLSDomain     string `json:"tls_domain"`
			ServerAddress string `json:"server_address"`
			ProxyTag      string `json:"proxy_tag"`
		}
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(400, gin.H{"error": "Invalid input"})
			return
		}

		if input.Port == 0 {
			input.Port = 443
		}

		var cfg database.TelemetConfig
		result := database.DB.First(&cfg)

		if result.Error != nil {
			// Создаём новый
			cfg = database.TelemetConfig{
				Enabled:       input.Enabled,
				Port:          input.Port,
				TLSDomain:     input.TLSDomain,
				ServerAddress: input.ServerAddress,
				ProxyTag:      input.ProxyTag,
			}
			database.DB.Create(&cfg)
		} else {
			// Обновляем существующий
			cfg.Enabled = input.Enabled
			cfg.Port = input.Port
			cfg.TLSDomain = input.TLSDomain
			cfg.ServerAddress = input.ServerAddress
			cfg.ProxyTag = input.ProxyTag
			database.DB.Save(&cfg)
		}

		warning := ""
		if input.Enabled && input.ProxyTag == "" {
			warning = "proxy_tag не задан — middle proxy не будет работать, прокси будет работать в режиме прямого подключения"
		}

		c.JSON(200, gin.H{
			"config":  cfg,
			"warning": warning,
		})
	}
}

// POST /api/telemt/setup — полный цикл настройки
func SetupTelemet() gin.HandlerFunc {
	return func(c *gin.Context) {
		if err := service.SetupTelemet(); err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, gin.H{"message": "telemt настроен и запущен"})
	}
}

// POST /api/telemt/stop — остановить telemt
func StopTelemet() gin.HandlerFunc {
	return func(c *gin.Context) {
		if err := service.StopTelemet(); err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, gin.H{"message": "telemt остановлен"})
	}
}

// GET /api/telemt/status — статус сервиса
func GetTelemetStatus() gin.HandlerFunc {
	return func(c *gin.Context) {
		running := service.IsTelemetRunning()
		status := "stopped"
		if running {
			status = "running"
		}

		var userCount int64
		database.DB.Model(&database.TelemetUser{}).Count(&userCount)

		c.JSON(200, gin.H{
			"status":     status,
			"user_count": userCount,
		})
	}
}

// GET /api/telemt/users — список TelemetUser
func GetTelemetUsers() gin.HandlerFunc {
	return func(c *gin.Context) {
		var users []database.TelemetUser
		database.DB.Preload("User").Find(&users)
		c.JSON(200, users)
	}
}

// POST /api/telemt/sync — принудительная синхронизация юзеров
func SyncTelemetUsers() gin.HandlerFunc {
	return func(c *gin.Context) {
		service.SyncTelemetUsers()
		service.GenerateAndReloadTelemet()
		c.JSON(200, gin.H{"message": "Пользователи telemt синхронизированы"})
	}
}
