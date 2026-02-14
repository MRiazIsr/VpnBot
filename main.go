package main

import (
	"encoding/base64"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
	"vpnbot/bot"
	"vpnbot/database"
	"vpnbot/service"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

var jwtSecret = []byte(os.Getenv("JWT_SECRET"))

func init() {
	if len(jwtSecret) == 0 {
		jwtSecret = []byte("default-secret-key-change-me")
	}
}

type LoginRequest struct {
	Password string `json:"password" binding:"required"`
}

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

func AuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Authorization header required"})
			return
		}

		tokenString := strings.TrimPrefix(authHeader, "Bearer ")
		token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
			if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
			}
			return jwtSecret, nil
		})

		if err != nil || !token.Valid {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Invalid token"})
			return
		}

		c.Next()
	}
}

func main() {
	database.Init("vpn.db")

	err := service.GenerateAndReload()
	if err != nil {
		log.Println("Error generating initial config:", err)
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
	r.Use(CORSMiddleware())

	api := r.Group("/api")
	{
		api.POST("/login", func(c *gin.Context) {
			var loginReq LoginRequest
			if err := c.ShouldBindJSON(&loginReq); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request"})
				return
			}

			adminPassword := os.Getenv("ADMIN_PASSWORD")
			if adminPassword == "" {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Server admin password not configured"})
				return
			}

			if loginReq.Password != adminPassword {
				c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid password"})
				return
			}

			token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
				"admin": true,
				"exp":   time.Now().Add(time.Hour * 24).Unix(),
			})

			tokenString, err := token.SignedString(jwtSecret)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Could not generate token"})
				return
			}

			c.JSON(http.StatusOK, gin.H{"token": tokenString})
		})

		authorized := api.Group("/")
		authorized.Use(AuthMiddleware())
		{
			authorized.GET("/users", func(c *gin.Context) {
				var users []database.User
				database.DB.Find(&users)
				c.JSON(200, users)
			})

			authorized.PUT("/users/:id/status", func(c *gin.Context) {
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
			})

			// --- НОВЫЙ МЕТОД: Обновление лимита ---
			authorized.PUT("/users/:id/limit", func(c *gin.Context) {
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

				var input struct {
					Limit int64 `json:"limit"` // Лимит в байтах
				}
				if err := c.ShouldBindJSON(&input); err != nil {
					c.JSON(400, gin.H{"error": "Invalid input"})
					return
				}

				// Обновляем лимит
				user.TrafficLimit = input.Limit

				// Умная логика: если юзер был expired, но новый лимит позволяет работать — активируем
				// (0 = безлимит, либо новый лимит > использованного)
				if user.Status == "expired" {
					if user.TrafficLimit == 0 || user.TrafficUsed < user.TrafficLimit {
						user.Status = "active"
						// Так как статус изменился, нужно перезагрузить конфиг VLESS
						service.GenerateAndReload()
					}
				}

				database.DB.Save(&user)
				c.JSON(200, user)
			})
			// -------------------------------------

			authorized.POST("/users/sync", func(c *gin.Context) {
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
			})
		}
	}

	r.GET("/sub/:token", func(c *gin.Context) {
		token := c.Param("token")

		// 1. Найти пользователя по subscription_token
		var user database.User
		if err := database.DB.Where("subscription_token = ?", token).First(&user).Error; err != nil {
			c.String(404, "Not found")
			return
		}

		// 2. Проверить статус
		if user.Status != "active" {
			c.String(404, "Not found")
			return
		}

		// 3. Загрузить SystemSettings
		var settings database.SystemSettings
		database.DB.First(&settings)

		// 4. Определить serverIP
		serverIP := os.Getenv("SERVER_IP")
		if serverIP == "" {
			serverIP = "49.13.201.110"
		}

		// 5. Собрать массив ссылок
		mainAddr := serverIP
		if settings.ServerDomain != "" {
			mainAddr = settings.ServerDomain
		}

		links := []string{
			service.GenerateLink(user, settings, mainAddr),
			service.GenerateLinkAntiCensorship(user, settings, serverIP),
			service.GenerateLinkGRPC(user, settings, serverIP),
			service.GenerateLinkHysteria2(user, serverIP),
		}

		// 6. Склеить через \n и закодировать в base64
		body := base64.StdEncoding.EncodeToString([]byte(strings.Join(links, "\n")))

		// 7. Вернуть с заголовками
		c.Header("Content-Type", "text/plain")
		c.Header("Profile-Update-Interval", "6")
		c.Header("Subscription-Userinfo", fmt.Sprintf("upload=0; download=%d; total=%d", user.TrafficUsed, user.TrafficLimit))
		c.String(200, body)
	})

	r.GET("/sub/:token/bypass", func(c *gin.Context) {
		token := c.Param("token")

		var user database.User
		if err := database.DB.Where("subscription_token = ?", token).First(&user).Error; err != nil {
			c.String(404, "Not found")
			return
		}

		if user.Status != "active" {
			c.String(404, "Not found")
			return
		}

		var settings database.SystemSettings
		database.DB.First(&settings)

		serverIP := os.Getenv("SERVER_IP")
		if serverIP == "" {
			serverIP = "49.13.201.110"
		}

		bypassAddr := settings.BypassDomain
		if bypassAddr == "" {
			bypassAddr = serverIP
		}

		links := []string{
			service.GenerateLinkBypass(user, settings, bypassAddr),
			service.GenerateLinkAntiCensorship(user, settings, serverIP),
			service.GenerateLinkGRPC(user, settings, serverIP),
			service.GenerateLinkHysteria2(user, serverIP),
		}

		body := base64.StdEncoding.EncodeToString([]byte(strings.Join(links, "\n")))

		c.Header("Content-Type", "text/plain")
		c.Header("Profile-Update-Interval", "6")
		c.Header("Subscription-Userinfo", fmt.Sprintf("upload=0; download=%d; total=%d", user.TrafficUsed, user.TrafficLimit))
		c.String(200, body)
	})

	log.Println("Server starting on :8085")
	err = r.Run(":8085")
	if err != nil {
		return
	}
}
