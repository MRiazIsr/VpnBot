package handlers

import (
	"net/http"
	"vpnbot/service"

	"github.com/gin-gonic/gin"
)

func ReloadConfig() gin.HandlerFunc {
	return func(c *gin.Context) {
		if err := service.GenerateAndReload(); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to reload config", "details": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "Config generated and sing-box reloaded"})
	}
}

func ValidateSNI() gin.HandlerFunc {
	return func(c *gin.Context) {
		domain := c.Query("domain")
		if domain == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "domain query parameter is required"})
			return
		}

		valid := service.ValidateRealitySNI(domain)

		c.JSON(http.StatusOK, gin.H{
			"domain": domain,
			"valid":  valid,
		})
	}
}
