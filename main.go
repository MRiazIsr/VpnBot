package main

import (
	"log"
	"os"
	"vpnbot/bot"
	"vpnbot/database"
	"vpnbot/service"

	"github.com/gin-gonic/gin"
)

func main() {
	// 1. Инициализация БД
	database.Init("vpn.db")

	// 2. Генерация конфига при старте (чтобы убедиться, что sing-box работает)
	err := service.GenerateAndReload()
	if err != nil {
		log.Println("Error generating initial config:", err)
	}

	// 3. Запуск Telegram бота в отдельной горутине
	botToken := os.Getenv("BOT_TOKEN")
	adminID := int64(124343839)

	if botToken != "" {
		go bot.Start(botToken, adminID)
	} else {
		log.Println("BOT_TOKEN not set, skipping bot start")
	}

	// 4. HTTP API Server (для Админки и Подписок)
	r := gin.Default()

	// API для админки
	api := r.Group("/api")
	{
		api.GET("/users", func(c *gin.Context) {
			var users []database.User
			database.DB.Find(&users)
			c.JSON(200, users)
		})

		api.POST("/users", func(c *gin.Context) {
			// Логика создания пользователя
		})
	}

	// Subscription URL (для приложений)
	r.GET("/sub/:token", func(c *gin.Context) {
		token := c.Param("token")
		var user database.User
		if err := database.DB.Where("subscription_token = ?", token).First(&user).Error; err != nil {
			c.String(404, "Invalid token")
			return
		}

		// Здесь мы должны отдать JSON или Base64 список ссылок
		// Для примера отдаем просто текст
		c.String(200, "TODO: Return Base64 VLESS config list")
	})

	log.Println("Server starting on :8080")
	err = r.Run(":8085")
	if err != nil {
		return
	}
}
