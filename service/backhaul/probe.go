package backhaul

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ProbeResult — исход одной проверки одного backhaul'а.
type ProbeResult struct {
	Tier  string       `json:"tier"`
	Class ServiceClass `json:"class"`
	OK    bool         `json:"ok"`
	// Причина провала, пусто при OK.
	Reason string `json:"reason,omitempty"`
	// Фаза, на которой всё сломалось: connect|download|upload|dns.
	Phase string `json:"phase,omitempty"`

	DownBytes   int64  `json:"down_bytes"`
	UpBytes     int64  `json:"up_bytes"`
	DownBps     int64  `json:"down_bps"`
	UpBps       int64  `json:"up_bps"`
	ConnectMs   int64  `json:"connect_ms"`
	TotalMs     int64  `json:"total_ms"`
	RemoteDNSOK *bool  `json:"remote_dns_ok,omitempty"`
	At          string `json:"at"`
}

// probeHTTPClient — HTTP-клиент, ходящий строго через указанный SOCKS5-адаптер,
// с детектом зависания на уровне соединения.
func probeHTTPClient(socksAddr string, timeout, stall time.Duration) *http.Client {
	d := &socks5Dialer{proxyAddr: socksAddr, timeout: timeout}
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			Proxy: nil,
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				c, err := d.DialContext(ctx, network, addr)
				if err != nil {
					return nil, err
				}
				return &stallConn{Conn: c, stall: stall}, nil
			},
			// Каждая проверка — своё соединение. Переиспользование скрыло бы
			// как раз то, что мы измеряем (установку соединения через backhaul).
			DisableKeepAlives:   true,
			MaxIdleConns:        0,
			TLSHandshakeTimeout: timeout,
		},
	}
}

// Probe прогоняет полную проверку одного backhaul'а для одного класса:
// download >30KB, upload >30KB, измерение фактической скорости, детект
// зависания и (опционально) remote-DNS через SOCKS5.
func Probe(ctx context.Context, cfg ProbeConfig, tier Tier, cls ServiceClass) ProbeResult {
	res := ProbeResult{
		Tier:  tier.Name,
		Class: cls,
		At:    time.Now().UTC().Format(time.RFC3339),
	}
	start := time.Now()
	defer func() { res.TotalMs = time.Since(start).Milliseconds() }()

	timeout := time.Duration(cfg.TimeoutSec) * time.Second
	stall := time.Duration(cfg.StallSec) * time.Second
	socksAddr := tier.SocksAddr(cls)

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	client := probeHTTPClient(socksAddr, timeout, stall)

	// --- download ---
	dlStart := time.Now()
	downURL := fmt.Sprintf("%s/down?bytes=%d", strings.TrimRight(cfg.BaseURL, "/"), cfg.DownloadBytes)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downURL, nil)
	if err != nil {
		res.Phase, res.Reason = "connect", err.Error()
		return res
	}
	resp, err := client.Do(req)
	if err != nil {
		res.Phase, res.Reason = "connect", err.Error()
		return res
	}
	res.ConnectMs = time.Since(dlStart).Milliseconds()
	n, err := io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	res.DownBytes = n
	dlElapsed := time.Since(dlStart)
	if err != nil {
		res.Phase = "download"
		res.Reason = fmt.Sprintf("оборвалось после %d байт: %v", n, err)
		return res
	}
	if resp.StatusCode != http.StatusOK {
		res.Phase, res.Reason = "download", fmt.Sprintf("HTTP %d", resp.StatusCode)
		return res
	}
	if n < cfg.DownloadBytes {
		res.Phase = "download"
		res.Reason = fmt.Sprintf("недокачано: %d из %d байт", n, cfg.DownloadBytes)
		return res
	}
	res.DownBps = bytesPerSec(n, dlElapsed)
	if cfg.MinBytesPerSec > 0 && res.DownBps < cfg.MinBytesPerSec {
		res.Phase = "download"
		res.Reason = fmt.Sprintf("скорость %d B/s ниже порога %d B/s", res.DownBps, cfg.MinBytesPerSec)
		return res
	}

	// --- upload ---
	ulStart := time.Now()
	upURL := strings.TrimRight(cfg.BaseURL, "/") + "/up"
	body := io.LimitReader(newFiller(), cfg.UploadBytes)
	upReq, err := http.NewRequestWithContext(ctx, http.MethodPost, upURL, body)
	if err != nil {
		res.Phase, res.Reason = "upload", err.Error()
		return res
	}
	upReq.ContentLength = cfg.UploadBytes
	upReq.Header.Set("Content-Type", "application/octet-stream")
	upResp, err := client.Do(upReq)
	if err != nil {
		res.Phase, res.Reason = "upload", err.Error()
		return res
	}
	upBody, _ := io.ReadAll(io.LimitReader(upResp.Body, 4096))
	upResp.Body.Close()
	ulElapsed := time.Since(ulStart)
	if upResp.StatusCode != http.StatusOK {
		res.Phase, res.Reason = "upload", fmt.Sprintf("HTTP %d: %s", upResp.StatusCode, strings.TrimSpace(string(upBody)))
		return res
	}
	res.UpBytes = cfg.UploadBytes
	res.UpBps = bytesPerSec(cfg.UploadBytes, ulElapsed)
	if cfg.MinBytesPerSec > 0 && res.UpBps < cfg.MinBytesPerSec {
		res.Phase = "upload"
		res.Reason = fmt.Sprintf("скорость отдачи %d B/s ниже порога %d B/s", res.UpBps, cfg.MinBytesPerSec)
		return res
	}

	// --- remote DNS через SOCKS5 ---
	if cfg.CheckRemoteDNS && cfg.DNSProbeHost != "" {
		ok := probeRemoteDNS(ctx, socksAddr, cfg, timeout, stall)
		res.RemoteDNSOK = &ok
		if !ok {
			res.Phase, res.Reason = "dns", "remote DNS через SOCKS5 не отработал"
			return res
		}
	}

	res.OK = true
	return res
}

