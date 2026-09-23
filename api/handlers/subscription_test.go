package handlers

import (
	"testing"
	"vpnbot/database"
)

func TestInSubscription(t *testing.T) {
	cases := []struct {
		ib   database.InboundConfig
		want bool
	}{
		{database.InboundConfig{Tag: "DE-TCP", Protocol: "vless"}, true},
		{database.InboundConfig{Tag: "DE-Yandex", Protocol: "vless", ExitOutbound: "wg-out"}, true},
		{database.InboundConfig{Tag: "RU", Protocol: "vless", ExitOutbound: "direct"}, false},
		{database.InboundConfig{Tag: "RU-STLS", Protocol: "shadowtls", ExitOutbound: "direct"}, false},
		{database.InboundConfig{Tag: "RU-MASK", Protocol: "mask"}, false},
		{database.InboundConfig{Tag: "XDNS", Protocol: "xdns"}, false},
	}
	for _, c := range cases {
		if got := inSubscription(c.ib); got != c.want {
			t.Errorf("inSubscription(%s) = %v, want %v", c.ib.Tag, got, c.want)
		}
	}
}
