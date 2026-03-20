package handlers

import (
	"net/http"
	"vpnbot/service"

	"github.com/gin-gonic/gin"
)

// === Firewall (Hetzner Cloud) ===

func GetFirewallInfo() gin.HandlerFunc {
	return func(c *gin.Context) {
		info, err := service.GetFirewallInfo()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, info)
	}
}

func GetFirewallRules() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !service.IsFirewallConfigured() {
			c.JSON(http.StatusBadRequest, gin.H{"error": "HETZNER_API_TOKEN не задан"})
			return
		}

		rules, err := service.GetFirewallRules()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		c.JSON(http.StatusOK, gin.H{"rules": rules})
	}
}

func OpenFirewallPort() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !service.IsFirewallConfigured() {
			c.JSON(http.StatusBadRequest, gin.H{"error": "HETZNER_API_TOKEN не задан"})
			return
		}

		var req struct {
			Port        int    `json:"port" binding:"required"`
			Protocol    string `json:"protocol" binding:"required"`
			Description string `json:"description"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Нужны port и protocol"})
			return
		}

		if req.Protocol != "tcp" && req.Protocol != "udp" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "protocol должен быть 'tcp' или 'udp'"})
			return
		}

		if err := service.OpenFirewallPort(req.Port, req.Protocol, req.Description); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		c.JSON(http.StatusOK, gin.H{"message": "Порт открыт"})
	}
}

func CloseFirewallPort() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !service.IsFirewallConfigured() {
			c.JSON(http.StatusBadRequest, gin.H{"error": "HETZNER_API_TOKEN не задан"})
			return
		}

		var req struct {
			Port     int    `json:"port" binding:"required"`
			Protocol string `json:"protocol" binding:"required"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Нужны port и protocol"})
			return
		}

		if err := service.CloseFirewallPort(req.Port, req.Protocol); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		c.JSON(http.StatusOK, gin.H{"message": "Порт закрыт"})
	}
}

// === Port Forwarding (RuVDS iptables) ===

func GetPortForwardInfo() gin.HandlerFunc {
	return func(c *gin.Context) {
		info, err := service.GetPortForwardInfo()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, info)
	}
}

func GetForwardRules() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !service.IsPortForwardConfigured() {
			c.JSON(http.StatusBadRequest, gin.H{"error": "RUVDS_IP не задан"})
			return
		}

		rules, err := service.GetForwardRules()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		c.JSON(http.StatusOK, gin.H{"rules": rules})
	}
}

func AddForwardRule() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !service.IsPortForwardConfigured() {
			c.JSON(http.StatusBadRequest, gin.H{"error": "RUVDS_IP не задан"})
			return
		}

		var req struct {
			Port     int    `json:"port" binding:"required"`
			Protocol string `json:"protocol" binding:"required"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Нужны port и protocol"})
			return
		}

		if req.Protocol != "tcp" && req.Protocol != "udp" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "protocol должен быть 'tcp' или 'udp'"})
			return
		}

		if err := service.AddForward(req.Port, req.Protocol); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		c.JSON(http.StatusOK, gin.H{"message": "Проброс добавлен"})
	}
}

func RemoveForwardRule() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !service.IsPortForwardConfigured() {
			c.JSON(http.StatusBadRequest, gin.H{"error": "RUVDS_IP не задан"})
			return
		}

		var req struct {
			Port     int    `json:"port" binding:"required"`
			Protocol string `json:"protocol" binding:"required"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Нужны port и protocol"})
			return
		}

		if err := service.RemoveForward(req.Port, req.Protocol); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		c.JSON(http.StatusOK, gin.H{"message": "Проброс удалён"})
	}
}

// === Connectivity Checks ===

func PingPort() gin.HandlerFunc {
	return func(c *gin.Context) {
		var req struct {
			Host string `json:"host" binding:"required"`
			Port int    `json:"port" binding:"required"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Нужны host и port"})
			return
		}

		ok, ms, err := service.CheckPort(req.Host, req.Port, 5*1e9) // 5 секунд
		result := gin.H{
			"reachable":  ok,
			"latency_ms": ms,
		}
		if err != nil {
			result["error"] = err.Error()
		}

		c.JSON(http.StatusOK, result)
	}
}

func CheckAllPorts() gin.HandlerFunc {
	return func(c *gin.Context) {
		checks := service.CheckAllInboundPorts()
		c.JSON(http.StatusOK, gin.H{"checks": checks})
	}
}

// === Network Status ===

func GetNetworkStatus() gin.HandlerFunc {
	return func(c *gin.Context) {
		status := service.GetNetworkStatus()
		c.JSON(http.StatusOK, status)
	}
}
