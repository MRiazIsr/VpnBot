package main

import (
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

// Секретный ключ для подписи JWT (в реальном проекте лучше тоже в ENV)
var jwtSecret = []byte(os.Getenv("JWT_SECRET"))

func init() {
	if len(jwtSecret) == 0 {
		jwtSecret = []byte("default-secret-key-change-me")
	}
}

// Структура для логина
type LoginRequest struct {
	Password string `json:"password" binding:"required"`
}

// Middleware для CORS
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

// Middleware для проверки авторизации
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
	r.Use(CORSMiddleware())

	api := r.Group("/api")
	{
		// Публичный роут для логина
		api.POST("/login", func(c *gin.Context) {
			var loginReq LoginRequest
			if err := c.ShouldBindJSON(&loginReq); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request"})
				return
			}

			adminPassword := os.Getenv("ADMIN_PASSWORD")
			if adminPassword == "" {
				// Если пароль не задан, логин невозможен (безопасность)
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Server admin password not configured"})
				return
			}

			if loginReq.Password != adminPassword {
				c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid password"})
				return
			}

			// Генерация токена
			token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
				"admin": true,
				"exp":   time.Now().Add(time.Hour * 24).Unix(), // Токен на 24 часа
			})

			tokenString, err := token.SignedString(jwtSecret)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Could not generate token"})
				return
			}

			c.JSON(http.StatusOK, gin.H{"token": tokenString})
		})

		// Защищенные роуты
		authorized := api.Group("/")
		authorized.Use(AuthMiddleware())
		{
			// Получить всех пользователей
			authorized.GET("/users", func(c *gin.Context) {
				var users []database.User
				database.DB.Find(&users)
				c.JSON(200, users)
			})

			// Банить/Разбанивать пользователя
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
		}
	}

	// Subscription URL (публичный, без токена)
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
