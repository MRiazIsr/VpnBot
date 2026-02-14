package handlers

import (
	"net/http"
	"vpnbot/database"
	"vpnbot/service"

	"github.com/gin-gonic/gin"
)

func GetInbounds() gin.HandlerFunc {
	return func(c *gin.Context) {
		var inbounds []database.InboundConfig
		database.DB.Order("sort_order").Find(&inbounds)
		c.JSON(http.StatusOK, inbounds)
	}
}

func CreateInbound() gin.HandlerFunc {
	return func(c *gin.Context) {
		var input database.InboundConfig
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid input"})
			return
		}

		// Validation
		if input.Tag == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Tag is required"})
			return
		}
		if input.Protocol != "vless" && input.Protocol != "hysteria2" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Protocol must be 'vless' or 'hysteria2'"})
			return
		}

		// Check unique tag
		var count int64
		database.DB.Model(&database.InboundConfig{}).Where("tag = ?", input.Tag).Count(&count)
		if count > 0 {
			c.JSON(http.StatusConflict, gin.H{"error": "Tag already exists"})
			return
		}

		// Check unique port (if non-zero)
		if input.ListenPort != 0 {
			database.DB.Model(&database.InboundConfig{}).Where("listen_port = ?", input.ListenPort).Count(&count)
			if count > 0 {
				c.JSON(http.StatusConflict, gin.H{"error": "Port already in use"})
				return
			}
		}

		// Auto-copy Reality keys from existing inbound if not provided
		if input.TLSType == "reality" && input.RealityPrivateKey == "" {
			var donor database.InboundConfig
			if database.DB.Where("tls_type = ? AND reality_private_key != ''", "reality").First(&donor).Error == nil {
				input.RealityPrivateKey = donor.RealityPrivateKey
				input.RealityPublicKey = donor.RealityPublicKey
				input.RealityShortIDs = donor.RealityShortIDs
			}
		}

		input.IsBuiltin = false
		if err := database.DB.Create(&input).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create inbound"})
			return
		}

		service.GenerateAndReload()

		c.JSON(http.StatusCreated, input)
	}
}

func UpdateInbound() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")

		var existing database.InboundConfig
		if err := database.DB.First(&existing, id).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Inbound not found"})
			return
		}

		var input database.InboundConfig
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid input"})
			return
		}

		if input.Protocol != "" && input.Protocol != "vless" && input.Protocol != "hysteria2" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Protocol must be 'vless' or 'hysteria2'"})
			return
		}

		// Check unique tag if changed
		if input.Tag != "" && input.Tag != existing.Tag {
			var count int64
			database.DB.Model(&database.InboundConfig{}).Where("tag = ? AND id != ?", input.Tag, existing.ID).Count(&count)
			if count > 0 {
				c.JSON(http.StatusConflict, gin.H{"error": "Tag already exists"})
				return
			}
		}

		// Check unique port if changed
		if input.ListenPort != 0 && input.ListenPort != existing.ListenPort {
			var count int64
			database.DB.Model(&database.InboundConfig{}).Where("listen_port = ? AND id != ?", input.ListenPort, existing.ID).Count(&count)
			if count > 0 {
				c.JSON(http.StatusConflict, gin.H{"error": "Port already in use"})
				return
			}
		}

		// Preserve IsBuiltin
		input.IsBuiltin = existing.IsBuiltin
		input.ID = existing.ID

		database.DB.Model(&existing).Updates(input)

		// Reload updated record
		database.DB.First(&existing, id)

		service.GenerateAndReload()

		c.JSON(http.StatusOK, existing)
	}
}

func DeleteInbound() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")

		var existing database.InboundConfig
		if err := database.DB.First(&existing, id).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Inbound not found"})
			return
		}

		if existing.IsBuiltin {
			c.JSON(http.StatusForbidden, gin.H{"error": "Cannot delete builtin inbound. Disable it instead."})
			return
		}

		database.DB.Delete(&existing)

		service.GenerateAndReload()

		c.JSON(http.StatusOK, gin.H{"message": "Inbound deleted"})
	}
}

func ToggleInbound() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")

		var existing database.InboundConfig
		if err := database.DB.First(&existing, id).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Inbound not found"})
			return
		}

		existing.Enabled = !existing.Enabled
		database.DB.Model(&existing).Update("enabled", existing.Enabled)

		service.GenerateAndReload()

		c.JSON(http.StatusOK, existing)
	}
}
