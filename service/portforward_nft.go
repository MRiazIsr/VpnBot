package service

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

const nftConfPath = "/etc/nftables.conf"

// relayTableExists reports whether conf declares `table ip relay`.
func relayTableExists(conf string) bool {
	return regexp.MustCompile(`(?m)^\s*table\s+ip\s+relay\s*\{`).MatchString(conf)
}

// buildForwardLines returns the exact nft lines (tab-indented to match the
// hand-written /etc/nftables.conf chains) for one forwarded port.
func buildForwardLines(port int, proto, hetznerIP string) (pre, post string) {
	p := strconv.Itoa(port)
	pre = "\t\t" + proto + " dport " + p + " dnat to " + hetznerIP + ":" + p
	post = "\t\tip daddr " + hetznerIP + " " + proto + " dport " + p + " masquerade"
	return
}

func reservedPort(port int) error {
	if port == 9443 {
		return fmt.Errorf("порт 9443 зарезервирован под MTProto-туннель (redirect→29443), не управляется как forward")
	}
	return nil
}

// insertForward inserts the two lines for `port` into the prerouting and
// postrouting chains of `table ip relay`, surgically. Everything else
// (comments, manual edits, the 9443 redirect line, ordering) is preserved.
// Idempotent. Errors if table absent or port is the reserved tunnel.
func insertForward(conf string, port int, proto, hetznerIP string) (string, error) {
	if err := reservedPort(port); err != nil {
		return "", err
	}
	if !relayTableExists(conf) {
		return "", fmt.Errorf("в %s нет `table ip relay` — миграция на nftables не выполнена, отказ (без legacy-фолбэка)", nftConfPath)
	}
	pre, post := buildForwardLines(port, proto, hetznerIP)

	lines := strings.Split(conf, "\n")
	hasPre, hasPost := false, false
	for _, l := range lines {
		t := strings.TrimSpace(l)
		if t == strings.TrimSpace(pre) {
			hasPre = true
		}
		if t == strings.TrimSpace(post) {
			hasPost = true
		}
	}

	out := make([]string, 0, len(lines)+2)
	chain := ""
	for _, l := range lines {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "chain prerouting") {
			chain = "pre"
		} else if strings.HasPrefix(t, "chain postrouting") {
			chain = "post"
		}
		if t == "}" && chain == "pre" && !hasPre {
			out = append(out, pre)
			hasPre = true
			chain = ""
		} else if t == "}" && chain == "post" && !hasPost {
			out = append(out, post)
			hasPost = true
			chain = ""
		} else if t == "}" && (chain == "pre" || chain == "post") {
			chain = ""
		}
		out = append(out, l)
	}
	return strings.Join(out, "\n"), nil
}

// removeForward deletes exactly the two lines for `port`. Unrelated lines and
// the reserved 9443 redirect are never touched.
func removeForward(conf string, port int, proto string) (string, error) {
	if err := reservedPort(port); err != nil {
		return "", err
	}
	if !relayTableExists(conf) {
		return "", fmt.Errorf("в %s нет `table ip relay`, отказ", nftConfPath)
	}
	p := strconv.Itoa(port)
	dnatMatch := proto + " dport " + p + " dnat to "
	masqMatch := proto + " dport " + p + " masquerade"
	var out []string
	for _, l := range strings.Split(conf, "\n") {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, dnatMatch) || strings.Contains(t, masqMatch) {
			continue
		}
		out = append(out, l)
	}
	return strings.Join(out, "\n"), nil
}

var nftDnatRe = regexp.MustCompile(`(?m)^\s*(tcp|udp)\s+dport\s+(\d+)\s+dnat\s+to\s+([\d.]+):(\d+)`)

// parseNftForwards extracts forward rules from `nft list table ip relay`
// output, restricted to the given Hetzner IP. The 9443->:29443 redirect is
// not a dnat and is naturally excluded.
func parseNftForwards(nftOut, hetznerIP string) []ForwardRule {
	rules := []ForwardRule{}
	seen := map[string]bool{}
	for _, m := range nftDnatRe.FindAllStringSubmatch(nftOut, -1) {
		proto, portS, dstIP, dstPort := m[1], m[2], m[3], m[4]
		if dstIP != hetznerIP {
			continue
		}
		port, _ := strconv.Atoi(portS)
		key := portS + "/" + proto
		if seen[key] {
			continue
		}
		seen[key] = true
		rules = append(rules, ForwardRule{Port: port, Protocol: proto, Destination: dstIP + ":" + dstPort})
	}
	return rules
}
