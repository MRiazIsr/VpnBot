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

			// Config reload
			auth.POST("/reload", handlers.ReloadConfig())

			// Inbounds
			auth.GET("/inbounds", handlers.GetInbounds())
			auth.POST("/inbounds", handlers.CreateInbound())
			auth.PUT("/inbounds/:id", handlers.UpdateInbound())
			auth.DELETE("/inbounds/:id", handlers.DeleteInbound())
			auth.PUT("/inbounds/:id/toggle", handlers.ToggleInbound())
			auth.GET("/inbounds/validate-sni", handlers.ValidateSNI())

			// Stats
			auth.GET("/stats", handlers.GetStats())
		}
	}

	// Public subscription endpoint
	r.GET("/sub/:token", handlers.GetSubscription())
}
