package health

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"vpnbot/database"
	"vpnbot/service"
)

// Collect builds one cycle of signals.
//   - Per enabled inbound: chain reachability via service.CheckAllInboundPorts().
//     Both legs down -> Bad. Exactly one leg down -> Partial (early warning).
//   - telemt: night-robust failure ratio from local journald (the app runs on
//     the Hetzner host alongside telemt). Ratio is over ATTEMPTS, so zero
//     attempts (night) -> not Bad.
func Collect(intervalSec int) []SignalResult {
	var out []SignalResult

	for _, pc := range service.CheckAllInboundPorts() {
		svc := fmt.Sprintf("inbound:%d", pc.Port)
		label := fmt.Sprintf("%s :%d (%s)", pc.Protocol, pc.Port, pc.Tag)
		bothDown := !pc.RuVDSReachable && !pc.HetznerReachable
		oneDown := pc.RuVDSReachable != pc.HetznerReachable
		reason := ""
		if bothDown {
			reason = "порт недоступен (RuVDS и Hetzner)"
		} else if oneDown {
			if !pc.RuVDSReachable {
				reason = "RuVDS-цепочка недоступна, Hetzner ок"
			} else {
				reason = "Hetzner недоступен, RuVDS ок"
			}
		}
		out = append(out, SignalResult{
			Service: svc, Label: label,
			Bad:     bothDown,
			Partial: oneDown,
			Reason:  reason,
		})
	}

	var tcfg database.TelemetConfig
	if database.DB.First(&tcfg).Error == nil && tcfg.Enabled {
		out = append(out, telemtSignal(intervalSec))
	}
	return out
}

// telemtSignal reads telemt journald for the last interval and flags Bad when
// (handshake-timeout) dominates handshake attempts. Night-robust: with no
// attempts the ratio is undefined -> treated as healthy.
func telemtSignal(intervalSec int) SignalResult {
	res := SignalResult{Service: "telemt", Label: "MTProto (telemt)"}
	since := fmt.Sprintf("%d sec ago", intervalSec)
	out, err := exec.Command("journalctl", "-u", "telemt", "--no-pager",
		"--since", since).CombinedOutput()
	if err != nil {
		// Cannot read journal (not on telemt host / no perms): do not alarm.
		return res
	}
	text := string(out)
	timeouts := strings.Count(text, "handshake timeout")
	established := strings.Count(text, "peer=127.0.0.1") +
		strings.Count(text, "Connected") // tunnel-delivered + generic success
	attempts := timeouts + established
	if attempts < 5 {
		return res // too few attempts (e.g. night) — not a signal
	}
	failPct := timeouts * 100 / attempts
	if failPct >= 70 {
		res.Bad = true
		res.Reason = "telemt: " + strconv.Itoa(failPct) + "% попыток с handshake timeout"
	} else if failPct >= 40 {
		res.Partial = true
		res.Reason = "telemt: рост ошибок рукопожатия (" + strconv.Itoa(failPct) + "%)"
	}
	return res
}

var _ = time.Second // reserved for future probe timeout
