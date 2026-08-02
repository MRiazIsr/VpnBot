// backhaul-probe — крошечный HTTP-эндпоинт на стороне Hetzner, через который
// монитор на RuVDS измеряет каждый backhaul: реальная закачка, реальная
// отдача, реальная скорость.
//
// Слушает ТОЛЬКО на адресе внутри защищённого канала (WG). Наружу не выставляется.
//
//	GET  /down?bytes=N  → ровно N байт
//	POST /up            → принимает тело, отвечает {"received":N}
//	GET  /healthz       → 200 ok
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strconv"
	"time"
)

const maxProbeBytes = 32 << 20 // 32 MiB — потолок, чтобы эндпоинт нельзя было использовать как генератор трафика

func main() {
	listen := flag.String("listen", "10.9.0.1:18080", "адрес прослушивания (только внутри WG/loopback)")
	flag.Parse()

	host, _, err := net.SplitHostPort(*listen)
	if err != nil {
		log.Fatalf("некорректный -listen %q: %v", *listen, err)
	}
	if !isPrivateOrLoopback(host) {
		log.Fatalf("отказ: %s не является loopback/приватным адресом — probe наружу не выставляем", host)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "ok")
	})
	mux.HandleFunc("/down", handleDown)
	mux.HandleFunc("/up", handleUp)

	srv := &http.Server{
		Addr:    *listen,
		Handler: mux,
		// Щедро: проверка гоняет мегабайты через возможно медленный backhaul.
		ReadTimeout:       2 * time.Minute,
		WriteTimeout:      2 * time.Minute,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       30 * time.Second,
	}
	log.Printf("backhaul-probe слушает %s", *listen)
	log.Fatal(srv.ListenAndServe())
}

func handleDown(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	n := int64(128 * 1024)
	if v := r.URL.Query().Get("bytes"); v != "" {
		parsed, err := strconv.ParseInt(v, 10, 64)
		if err != nil || parsed <= 0 {
			http.Error(w, "bad bytes", http.StatusBadRequest)
			return
		}
		n = parsed
	}
	if n > maxProbeBytes {
		http.Error(w, "too large", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(n, 10))
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return
	}

	chunk := make([]byte, 32*1024)
	for i := range chunk {
		chunk[i] = byte('A' + i%26)
	}
	for n > 0 {
		c := int64(len(chunk))
		if n < c {
			c = n
		}
		if _, err := w.Write(chunk[:c]); err != nil {
			return
		}
		n -= c
	}
}

func handleUp(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodPut {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	n, err := io.Copy(io.Discard, io.LimitReader(r.Body, maxProbeBytes))
	if err != nil {
		http.Error(w, "read error: "+err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]int64{"received": n})
}

// isPrivateOrLoopback — страховка от случайного выставления probe в интернет.
func isPrivateOrLoopback(host string) bool {
	if host == "" || host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast()
}
