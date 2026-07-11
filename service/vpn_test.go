package service

import (
	"encoding/json"
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
