package service

import (
	"encoding/json"
	"fmt"
	"strings"
	"vpnbot/database"
)

const (
	XDNSServerMTU = 900
	XDNSClientMTU = 130
)

// ValidateXDNSInbound — проверки полей xdns-инбаунда (без ключей: они
// заполняются при setup).
func ValidateXDNSInbound(ib database.InboundConfig) error {
	if ib.ListenPort == 0 {
		return fmt.Errorf("listen_port is required for xdns")
	}
	if ib.XDNSDomain == "" {
		return fmt.Errorf("xdns_domain is required (e.g. \"t.edgn.net:txt\")")
	}
	if ib.XDNSResolvers == "" {
		return fmt.Errorf("xdns_resolvers is required (comma-separated resolver specs)")
	}
	return nil
}

// splitResolvers — режет XDNSResolvers по запятой, срезая пробелы и пустые.
func splitResolvers(s string) []string {
	out := []string{}
	for _, r := range strings.Split(s, ",") {
		if r = strings.TrimSpace(r); r != "" {
			out = append(out, r)
		}
	}
	return out
}

// xdnsClientFinalmask — клиентский блок finalmask для ссылки (fm).
func xdnsClientFinalmask(resolvers string) string {
	block := map[string]any{
		"udp": []any{map[string]any{
			"type":     "xdns",
			"settings": map[string]any{"resolvers": splitResolvers(resolvers)},
		}},
	}
	b, _ := json.Marshal(block)
	return string(b)
}

type xdnsVLessClient struct {
	ID string `json:"id"`
}

type xdnsInboundSettings struct {
	Clients    []xdnsVLessClient `json:"clients"`
	Decryption string            `json:"decryption"`
}

type xdnsKCPSettings struct {
	MTU int `json:"mtu"`
}

type xdnsStreamSettings struct {
	Network     string          `json:"network"`
	KCPSettings xdnsKCPSettings `json:"kcpSettings"`
	Finalmask   json.RawMessage `json:"finalmask"`
}

type xdnsInbound struct {
	Tag            string              `json:"tag"`
	Listen         string              `json:"listen"`
	Port           int                 `json:"port"`
	Protocol       string              `json:"protocol"`
	Settings       xdnsInboundSettings `json:"settings"`
	StreamSettings xdnsStreamSettings  `json:"streamSettings"`
}

type xdnsConfig struct {
	Log       xrayLog        `json:"log"`
	Inbounds  []xdnsInbound  `json:"inbounds"`
	Outbounds []xrayOutbound `json:"outbounds"`
}

// buildXrayXDNSConfig — чистая функция: JSON Xray для XDNS на Hetzner.
// nil,nil если xdns-инбаундов нет. Ошибка если у enabled нет ключей (setup).
func buildXrayXDNSConfig(inbounds []database.InboundConfig, users []database.User) ([]byte, error) {
	clients := []xdnsVLessClient{}
	for _, u := range buildNewUsers(users) {
		clients = append(clients, xdnsVLessClient{ID: u.UUID})
	}
	cfg := xdnsConfig{
		Log:       xrayLog{Loglevel: "warning"},
		Inbounds:  []xdnsInbound{},
		Outbounds: []xrayOutbound{{Protocol: "freedom", Tag: "direct"}},
	}
	for _, ib := range inbounds {
		if ib.Protocol != "xdns" {
			continue
		}
		if err := ValidateXDNSInbound(ib); err != nil {
			return nil, fmt.Errorf("xdns %q: %w", ib.Tag, err)
		}
		if ib.XDNSDecryption == "" {
			return nil, fmt.Errorf("xdns %q: нет ключей VLESS — выполните POST /api/xray/xdns/setup", ib.Tag)
		}
		serverFM, _ := json.Marshal(map[string]any{
			"udp": []any{map[string]any{
				"type":     "xdns",
				"settings": map[string]any{"domains": []string{ib.XDNSDomain}},
			}},
		})
		cfg.Inbounds = append(cfg.Inbounds, xdnsInbound{
			Tag:      ib.Tag,
			Listen:   "0.0.0.0",
			Port:     ib.ListenPort,
			Protocol: "vless",
			Settings: xdnsInboundSettings{Clients: clients, Decryption: ib.XDNSDecryption},
			StreamSettings: xdnsStreamSettings{
				Network:     "kcp",
				KCPSettings: xdnsKCPSettings{MTU: XDNSServerMTU},
				Finalmask:   json.RawMessage(serverFM),
			},
		})
	}
	if len(cfg.Inbounds) == 0 {
		return nil, nil
	}
	return json.MarshalIndent(cfg, "", "  ")
}
