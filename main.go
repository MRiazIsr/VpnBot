package main

import (
	"log"
	"os"
	"strconv"
	"vpnbot/bot"
	"vpnbot/database"
	"vpnbot/service"

	"github.com/gin-gonic/gin"
)

// CORSMiddleware разрешает запросы с фронтенда
func CORSMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Writer.Header().Set("Access-Control-Allow-Origin", "*")
		c.Writer.Header().Set("Access-Control-Allow-Credentials", "true")
		c.Writer.Header().Set("Access-Control-Allow-Headers", "Content-Type, Content-Length, Accept-Encoding, X-CSRF-Token, Authorization, accept, origin, Cache-Control, X-Requested-With")
		c.Writer.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS, GET, PUT, DELETE")

		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(204)
			return
		}

		c.Next()
	}
}

func main() {
	// 1. Инициализация БД
	database.Init("vpn.db")

	// 2. Генерация конфига при старте
	err := service.GenerateAndReload()
	if err != nil {
		log.Println("Error generating initial config:", err)
	}

	// 3. Запуск Telegram бота
	botToken := os.Getenv("BOT_TOKEN")
	adminID := int64(124343839)

	if botToken != "" {
		go bot.Start(botToken, adminID)
	} else {
		log.Println("BOT_TOKEN not set, skipping bot start")
	}

	// 4. HTTP API Server
	r := gin.Default()
	r.Use(CORSMiddleware()) // Включаем CORS

	api := r.Group("/api")
	{
		// Получить всех пользователей
		api.GET("/users", func(c *gin.Context) {
			var users []database.User
			database.DB.Find(&users)
			c.JSON(200, users)
		})

		// Создать пользователя (если нужно вручную)
		api.POST("/users", func(c *gin.Context) {
			// Логику можно добавить позже
		})

		// Банить/Разбанивать пользователя
		api.PUT("/users/:id/status", func(c *gin.Context) {
			idStr := c.Param("id")
			id, err := strconv.Atoi(idStr)
			if err != nil {
				c.JSON(400, gin.H{"error": "Invalid ID"})
				return
			}

			var user database.User
			if err := database.DB.First(&user, id).Error; err != nil {
				c.JSON(404, gin.H{"error": "User not found"})
				return
			}

			// Читаем новый статус из JSON тела запроса
			var input struct {
				Status string `json:"status"` // "active" или "banned"
			}
			if err := c.ShouldBindJSON(&input); err != nil {
				c.JSON(400, gin.H{"error": "Invalid input"})
				return
			}

			// Обновляем статус
			user.Status = input.Status
			database.DB.Save(&user)

			// Перезагружаем Sing-box, чтобы применить блокировку
			service.GenerateAndReload()

			c.JSON(200, user)
		})
	}

	// Subscription URL
	r.GET("/sub/:token", func(c *gin.Context) {
		token := c.Param("token")
		var user database.User
		if err := database.DB.Where("subscription_token = ?", token).First(&user).Error; err != nil {
			c.String(404, "Invalid token")
			return
		}
		c.String(200, "TODO: Return Base64 VLESS config list")
	})

	log.Println("Server starting on :8085")
	err = r.Run(":8085")
	if err != nil {
		return
	}
}
