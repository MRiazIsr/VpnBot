package service

import (
	"strconv"
	"strings"
	"testing"
)

const sampleConf = `#!/usr/sbin/nft -f
flush ruleset

table ip relay {
	chain prerouting {
		type nat hook prerouting priority dstnat; policy accept;
		tcp dport 9443 redirect to :29443
		tcp dport 443 dnat to 49.13.201.110:443
		tcp dport 2056 dnat to 49.13.201.110:2056
	}
	chain postrouting {
		type nat hook postrouting priority srcnat; policy accept;
		ip daddr 49.13.201.110 tcp dport { 443, 2056 } masquerade
	}
}
`

func TestRelayTableExists(t *testing.T) {
	if !relayTableExists(sampleConf) {
		t.Fatal("should detect table ip relay")
	}
	if relayTableExists("table ip nat {}") {
		t.Fatal("must not match other tables")
	}
}

func TestBuildForwardLines(t *testing.T) {
	pre, post := buildForwardLines(2057, "tcp", "49.13.201.110")
	if pre != "\t\ttcp dport 2057 dnat to 49.13.201.110:2057" {
		t.Fatalf("pre=%q", pre)
	}
	if post != "\t\tip daddr 49.13.201.110 tcp dport 2057 masquerade" {
		t.Fatalf("post=%q", post)
	}
}

func TestInsertForward_AddsBothLinesPreservingRest(t *testing.T) {
	out, err := insertForward(sampleConf, 2057, "tcp", "49.13.201.110")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "tcp dport 2057 dnat to 49.13.201.110:2057") {
		t.Fatal("missing prerouting dnat for 2057")
	}
	if !strings.Contains(out, "ip daddr 49.13.201.110 tcp dport 2057 masquerade") {
		t.Fatal("missing postrouting masquerade for 2057")
	}
	if !strings.Contains(out, "tcp dport 9443 redirect to :29443") {
		t.Fatal("tunnel redirect line was lost")
	}
	if !strings.Contains(out, "tcp dport 443 dnat to 49.13.201.110:443") {
		t.Fatal("existing 443 dnat lost")
	}
	if !strings.Contains(out, "#!/usr/sbin/nft -f") {
		t.Fatal("header/comment lost")
	}
	if strings.Index(out, "dport 2057 dnat") < strings.Index(out, "9443 redirect") {
		t.Fatal("new dnat must be after redirect line")
	}
}

func TestInsertForward_Idempotent(t *testing.T) {
	once, _ := insertForward(sampleConf, 2057, "tcp", "49.13.201.110")
	twice, err := insertForward(once, 2057, "tcp", "49.13.201.110")
	if err != nil {
		t.Fatal(err)
	}
	if once != twice {
		t.Fatal("insertForward must be idempotent (no duplicate lines)")
	}
}

func TestInsertForward_Refuses9443(t *testing.T) {
	if _, err := insertForward(sampleConf, 9443, "tcp", "49.13.201.110"); err == nil {
		t.Fatal("must refuse port 9443 (reserved MTProto tunnel)")
	}
}

func TestInsertForward_NoRelayTable(t *testing.T) {
	if _, err := insertForward("table ip nat {}", 2057, "tcp", "1.2.3.4"); err == nil {
		t.Fatal("must error when table ip relay absent (no silent legacy fallback)")
	}
}

func TestRemoveForward_RemovesOnlyTargetLines(t *testing.T) {
	out, err := removeForward(sampleConf, 2056, "tcp")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "dport 2056 dnat") {
		t.Fatal("2056 dnat not removed")
	}
	if !strings.Contains(out, "tcp dport 443 dnat to 49.13.201.110:443") {
		t.Fatal("must not remove unrelated 443")
	}
	if !strings.Contains(out, "tcp dport 9443 redirect to :29443") {
		t.Fatal("must never touch the tunnel redirect line")
	}
}

func TestRemoveForward_Refuses9443(t *testing.T) {
	if _, err := removeForward(sampleConf, 9443, "tcp"); err == nil {
		t.Fatal("must refuse removing 9443 tunnel")
	}
}

func TestParseNftForwards(t *testing.T) {
	nftOut := `table ip relay {
	chain prerouting {
		tcp dport 9443 redirect to :29443
		tcp dport 443 dnat to 49.13.201.110:443
		tcp dport 2056 dnat to 49.13.201.110:2056
	}
}`
	rules := parseNftForwards(nftOut, "49.13.201.110")
	if len(rules) != 2 {
		t.Fatalf("want 2 dnat rules (9443 redirect excluded), got %d: %+v", len(rules), rules)
	}
	for _, r := range rules {
		if r.Port == 9443 {
			t.Fatal("redirect 9443 must not be reported as a forward")
		}
		if r.Destination != "49.13.201.110:"+strconv.Itoa(r.Port) {
			t.Fatalf("bad dest %q", r.Destination)
		}
	}
}
