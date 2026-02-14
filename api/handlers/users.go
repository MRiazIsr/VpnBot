package handlers

import (
	"fmt"
	"net/http"
	"strconv"
	"vpnbot/bot"
	"vpnbot/database"
	"vpnbot/service"

	"github.com/gin-gonic/gin"
)

func GetUsers() gin.HandlerFunc {
	return func(c *gin.Context) {
		var users []database.User
		database.DB.Find(&users)
		c.JSON(200, users)
	}
}

func UpdateUserStatus() gin.HandlerFunc {
	return func(c *gin.Context) {
		id, err := strconv.Atoi(c.Param("id"))
		if err != nil {
			c.JSON(400, gin.H{"error": "Invalid ID"})
			return
		}

		var user database.User
		if err := database.DB.First(&user, id).Error; err != nil {
			c.JSON(404, gin.H{"error": "User not found"})
			return
		}

		var input struct {
			Status string `json:"status"`
		}
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(400, gin.H{"error": "Invalid input"})
			return
		}

		user.Status = input.Status
		database.DB.Save(&user)
		service.GenerateAndReload()

		c.JSON(200, user)
	}
}

func UpdateUserLimit() gin.HandlerFunc {
	return func(c *gin.Context) {
		id, err := strconv.Atoi(c.Param("id"))
		if err != nil {
			c.JSON(400, gin.H{"error": "Invalid ID"})
			return
		}

		var user database.User
		if err := database.DB.First(&user, id).Error; err != nil {
			c.JSON(404, gin.H{"error": "User not found"})
			return
		}

		var input struct {
			Limit int64 `json:"limit"`
		}
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(400, gin.H{"error": "Invalid input"})
			return
		}

		user.TrafficLimit = input.Limit

		if user.Status == "expired" {
			if user.TrafficLimit == 0 || user.TrafficUsed < user.TrafficLimit {
				user.Status = "active"
				service.GenerateAndReload()
			}
		}

		database.DB.Save(&user)
		c.JSON(200, user)
	}
}

func DeleteUser() gin.HandlerFunc {
	return func(c *gin.Context) {
		id, err := strconv.Atoi(c.Param("id"))
		if err != nil {
			c.JSON(400, gin.H{"error": "Invalid ID"})
			return
		}

		var user database.User
		if err := database.DB.First(&user, id).Error; err != nil {
			c.JSON(404, gin.H{"error": "User not found"})
			return
		}

		database.DB.Delete(&user)
		service.GenerateAndReload()

		c.JSON(200, gin.H{"message": "User deleted"})
	}
}

func SyncUsers() gin.HandlerFunc {
	return func(c *gin.Context) {
		if bot.Bot == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Bot is not initialized"})
			return
		}

		var users []database.User
		database.DB.Find(&users)

		updatedCount := 0
		for _, u := range users {
			if u.TelegramID == 0 {
				continue
			}
			chat, err := bot.Bot.ChatByID(u.TelegramID)
			if err == nil {
				realUsername := chat.Username
				if realUsername != "" && u.TelegramUsername != realUsername {
					u.TelegramUsername = realUsername
					database.DB.Save(&u)
					updatedCount++
				}
			}
		}

		c.JSON(http.StatusOK, gin.H{
			"status":        "success",
			"updated_users": updatedCount,
			"message":       fmt.Sprintf("Successfully synced %d users", updatedCount),
		})
	}
}
