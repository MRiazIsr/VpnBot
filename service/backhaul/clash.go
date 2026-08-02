package backhaul

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"
)

// ClashClient — минимальный клиент официального локального API sing-box
// (experimental.clash_api). Ходит только на 127.0.0.1.
type ClashClient struct {
	base   string
	secret string
	hc     *http.Client
}

// NewClashClient — клиент к локальному API. listen — "127.0.0.1:19090".
func NewClashClient(listen, secret string) (*ClashClient, error) {
	if !isLoopbackAddr(listen) {
		return nil, fmt.Errorf("отказ: API sing-box должен быть на loopback, а не %q", listen)
	}
	return &ClashClient{
		base:   "http://" + listen,
		secret: secret,
		hc: &http.Client{
			Timeout: 5 * time.Second,
			Transport: &http.Transport{
				// Никаких прокси из окружения и никакого keep-alive-пула на
				// длительное время: API дёргается редко.
				Proxy: nil,
				DialContext: (&net.Dialer{
					Timeout: 3 * time.Second,
				}).DialContext,
				MaxIdleConnsPerHost: 1,
			},
		},
	}, nil
}

func (c *ClashClient) do(ctx context.Context, method, path string, body any) ([]byte, error) {
	var rdr io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rdr = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rdr)
	if err != nil {
		return nil, err
	}
	if c.secret != "" {
		req.Header.Set("Authorization", "Bearer "+c.secret)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%s %s → %d: %s", method, path, resp.StatusCode, string(raw))
	}
	return raw, nil
}

type proxyInfo struct {
	Type string   `json:"type"`
	Name string   `json:"name"`
	Now  string   `json:"now"`
	All  []string `json:"all"`
}

// Selected — какой member сейчас активен у selector'а.
func (c *ClashClient) Selected(ctx context.Context, selector string) (string, error) {
	raw, err := c.do(ctx, http.MethodGet, "/proxies/"+url.PathEscape(selector), nil)
	if err != nil {
		return "", err
	}
	var p proxyInfo
	if err := json.Unmarshal(raw, &p); err != nil {
		return "", fmt.Errorf("разбор ответа /proxies/%s: %w", selector, err)
	}
	return p.Now, nil
}

// Members — список членов selector'а в порядке конфигурации.
func (c *ClashClient) Members(ctx context.Context, selector string) ([]string, error) {
	raw, err := c.do(ctx, http.MethodGet, "/proxies/"+url.PathEscape(selector), nil)
	if err != nil {
		return nil, err
	}
	var p proxyInfo
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("разбор ответа /proxies/%s: %w", selector, err)
	}
	return p.All, nil
}

// Select — переключить selector на member.
func (c *ClashClient) Select(ctx context.Context, selector, member string) error {
	_, err := c.do(ctx, http.MethodPut, "/proxies/"+url.PathEscape(selector),
		map[string]string{"name": member})
	if err != nil {
		return fmt.Errorf("переключение %s → %s: %w", selector, member, err)
	}
	return nil
}

// Ping — проверка, что API отвечает.
func (c *ClashClient) Ping(ctx context.Context) error {
	_, err := c.do(ctx, http.MethodGet, "/version", nil)
	return err
}
