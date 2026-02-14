package router

import (
	"os"
	"vpnbot/api/handlers"
	"vpnbot/api/middleware"

	"github.com/gin-gonic/gin"
)

func SetupRouter(r *gin.Engine) {
	jwtSecret := []byte(os.Getenv("JWT_SECRET"))
	if len(jwtSecret) == 0 {
		jwtSecret = []byte("default-secret-key-change-me")
	}

	r.Use(middleware.CORS())

	api := r.Group("/api")
	{
		api.POST("/login", handlers.Login(jwtSecret))

		auth := api.Group("/")
		auth.Use(middleware.Auth(jwtSecret))
		{
			// Users
			auth.GET("/users", handlers.GetUsers())
			auth.PUT("/users/:id/status", handlers.UpdateUserStatus())
			auth.PUT("/users/:id/limit", handlers.UpdateUserLimit())
			auth.DELETE("/users/:id", handlers.DeleteUser())
			auth.POST("/users/sync", handlers.SyncUsers())

			// Settings
			auth.GET("/settings", handlers.GetSettings())
			auth.PUT("/settings", handlers.UpdateSettings())
			auth.PUT("/settings/keys", handlers.UpdateKeys())
			auth.GET("/settings/validate-sni", handlers.ValidateSNI())

			// Stats
			auth.GET("/stats", handlers.GetStats())
		}
	}

	// Public subscription endpoints
	r.GET("/sub/:token", handlers.GetSubscription())
	r.GET("/sub/:token/bypass", handlers.GetSubscriptionBypass())
}
