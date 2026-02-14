package handlers

import (
	"net/http"
	"vpnbot/database"
	"vpnbot/service"

	"github.com/gin-gonic/gin"
)

func GetSettings() gin.HandlerFunc {
	return func(c *gin.Context) {
		var settings database.SystemSettings
		database.DB.First(&settings)
		c.JSON(http.StatusOK, settings)
	}
}

func UpdateSettings() gin.HandlerFunc {
	return func(c *gin.Context) {
		var settings database.SystemSettings
		if err := database.DB.First(&settings).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Settings not found"})
			return
		}

		var input struct {
			ListenPort      *int    `json:"listen_port"`
			ServerName      *string `json:"server_name"`
			DestAddr        *string `json:"dest_addr"`
			ServerDomain    *string `json:"server_domain"`
			BypassDomain    *string `json:"bypass_domain"`
			GrpcServerName  *string `json:"grpc_server_name"`
			AlternativeSNIs *string `json:"alternative_snis"`
			Fingerprint     *string `json:"fingerprint"`
			RealityShortIDs *string `json:"reality_short_ids"`
		}

		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid input"})
			return
		}

		if input.ListenPort != nil {
			settings.ListenPort = *input.ListenPort
		}
		if input.ServerName != nil {
			settings.ServerName = *input.ServerName
		}
		if input.DestAddr != nil {
			settings.DestAddr = *input.DestAddr
		}
		if input.ServerDomain != nil {
			settings.ServerDomain = *input.ServerDomain
		}
		if input.BypassDomain != nil {
			settings.BypassDomain = *input.BypassDomain
		}
		if input.GrpcServerName != nil {
			settings.GrpcServerName = *input.GrpcServerName
		}
		if input.AlternativeSNIs != nil {
			settings.AlternativeSNIs = *input.AlternativeSNIs
		}
		if input.Fingerprint != nil {
			settings.Fingerprint = *input.Fingerprint
		}
		if input.RealityShortIDs != nil {
			settings.RealityShortIDs = *input.RealityShortIDs
		}

		database.DB.Save(&settings)
		service.GenerateAndReload()

		c.JSON(http.StatusOK, settings)
	}
}

func UpdateKeys() gin.HandlerFunc {
	return func(c *gin.Context) {
		var settings database.SystemSettings
		if err := database.DB.First(&settings).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Settings not found"})
			return
		}

		var input struct {
			RealityPrivateKey string `json:"reality_private_key" binding:"required"`
			RealityPublicKey  string `json:"reality_public_key" binding:"required"`
		}

		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Both reality_private_key and reality_public_key are required"})
			return
		}

		settings.RealityPrivateKey = input.RealityPrivateKey
		settings.RealityPublicKey = input.RealityPublicKey

		database.DB.Save(&settings)
		service.GenerateAndReload()

		c.JSON(http.StatusOK, settings)
	}
}

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
