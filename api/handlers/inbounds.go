package handlers

import (
	"net/http"
	"strings"
	"vpnbot/database"
	"vpnbot/service"

	"github.com/gin-gonic/gin"
)

func trimInboundStrings(input *database.InboundConfig) {
	input.Tag = strings.TrimSpace(input.Tag)
	input.DisplayName = strings.TrimSpace(input.DisplayName)
	input.Protocol = strings.TrimSpace(input.Protocol)
	input.TLSType = strings.TrimSpace(input.TLSType)
	input.SNI = strings.TrimSpace(input.SNI)
	input.CertPath = strings.TrimSpace(input.CertPath)
	input.KeyPath = strings.TrimSpace(input.KeyPath)
	input.Transport = strings.TrimSpace(input.Transport)
	input.ServiceName = strings.TrimSpace(input.ServiceName)
	input.UserType = strings.TrimSpace(input.UserType)
	input.Flow = strings.TrimSpace(input.Flow)
	input.ServerAddress = strings.TrimSpace(input.ServerAddress)
	input.RealityPrivateKey = strings.TrimSpace(input.RealityPrivateKey)
	input.RealityPublicKey = strings.TrimSpace(input.RealityPublicKey)
	input.Fingerprint = strings.TrimSpace(input.Fingerprint)
}

// validateInboundCombination проверяет совместимость полей инбаунда.
// Возвращает текст ошибки или пустую строку если всё ок.
func validateInboundCombination(input *database.InboundConfig) string {
	// hysteria2: только certificate, user_type=hy2, без transport и flow
	if input.Protocol == "hysteria2" {
		if input.TLSType != "" && input.TLSType != "certificate" {
			return "Hysteria2 requires tls_type 'certificate'"
		}
		if input.UserType != "" && input.UserType != "hy2" {
			return "Hysteria2 requires user_type 'hy2'"
		}
		if input.Transport != "" {
			return "Hysteria2 does not support transport"
		}
		if input.Flow != "" {
			return "Hysteria2 does not support flow"
		}
	}

	// flow=xtls-rprx-vision только с TCP (transport пустой) и user_type=legacy
	if input.Flow != "" {
		if input.Transport != "" {
			return "Flow (XTLS-Vision) only works with TCP (empty transport)"
		}
		if input.UserType != "" && input.UserType != "legacy" {
			return "Flow (XTLS-Vision) requires user_type 'legacy'"
		}
	}

	// transport != "" требует user_type=new (без flow)
	if input.Transport != "" {
		if input.UserType == "legacy" {
			return "Transport '" + input.Transport + "' requires user_type 'new' (legacy adds flow which is incompatible)"
		}
	}

	return ""
}

func GetInboundRules() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"transports": []gin.H{
				{
					"value":       "",
					"label":       "TCP (прямое подключение)",
					"description": "Стандартный режим. Поддерживает XTLS-Vision (flow). Требует user_type=legacy.",
					"user_type":   "legacy",
					"flow":        "xtls-rprx-vision",
				},
				{
					"value":       "http",
					"label":       "HTTP/2",
					"description": "Мультиплексирование через HTTP/2. Может блокироваться ML-DPI. Требует user_type=new.",
					"user_type":   "new",
					"flow":        "",
				},
				{
					"value":       "httpupgrade",
					"label":       "HTTPUpgrade (HTTP/1.1)",
					"description": "HTTP/1.1 Upgrade — обходит ML-детекцию РКН, которая нацелена на HTTP/2. Рекомендуется при блокировках. Требует user_type=new. Поле service_name = path.",
					"user_type":   "new",
					"flow":        "",
				},
				{
					"value":       "grpc",
					"label":       "gRPC",
					"description": "Маскировка под gRPC API. Требует user_type=new. Поле service_name = имя сервиса.",
					"user_type":   "new",
					"flow":        "",
				},
				{
					"value":       "ws",
					"label":       "WebSocket",
					"description": "HTTP/1.1 WebSocket. Менее защищён чем httpupgrade. Требует user_type=new. Поле service_name = path.",
					"user_type":   "new",
					"flow":        "",
				},
			},
			"protocols": []gin.H{
				{
					"value":     "vless",
					"label":     "VLESS",
					"tls_types": []string{"reality", "certificate"},
				},
				{
					"value":     "hysteria2",
					"label":     "Hysteria2",
					"tls_types": []string{"certificate"},
					"forced":    gin.H{"user_type": "hy2", "transport": "", "flow": ""},
				},
			},
		})
	}
}

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

		trimInboundStrings(&input)

		// Validation
		if input.Tag == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Tag is required"})
			return
		}
		if input.Protocol != "vless" && input.Protocol != "hysteria2" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Protocol must be 'vless' or 'hysteria2'"})
			return
		}

		if err := validateInboundCombination(&input); err != "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": err})
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

		trimInboundStrings(&input)

		if input.Protocol != "" && input.Protocol != "vless" && input.Protocol != "hysteria2" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Protocol must be 'vless' or 'hysteria2'"})
			return
		}

		if err := validateInboundCombination(&input); err != "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": err})
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
