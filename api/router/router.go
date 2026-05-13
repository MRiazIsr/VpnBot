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
			auth.GET("/inbounds/sni-presets", handlers.GetSNIPresets())
			auth.GET("/inbounds/rules", handlers.GetInboundRules())
			auth.GET("/inbounds", handlers.GetInbounds())
			auth.POST("/inbounds", handlers.CreateInbound())
			auth.PUT("/inbounds/:id", handlers.UpdateInbound())
			auth.DELETE("/inbounds/:id", handlers.DeleteInbound())
			auth.PUT("/inbounds/:id/toggle", handlers.ToggleInbound())
			auth.GET("/inbounds/validate-sni", handlers.ValidateSNI())

			// Stats
			auth.GET("/stats", handlers.GetStats())

			// Network (Firewall + Port Forwarding + Connectivity)
			auth.GET("/network/status", handlers.GetNetworkStatus())
			auth.GET("/network/firewall/info", handlers.GetFirewallInfo())
			auth.GET("/network/firewall/rules", handlers.GetFirewallRules())
			auth.POST("/network/firewall/rules", handlers.OpenFirewallPort())
			auth.DELETE("/network/firewall/rules", handlers.CloseFirewallPort())
			auth.GET("/network/forwards/info", handlers.GetPortForwardInfo())
			auth.GET("/network/forwards/rules", handlers.GetForwardRules())
			auth.POST("/network/forwards/rules", handlers.AddForwardRule())
			auth.DELETE("/network/forwards/rules", handlers.RemoveForwardRule())
			auth.POST("/network/ping", handlers.PingPort())
			auth.GET("/network/check-all", handlers.CheckAllPorts())

			// VK TURN Tunnel
			auth.GET("/turn/config", handlers.GetTurnConfig())
			auth.PUT("/turn/config", handlers.UpdateTurnConfig())
			auth.POST("/turn/setup", handlers.SetupTurn())
			auth.POST("/turn/start", handlers.StartTurn())
			auth.POST("/turn/stop", handlers.StopTurn())
			auth.GET("/turn/status", handlers.GetTurnStatus())
			auth.POST("/turn/create-call", handlers.CreateVKCall())
			auth.POST("/turn/test-creds", handlers.TestTurnCreds())

			// VK TURN Client (RuVDS)
			auth.POST("/turn/client/setup", handlers.SetupTurnClient())
			auth.POST("/turn/client/start", handlers.StartTurnClient())
			auth.POST("/turn/client/stop", handlers.StopTurnClient())
			auth.GET("/turn/client/status", handlers.GetTurnClientStatus())
			auth.GET("/turn/client/link", handlers.GetTurnClientLink())

			// Telemt (MTProto proxy)
			auth.GET("/telemt/config", handlers.GetTelemetConfig())
			auth.POST("/telemt/config", handlers.UpdateTelemetConfig())
			auth.POST("/telemt/setup", handlers.SetupTelemet())
			auth.POST("/telemt/stop", handlers.StopTelemet())
			auth.GET("/telemt/status", handlers.GetTelemetStatus())
			auth.GET("/telemt/users", handlers.GetTelemetUsers())
			auth.POST("/telemt/sync", handlers.SyncTelemetUsers())

			// WireGuard tunnel (RuVDS ↔ Hetzner)
			auth.GET("/wireguard/config", handlers.GetWireGuardConfig())
			auth.PUT("/wireguard/config", handlers.UpdateWireGuardConfig())
			auth.POST("/wireguard/setup", handlers.SetupWireGuard())
			auth.POST("/wireguard/restart", handlers.RestartWireGuard())
			auth.POST("/wireguard/stop", handlers.StopWireGuard())
			auth.GET("/wireguard/status", handlers.GetWireGuardStatus())

			// Sing-box mirror on RuVDS
			auth.POST("/singbox/ruvds/setup", handlers.SetupSingboxRuVDS())
			auth.POST("/singbox/ruvds/reload", handlers.ReloadSingboxRuVDS())
			auth.POST("/singbox/ruvds/start", handlers.StartSingboxRuVDS())
			auth.POST("/singbox/ruvds/stop", handlers.StopSingboxRuVDS())
			auth.GET("/singbox/ruvds/status", handlers.GetSingboxRuVDSStatus())
			auth.GET("/singbox/ruvds/config", handlers.PreviewSingboxRuVDSConfig())
			auth.GET("/singbox/ruvds/logs", handlers.GetSingboxRuVDSLogs())

			// Telemt mirror on RuVDS
			auth.POST("/telemt/ruvds/setup", handlers.SetupTelemtRuVDS())
			auth.POST("/telemt/ruvds/reload", handlers.ReloadTelemtRuVDS())
			auth.POST("/telemt/ruvds/start", handlers.StartTelemtRuVDS())
			auth.POST("/telemt/ruvds/stop", handlers.StopTelemtRuVDS())
			auth.GET("/telemt/ruvds/status", handlers.GetTelemtRuVDSStatus())
			auth.GET("/telemt/ruvds/logs", handlers.GetTelemtRuVDSLogs())
		}
	}

	// Public subscription endpoints
	r.GET("/sub/:token", handlers.GetSubscription())              // Hetzner-направленная (backward compat)
	r.GET("/sub-ruvds/:token", handlers.GetSubscriptionRuVDS())   // RuVDS-направленная (новая)
}
