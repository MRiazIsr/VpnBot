package service

import (
	"encoding/json"
	"strings"
	"testing"
	"vpnbot/database"
)

const testSudokuMask = `{"tcp":[{"type":"sudoku","settings":{"password":"p","ascii":"prefer_entropy"}}]}`

func maskFixture() (database.InboundConfig, database.InboundConfig) {
	inner := database.InboundConfig{Tag: "vless-direct-tcp", Protocol: "vless", ListenPort: 2060, TLSType: "reality", Enabled: true}
	mask := database.InboundConfig{Tag: "RU-MASK", Protocol: "mask", ListenPort: 2071, MaskInnerTag: "vless-direct-tcp", MaskJSON: testSudokuMask, Enabled: true}
	return inner, mask
}

func TestValidateMaskJSON(t *testing.T) {
	cases := map[string]bool{
		testSudokuMask:                         true,
		`{"tcp":[]}`:                           false,
		`{"udp":[{"type":"noise"}]}`:           false,
		`not json`:                             false,
		``:                                     false,
		`{"tcp":[{"type":"sudoku"}],"udp":[]}`: true,
	}
	for in, ok := range cases {
		err := ValidateMaskJSON(in)
		if ok && err != nil {
			t.Errorf("%q: expected valid, got %v", in, err)
		}
		if !ok && err == nil {
			t.Errorf("%q: expected error", in)
		}
	}
}

func TestBuildXrayConfig_NoMaskInbounds(t *testing.T) {
	inner, _ := maskFixture()
	out, err := buildXrayConfig([]database.InboundConfig{inner})
	if err != nil {
		t.Fatal(err)
	}
	if out != nil {
		t.Fatalf("expected nil config when no mask inbounds, got %s", out)
	}
}

func TestBuildXrayConfig_SingleMask(t *testing.T) {
	inner, mask := maskFixture()
	out, err := buildXrayConfig([]database.InboundConfig{inner, mask})
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(out, &cfg); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out)
	}
	inbounds := cfg["inbounds"].([]any)
	if len(inbounds) != 1 {
		t.Fatalf("expected 1 inbound, got %d", len(inbounds))
	}
	got := string(out)
	for _, want := range []string{
		`"tag": "RU-MASK"`,
		`"protocol": "dokodemo-door"`,
		`"listen": "0.0.0.0"`,
		`"port": 2071`,
		`"address": "127.0.0.1"`,
		`"port": 2060`,
		`"network": "tcp"`,
		`"type": "sudoku"`,
		`"protocol": "freedom"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in:\n%s", want, got)
		}
	}
}

func TestBuildXrayConfig_TwoMasks(t *testing.T) {
	inner, mask := maskFixture()
	mask2 := mask
	mask2.Tag = "RU-MASK-2"
	mask2.ListenPort = 2072
	out, err := buildXrayConfig([]database.InboundConfig{inner, mask, mask2})
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	_ = json.Unmarshal(out, &cfg)
	if n := len(cfg["inbounds"].([]any)); n != 2 {
		t.Fatalf("expected 2 inbounds, got %d", n)
	}
}

func TestBuildXrayConfig_InnerMissing(t *testing.T) {
	_, mask := maskFixture()
	if _, err := buildXrayConfig([]database.InboundConfig{mask}); err == nil {
		t.Fatal("expected error when inner inbound is absent")
	}
}

func TestBuildXrayConfig_InnerMustBeVlessTCP(t *testing.T) {
	inner, mask := maskFixture()
	inner.Transport = "xhttp"
	if _, err := buildXrayConfig([]database.InboundConfig{inner, mask}); err == nil {
		t.Fatal("expected error for non-TCP inner inbound")
	}
	inner.Transport = ""
	inner.Protocol = "hysteria2"
	if _, err := buildXrayConfig([]database.InboundConfig{inner, mask}); err == nil {
		t.Fatal("expected error for non-vless inner inbound")
	}
}

func TestBuildXrayConfig_BadMaskJSON(t *testing.T) {
	inner, mask := maskFixture()
	mask.MaskJSON = `{"udp":[]}`
	if _, err := buildXrayConfig([]database.InboundConfig{inner, mask}); err == nil {
		t.Fatal("expected error for mask JSON without tcp")
	}
}

func TestGenerateMaskLink(t *testing.T) {
	inner, mask := maskFixture()
	inner.RealityPublicKey = "PUBKEY"
	inner.SNI = "gosuslugi.ru"
	inner.RealityShortIDs = database.JSONStringArray{"caa7f714"}
	inner.Flow = "xtls-rprx-vision"
	inner.ServerAddress = "hetzner.example" // должен быть проигнорирован
	user := database.User{Username: "alice", UUID: "550e8400-e29b-41d4-a716-446655440000"}

	link := GenerateMaskLink(mask, inner, user, "198.51.100.7")

	if !strings.HasPrefix(link, "vless://550e8400-e29b-41d4-a716-446655440000@198.51.100.7:2071?") {
		t.Fatalf("expected inner uuid + mask port, got %s", link)
	}
	for _, want := range []string{"security=reality", "pbk=PUBKEY", "sni=gosuslugi.ru", "sid=caa7f714", "flow=xtls-rprx-vision"} {
		if !strings.Contains(link, want) {
			t.Errorf("missing %s in %s", want, link)
		}
	}
	// fm = URL-encoded компактный JSON блока finalmask (формат v2rayN/v2rayNG).
	if !strings.Contains(link, "fm=%7B%22tcp%22%3A%5B%7B%22type%22%3A%22sudoku%22") {
		t.Errorf("expected url-encoded fm, got %s", link)
	}
	if !strings.HasSuffix(link, "#RU-MASK") {
		t.Errorf("expected #RU-MASK fragment, got %s", link)
	}
	if strings.Contains(link, "hetzner.example") || strings.Contains(link, ":2060") {
		t.Errorf("inner address/port leaked: %s", link)
	}
}

func TestGenerateLinkForInbound_MaskWithoutDB(t *testing.T) {
	_, mask := maskFixture()
	if got := GenerateLinkForInbound(mask, database.User{}, "198.51.100.7"); got != "" {
		t.Fatalf("expected empty link without DB, got %q", got)
	}
}

func TestGenerateMaskLink_RejectsNonVlessInner(t *testing.T) {
	_, mask := maskFixture()
	user := database.User{Username: "alice", UUID: "550e8400-e29b-41d4-a716-446655440000"}

	// Тест 1: Inner с Protocol "mask" (циклический путь)
	innerMask := database.InboundConfig{Tag: "cyclic-mask", Protocol: "mask", ListenPort: 2060}
	if got := GenerateMaskLink(mask, innerMask, user, "198.51.100.7"); got != "" {
		t.Errorf("should reject mask inner, got %q", got)
	}

	// Тест 2: Inner с Protocol "hysteria2"
	innerHy2 := database.InboundConfig{Tag: "hy2-inner", Protocol: "hysteria2", ListenPort: 2060}
	if got := GenerateMaskLink(mask, innerHy2, user, "198.51.100.7"); got != "" {
		t.Errorf("should reject non-vless inner, got %q", got)
	}
}
