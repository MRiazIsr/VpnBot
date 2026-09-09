package service

import (
	"encoding/json"
	"fmt"
	"vpnbot/database"
)

// Xray-core на RuVDS — сайдкар перед sing-box: снимает finalmask и отдаёт
// голый поток во внутренний VLESS-инбаунд на loopback. Пин версии — из
// спайка 2026-09-09 (docs/xray-finalmask-spike.md): sudoku/header-custom
// есть с 26.3.27, баг #6184 закрыт в 26.6.1, 26.9.9 проверен на стенде.
const (
	XrayVersion       = "26.9.9"
	XrayLinux64SHA256 = "1eb9175d0f0a8f8149c9230a7fc5ae66ce332ed20a53155ce61fe62e3f58b7df"
)

// finalmaskBlock — минимальная схема для валидации MaskJSON. Остальное
// содержимое не трактуем: в Xray уходит исходная строка как есть.
type finalmaskBlock struct {
	TCP []json.RawMessage `json:"tcp"`
}

// ValidateMaskJSON проверяет, что строка — JSON-объект с непустым массивом tcp.
func ValidateMaskJSON(s string) error {
	if s == "" {
		return fmt.Errorf("mask_json is empty")
	}
	var fm finalmaskBlock
	if err := json.Unmarshal([]byte(s), &fm); err != nil {
		return fmt.Errorf("mask_json is not a JSON object: %w", err)
	}
	if len(fm.TCP) == 0 {
		return fmt.Errorf("mask_json must contain a non-empty \"tcp\" array")
	}
	return nil
}

type xrayLog struct {
	Loglevel string `json:"loglevel"`
}

type xrayDokodemoSettings struct {
	Address string `json:"address"`
	Port    int    `json:"port"`
	Network string `json:"network"`
}

type xrayStreamSettings struct {
	Network   string          `json:"network"`
	Finalmask json.RawMessage `json:"finalmask"`
}

type xrayInbound struct {
	Tag            string               `json:"tag"`
	Listen         string               `json:"listen"`
	Port           int                  `json:"port"`
	Protocol       string               `json:"protocol"`
	Settings       xrayDokodemoSettings `json:"settings"`
	StreamSettings xrayStreamSettings   `json:"streamSettings"`
}

type xrayOutbound struct {
	Protocol string `json:"protocol"`
	Tag      string `json:"tag"`
}

type xrayConfig struct {
	Log       xrayLog        `json:"log"`
	Inbounds  []xrayInbound  `json:"inbounds"`
	Outbounds []xrayOutbound `json:"outbounds"`
}

// findMaskInner ищет внутренний инбаунд для маски среди переданных (enabled).
func findMaskInner(mask database.InboundConfig, inbounds []database.InboundConfig) (database.InboundConfig, error) {
	for _, ib := range inbounds {
		if ib.Tag != mask.MaskInnerTag {
			continue
		}
		if ib.Protocol != "vless" || ib.Transport != "" {
			return database.InboundConfig{}, fmt.Errorf(
				"mask %q: inner inbound %q must be vless over plain TCP (got protocol=%q transport=%q)",
				mask.Tag, ib.Tag, ib.Protocol, ib.Transport)
		}
		return ib, nil
	}
	return database.InboundConfig{}, fmt.Errorf("mask %q: inner inbound %q not found among enabled inbounds", mask.Tag, mask.MaskInnerTag)
}

// buildXrayConfig — чистая функция: JSON Xray из mask-инбаундов.
// nil, nil — если mask-инбаундов нет (сервис на RuVDS надо остановить).
func buildXrayConfig(inbounds []database.InboundConfig) ([]byte, error) {
	cfg := xrayConfig{
		Log:       xrayLog{Loglevel: "warning"},
		Inbounds:  []xrayInbound{},
		Outbounds: []xrayOutbound{{Protocol: "freedom", Tag: "direct"}},
	}
	for _, mask := range inbounds {
		if mask.Protocol != "mask" {
			continue
		}
		if err := ValidateMaskJSON(mask.MaskJSON); err != nil {
			return nil, fmt.Errorf("mask %q: %w", mask.Tag, err)
		}
		inner, err := findMaskInner(mask, inbounds)
		if err != nil {
			return nil, err
		}
		cfg.Inbounds = append(cfg.Inbounds, xrayInbound{
			Tag:      mask.Tag,
			Listen:   "0.0.0.0",
			Port:     mask.ListenPort,
			Protocol: "dokodemo-door",
			Settings: xrayDokodemoSettings{Address: "127.0.0.1", Port: inner.ListenPort, Network: "tcp"},
			StreamSettings: xrayStreamSettings{
				Network:   "tcp",
				Finalmask: json.RawMessage(mask.MaskJSON),
			},
		})
	}
	if len(cfg.Inbounds) == 0 {
		return nil, nil
	}
	return json.MarshalIndent(cfg, "", "  ")
}
