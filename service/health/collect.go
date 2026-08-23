package health

import (
	"fmt"

	"vpnbot/service"
)

// Collect builds one cycle of signals.
//   - Per enabled inbound: chain reachability via service.CheckAllInboundPorts().
//     Both legs down -> Bad. Exactly one leg down -> Partial (early warning).
func Collect() []SignalResult {
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

	return out
}

// Сигнал telemt удалён намеренно. Он считал долю "handshake timeout" в журнале
// telemt и поднимал тревогу с 40%, но эту долю набивают старые ссылки, бьющие
// напрямую в Hetzner:9443 в обход туннеля. Замер 23.08.2026 на боевом хосте:
// 3 таймаута против 5 установленных за час, то есть 37% — вплотную к порогу.
// Сигнал перескакивал границу туда-обратно, и каждый переход слал сообщение
// админу. Метрика, живущая на пороге, вреднее отсутствия метрики.
//
// Здоровье MTProto надо мерить доставкой через плечо, а не текстом в журнале;
// пока такой пробы нет, сигнала нет.
