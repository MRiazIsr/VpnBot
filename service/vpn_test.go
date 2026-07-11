package service

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"vpnbot/database"
)

func TestRouteRule_InboundOmitEmpty(t *testing.T) {
	// Пустой Inbound не должен сериализоваться (backwards compat).
	r := RouteRule{IPCIDR: []string{"10.0.0.0/8"}, Outbound: "block"}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "inbound") {
		t.Fatalf("empty Inbound must be omitted, got: %s", b)
	}
}

func TestRouteRule_InboundSerialized(t *testing.T) {
	r := RouteRule{Inbound: []string{"vless-direct-xhttp"}, Outbound: "direct"}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	if !strings.Contains(got, `"inbound":["vless-direct-xhttp"]`) {
		t.Fatalf("expected inbound field, got: %s", got)
	}
	if !strings.Contains(got, `"outbound":"direct"`) {
		t.Fatalf("expected outbound=direct, got: %s", got)
	}
}

func TestLoadExtraOutbound_MissingEnv(t *testing.T) {
	os.Unsetenv("EXTRA_OUTBOUND_JSON_PATH")
	got, err := loadExtraOutbound()
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("expected nil when env missing, got %v", got)
	}
}

func TestLoadExtraOutbound_ReadsFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wg.json")
	content := `{"type":"wireguard","tag":"wg-out","server":"1.2.3.4","server_port":51820}`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("EXTRA_OUTBOUND_JSON_PATH", path)

	got, err := loadExtraOutbound()
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("expected non-nil outbound")
	}
	if got["tag"] != "wg-out" {
		t.Fatalf("expected tag=wg-out, got %v", got["tag"])
	}
}

