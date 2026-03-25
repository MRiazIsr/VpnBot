package main

import (
	"log"
	"os"
	"strconv"
	"vpnbot/api/router"
	"vpnbot/bot"
	"vpnbot/database"
	"vpnbot/service"

	"github.com/gin-gonic/gin"
)

func main() {
	database.Init("vpn.db")

	err := service.GenerateAndReload()
	if err != nil {
		log.Println("Error generating initial config:", err)
	}

	// Настройка telemt (MTProto proxy) если включён
	if err := service.SetupTelemet(); err != nil {
		log.Println("Error setting up telemt:", err)
	}

	// Настройка VK TURN tunnel если включён
	if err := service.SetupTurnProxy(); err != nil {
		log.Println("Error setting up VK TURN tunnel:", err)
	}

	botToken := os.Getenv("BOT_TOKEN")
	adminID := int64(124343839)
	if envAdminID := os.Getenv("ADMIN_ID"); envAdminID != "" {
		if parsed, err := strconv.ParseInt(envAdminID, 10, 64); err == nil {
			adminID = parsed
		}
	}

	if botToken != "" {
		go bot.Start(botToken, adminID)
	} else {
		log.Println("BOT_TOKEN not set, skipping bot start")
	}

	r := gin.Default()
	router.SetupRouter(r)

	log.Println("Server starting on :8085")
	if err := r.Run(":8085"); err != nil {
		log.Fatal("Failed to start server:", err)
	}
}
