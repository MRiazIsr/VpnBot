package service

import (
	"fmt"
	"net"
	"time"
	"vpnbot/database"
)

// --- Public types ---

type PortCheck struct {
	Port     int    `json:"port"`
	Protocol string `json:"protocol"`
	Tag      string `json:"tag,omitempty"`

	// Проверка через RuVDS (полная цепочка: клиент → RuVDS → Hetzner)
	RuVDSReachable bool   `json:"ruvds_reachable"`
	RuVDSLatencyMs int64  `json:"ruvds_latency_ms,omitempty"`
	RuVDSError     string `json:"ruvds_error,omitempty"`

	// Прямая проверка Hetzner (фаервол + sing-box)
	HetznerReachable bool   `json:"hetzner_reachable"`
	HetznerLatencyMs int64  `json:"hetzner_latency_ms,omitempty"`
	HetznerError     string `json:"hetzner_error,omitempty"`
}

type NetworkStatus struct {
	Firewall    *FirewallInfo    `json:"firewall"`
	PortForward *PortForwardInfo `json:"port_forward"`
}

// --- Public API ---

func CheckPort(host string, port int, timeout time.Duration) (bool, int64, error) {
	addr := fmt.Sprintf("%s:%d", host, port)
	start := time.Now()
	conn, err := net.DialTimeout("tcp", addr, timeout)
	latency := time.Since(start).Milliseconds()
	if err != nil {
		return false, latency, err
	}
	conn.Close()
	return true, latency, nil
}

func CheckAllInboundPorts() []PortCheck {
	var inbounds []database.InboundConfig
	database.DB.Where("enabled = ?", true).Order("sort_order").Find(&inbounds)

	ruvdsIP := GetRuVDSIP()
	hetznerIP := GetHetznerServerIP()
	timeout := 5 * time.Second

	checks := []PortCheck{}
	for _, ib := range inbounds {
		if ib.ListenPort == 0 {
			continue
		}

		check := PortCheck{
			Port:     ib.ListenPort,
			Protocol: inboundNetProtocol(ib),
			Tag:      ib.Tag,
		}

		// TCP-проверка через RuVDS (полная цепочка)
		if ruvdsIP != "" {
			ok, ms, err := CheckPort(ruvdsIP, ib.ListenPort, timeout)
			check.RuVDSReachable = ok
			check.RuVDSLatencyMs = ms
			if err != nil {
				check.RuVDSError = err.Error()
			}
		}

		// Прямая TCP-проверка Hetzner
		if hetznerIP != "" {
			ok, ms, err := CheckPort(hetznerIP, ib.ListenPort, timeout)
			check.HetznerReachable = ok
			check.HetznerLatencyMs = ms
			if err != nil {
				check.HetznerError = err.Error()
			}
		}

		checks = append(checks, check)
	}

	// Проверка порта telemt если включён
	var telemetCfg database.TelemetConfig
	if database.DB.First(&telemetCfg).Error == nil && telemetCfg.Enabled && telemetCfg.Port > 0 {
		check := PortCheck{
			Port:     telemetCfg.Port,
			Protocol: "tcp",
			Tag:      "telemt-proxy",
		}

		if ruvdsIP != "" {
			ok, ms, err := CheckPort(ruvdsIP, telemetCfg.Port, timeout)
			check.RuVDSReachable = ok
			check.RuVDSLatencyMs = ms
			if err != nil {
				check.RuVDSError = err.Error()
			}
		}

		if hetznerIP != "" {
			ok, ms, err := CheckPort(hetznerIP, telemetCfg.Port, timeout)
			check.HetznerReachable = ok
			check.HetznerLatencyMs = ms
			if err != nil {
				check.HetznerError = err.Error()
			}
		}

		checks = append(checks, check)
	}

	return checks
}

func GetNetworkStatus() NetworkStatus {
	var status NetworkStatus

	fwInfo, err := GetFirewallInfo()
	if err != nil {
		status.Firewall = &FirewallInfo{Configured: false}
	} else {
		status.Firewall = fwInfo
	}

	pfInfo, err := GetPortForwardInfo()
	if err != nil {
		status.PortForward = &PortForwardInfo{Configured: false}
	} else {
		status.PortForward = pfInfo
	}

	return status
}

// InboundNetProtocol возвращает сетевой протокол для инбаунда
func InboundNetProtocol(ib database.InboundConfig) string {
	return inboundNetProtocol(ib)
}

func inboundNetProtocol(ib database.InboundConfig) string {
	if ib.Protocol == "hysteria2" {
		return "udp"
	}
	return "tcp"
}