// probeRemoteDNS проверяет, что адаптер умеет резолвить имя на своей стороне:
// CONNECT уходит с ATYP=DOMAINNAME, локального резолва не происходит.
func probeRemoteDNS(ctx context.Context, socksAddr string, cfg ProbeConfig, timeout, stall time.Duration) bool {
	u, err := url.Parse(cfg.BaseURL)
	if err != nil {
		return false
	}
	port := u.Port()
	if port == "" {
		if u.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	d := &socks5Dialer{proxyAddr: socksAddr, timeout: timeout}
	// Имя, а не IP → сервер SOCKS5 обязан резолвить сам.
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(cfg.DNSProbeHost, port))
	if err != nil {
		return false
	}
	defer conn.Close()

	sc := &stallConn{Conn: conn, stall: stall}
	reqLine := "GET /healthz HTTP/1.1\r\nHost: " + cfg.DNSProbeHost + ":" + port + "\r\nConnection: close\r\n\r\n"
	if _, err := sc.Write([]byte(reqLine)); err != nil {
		return false
	}
	buf := make([]byte, 64)
	n, err := io.ReadFull(sc, buf[:12])
	if err != nil || n < 12 {
		return false
	}
	return strings.HasPrefix(string(buf[:12]), "HTTP/1.1 200")
}

func bytesPerSec(n int64, d time.Duration) int64 {
	if d <= 0 {
		return 0
	}
	return int64(float64(n) / d.Seconds())
}

// filler — бесконечный поток неслучайных байт для upload-проверки.
// Не crypto/rand: нам нужен предсказуемый, дешёвый по CPU источник, чтобы
// измерять сеть, а не генератор.
type filler struct{ b []byte }

func newFiller() io.Reader {
	chunk := make([]byte, 32*1024)
	for i := range chunk {
		chunk[i] = byte('A' + i%26)
	}
	return &filler{b: chunk}
}

func (f *filler) Read(p []byte) (int, error) {
	n := 0
	for n < len(p) {
		c := copy(p[n:], f.b)
		n += c
	}
	return n, nil
}
