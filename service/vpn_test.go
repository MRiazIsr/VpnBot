package service

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