func TestLoadExtraOutbound_MissingFileIsError(t *testing.T) {
	t.Setenv("EXTRA_OUTBOUND_JSON_PATH", "/nonexistent/wg.json")
	_, err := loadExtraOutbound()
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestSingBoxConfig_OutboundsMixedTypes(t *testing.T) {
	cfg := SingBoxConfig{
		Outbounds: []any{
			map[string]any{"type": "wireguard", "tag": "wg-out", "server": "1.2.3.4"},
			OutboundConfig{Type: "direct", Tag: "direct"},
		},
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	if !strings.Contains(got, `"tag":"wg-out"`) {
		t.Fatalf("expected wg-out, got %s", got)
	}
	if !strings.Contains(got, `"tag":"direct"`) {
		t.Fatalf("expected direct, got %s", got)
	}
}

func TestBuildSingBoxConfig_NoExtraOutbound(t *testing.T) {
	cfg := buildSingBoxConfig(nil, nil, nil, "")
	if len(cfg.Outbounds) != 2 {
		t.Fatalf("expected 2 outbounds (direct, block), got %d", len(cfg.Outbounds))
	}
	if cfg.Route.Final != "direct" {
		t.Fatalf("expected route.final=direct, got %q", cfg.Route.Final)
	}
}

func TestBuildSingBoxConfig_WithExtraOutbound(t *testing.T) {
	extra := map[string]any{"type": "wireguard", "tag": "wg-out"}
	cfg := buildSingBoxConfig(nil, nil, extra, "wg-out")
	if len(cfg.Outbounds) != 3 {
		t.Fatalf("expected 3 outbounds, got %d", len(cfg.Outbounds))
	}
	first, ok := cfg.Outbounds[0].(map[string]any)
	if !ok || first["tag"] != "wg-out" {
		t.Fatalf("expected first outbound wg-out, got %+v", cfg.Outbounds[0])
	}
	if cfg.Route.Final != "wg-out" {
		t.Fatalf("expected route.final=wg-out, got %q", cfg.Route.Final)
	}
}

func TestBuildSingBoxConfig_PerInboundRule(t *testing.T) {
	ib := database.InboundConfig{
		Tag:               "vless-direct-xhttp",
		Protocol:          "vless",
		ListenPort:        2059,
		TLSType:           "reality",
		Transport:         "xhttp",
		SNI:               "yastatic.net",
		UserType:          "new",
		Enabled:           true,
		ExitOutbound:      "direct",
		RealityPrivateKey: "x", RealityPublicKey: "y",
		RealityShortIDs:   database.JSONStringArray{"abcd"},
	}
	cfg := buildSingBoxConfig([]database.InboundConfig{ib}, nil, nil, "")
	if len(cfg.Route.Rules) < 2 {
		t.Fatalf("expected >=2 rules, got %d", len(cfg.Route.Rules))
	}
	first := cfg.Route.Rules[0]
	if len(first.Inbound) != 1 || first.Inbound[0] != "vless-direct-xhttp" || first.Outbound != "direct" {
		t.Fatalf("expected first rule to route direct-xhttp→direct, got %+v", first)
	}
	found := false
	for _, r := range cfg.Route.Rules {
		if len(r.IPCIDR) > 0 && r.Outbound == "block" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("bogon block rule missing")
	}
}

func TestBuildSingBoxConfig_DirectExit_EndToEnd(t *testing.T) {
	users := []database.User{
		{Username: "alice", UUID: "550e8400-e29b-41d4-a716-446655440000"},
	}
	ib := database.InboundConfig{
		Tag:               "vless-direct-xhttp",
		Protocol:          "vless",
		ListenPort:        2059,
		TLSType:           "reality",
		SNI:               "yastatic.net",
		Transport:         "xhttp",
		UserType:          "new",
		Enabled:           true,
		ExitOutbound:      "direct",
		RealityPrivateKey: "priv",
		RealityPublicKey:  "pub",
		RealityShortIDs:   database.JSONStringArray{"abcd1234"},
	}
	cfg := buildSingBoxConfig([]database.InboundConfig{ib}, users, nil, "")

	b, _ := json.Marshal(cfg)
	got := string(b)
	for _, expected := range []string{
		`"tag":"vless-direct-xhttp"`,
		`"listen_port":2059`,
		`"server_name":"yastatic.net"`,
		`"private_key":"priv"`,
		`"inbound":["vless-direct-xhttp"]`,
		`"outbound":"direct"`,
	} {
		if !strings.Contains(got, expected) {
			t.Fatalf("missing %q in generated config: %s", expected, got)
		}
	}
}

func TestMultiplexConfig_PaddingAndMaxStreams(t *testing.T) {
	m := MultiplexConfig{Enabled: true, Padding: true, MaxStreams: 8}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	if !strings.Contains(got, `"padding":true`) {
		t.Fatalf("expected padding, got %s", got)
	}
	if !strings.Contains(got, `"max_streams":8`) {
		t.Fatalf("expected max_streams=8, got %s", got)
	}
}

func TestMultiplexConfig_OmitsWhenZero(t *testing.T) {
	m := MultiplexConfig{Enabled: true}
	b, _ := json.Marshal(m)
	got := string(b)
	if strings.Contains(got, "padding") {
		t.Fatalf("expected no padding for zero value, got %s", got)
	}
	if strings.Contains(got, "max_streams") {
		t.Fatalf("expected no max_streams for zero value, got %s", got)
	}
}

func TestBuildSingboxInbound_MuxPadding(t *testing.T) {
	ib := database.InboundConfig{
		Tag:           "vless-direct-xhttp",
		Protocol:      "vless",
		ListenPort:    2059,
		TLSType:       "reality",
		SNI:           "yastatic.net",
		Transport:     "xhttp",
		UserType:      "new",
		Multiplex:     true,
		MuxPadding:    true,
		MuxMaxStreams: 8,
	}
	sb := buildSingboxInbound(ib, nil)
	if sb.Multiplex == nil {
		t.Fatal("expected non-nil Multiplex")
	}
	if !sb.Multiplex.Padding {
		t.Fatal("expected padding=true")
	}
	if sb.Multiplex.MaxStreams != 8 {
		t.Fatalf("expected max_streams=8, got %d", sb.Multiplex.MaxStreams)
	}
}
