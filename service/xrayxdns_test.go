package service

import (
	"encoding/json"
	"strings"
	"testing"
	"vpnbot/database"
)

func xdnsFixture() database.InboundConfig {
	return database.InboundConfig{
		Tag: "XDNS", Protocol: "xdns", ListenPort: 53, Enabled: true,
		XDNSDomain:     "t.edgn.net:txt",
		XDNSResolvers:  "t.edgn.net:txt+udp://8.8.8.8:53,t.edgn.net:txt+udp://1.1.1.1:53",
		XDNSDecryption: "mlkem768x25519plus.native.600s.SERVERKEY",
		XDNSEncryption: "mlkem768x25519plus.native.0rtt.CLIENTKEY",
	}
}

func TestValidateXDNSInbound(t *testing.T) {
	ok := xdnsFixture()
	if err := ValidateXDNSInbound(ok); err != nil {
		t.Fatalf("valid inbound rejected: %v", err)
	}
	for _, bad := range []database.InboundConfig{
		func() database.InboundConfig { c := ok; c.ListenPort = 0; return c }(),
		func() database.InboundConfig { c := ok; c.XDNSDomain = ""; return c }(),
		func() database.InboundConfig { c := ok; c.XDNSResolvers = ""; return c }(),
	} {
		if err := ValidateXDNSInbound(bad); err == nil {
			t.Errorf("expected error for %+v", bad)
		}
	}
}

func TestBuildXrayXDNSConfig_None(t *testing.T) {
	out, err := buildXrayXDNSConfig([]database.InboundConfig{{Tag: "DE", Protocol: "vless"}}, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if out != nil {
		t.Fatalf("expected nil when no xdns inbounds, got %s", out)
	}
}

func TestBuildXrayXDNSConfig_Single(t *testing.T) {
	users := []database.User{{Username: "alice", UUID: "550e8400-e29b-41d4-a716-446655440000"}}
	out, err := buildXrayXDNSConfig([]database.InboundConfig{xdnsFixture()}, users, "")
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(out, &cfg); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	got := string(out)
	for _, want := range []string{
		`"protocol": "vless"`, `"port": 53`, `"network": "kcp"`, `"mtu": 900`,
		`"type": "xdns"`, `"t.edgn.net:txt"`, `SERVERKEY`,
		`550e8400-e29b-41d4-a716-446655440000`, `"protocol": "freedom"`,
		`"listen": "0.0.0.0"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "CLIENTKEY") {
		t.Errorf("client encryption key must NOT be in server config: %s", got)
	}

	outAddr, err := buildXrayXDNSConfig([]database.InboundConfig{xdnsFixture()}, users, "203.0.113.9")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(outAddr), `"listen": "203.0.113.9"`) {
		t.Errorf("expected listen bound to 203.0.113.9, got:\n%s", outAddr)
	}
}

func TestBuildXrayXDNSConfig_MissingKeysErrors(t *testing.T) {
	c := xdnsFixture()
	c.XDNSDecryption = ""
	if _, err := buildXrayXDNSConfig([]database.InboundConfig{c}, nil, ""); err == nil {
		t.Fatal("expected error when XDNSDecryption empty (setup not run)")
	}
}

func TestXDNSClientFinalmask(t *testing.T) {
	fm := xdnsClientFinalmask("t.edgn.net:txt+udp://8.8.8.8:53, t.edgn.net:txt+udp://1.1.1.1:53")
	var m map[string]any
	if err := json.Unmarshal([]byte(fm), &m); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, fm)
	}
	for _, want := range []string{`"udp"`, `"xdns"`, `"resolvers"`, `8.8.8.8`, `1.1.1.1`} {
		if !strings.Contains(fm, want) {
			t.Errorf("missing %s in %s", want, fm)
		}
	}
	// пробелы вокруг запятой должны срезаться
	if strings.Contains(fm, `" t.edgn.net`) {
		t.Errorf("resolver not trimmed: %s", fm)
	}
}

func TestGenerateXDNSLink(t *testing.T) {
	ib := xdnsFixture()
	user := database.User{Username: "alice", UUID: "550e8400-e29b-41d4-a716-446655440000"}
	link := GenerateXDNSLink(ib, user)
	if !strings.HasPrefix(link, "vless://550e8400-e29b-41d4-a716-446655440000@8.8.8.8:53?") {
		t.Fatalf("expected first resolver host, got %s", link)
	}
	for _, want := range []string{"type=kcp", "encryption=mlkem768x25519plus", "fm=%7B%22udp%22"} {
		if !strings.Contains(link, want) {
			t.Errorf("missing %s in %s", want, link)
		}
	}
	if strings.Contains(link, "seed=") {
		t.Errorf("seed must not be in xdns link: %s", link)
	}
	if !strings.HasSuffix(link, "#XDNS-alice") {
		t.Errorf("expected #XDNS-alice fragment, got %s", link)
	}
}

func TestGenerateXDNSLink_NoResolvers(t *testing.T) {
	ib := xdnsFixture()
	ib.XDNSResolvers = ""
	if got := GenerateXDNSLink(ib, database.User{UUID: "u"}); got != "" {
		t.Fatalf("expected empty link without resolvers, got %q", got)
	}
}
