package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
)

const hetznerAPIBase = "https://api.hetzner.cloud/v1"

// --- Public types ---

type FirewallRule struct {
	Direction      string   `json:"direction"`
	Protocol       string   `json:"protocol"`
	Port           string   `json:"port"`
	SourceIPs      []string `json:"source_ips"`
	DestinationIPs []string `json:"destination_ips,omitempty"`
	Description    string   `json:"description"`
}

type FirewallInfo struct {
	Configured   bool           `json:"configured"`
	FirewallID   int64          `json:"firewall_id,omitempty"`
	FirewallName string         `json:"firewall_name,omitempty"`
	ServerID     int64          `json:"server_id,omitempty"`
	ServerName   string         `json:"server_name,omitempty"`
	HetznerIP    string         `json:"hetzner_ip,omitempty"`
	Rules        []FirewallRule `json:"rules,omitempty"`
	UFWRules     []string       `json:"ufw_rules,omitempty"`
}

// --- Internal state ---

var (
	fwToken      string
	fwServerIP   string
	fwMu         sync.Mutex
	fwInited     bool
	fwFirewallID int64
	fwFirewallNm string
	fwServerID   int64
	fwServerNm   string
)

// --- Hetzner API response types ---

type hzServersResp struct {
	Servers []hzServer `json:"servers"`
}

type hzServer struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	PublicNet struct {
		IPv4 struct {
			IP string `json:"ip"`
		} `json:"ipv4"`
	} `json:"public_net"`
}

type hzFirewallsResp struct {
	Firewalls []hzFirewall `json:"firewalls"`
}

type hzFirewall struct {
	ID        int64          `json:"id"`
	Name      string         `json:"name"`
	Rules     []FirewallRule `json:"rules"`
	AppliedTo []struct {
		Type   string `json:"type"`
		Server struct {
			ID int64 `json:"id"`
		} `json:"server"`
	} `json:"applied_to"`
}

type hzSetRulesReq struct {
	Rules []FirewallRule `json:"rules"`
}

// --- Public API ---

func IsFirewallConfigured() bool {
	return os.Getenv("HETZNER_API_TOKEN") != ""
}

func GetHetznerServerIP() string {
	ip := os.Getenv("HETZNER_SERVER_IP")
	if ip == "" {
		ip = os.Getenv("SERVER_IP")
	}
	if ip == "" {
		ip = "49.13.201.110"
	}
	return ip
}

func ensureFirewallInit() error {
	fwMu.Lock()
	defer fwMu.Unlock()

	if fwInited {
		return nil
	}

	fwToken = os.Getenv("HETZNER_API_TOKEN")
	if fwToken == "" {
		return fmt.Errorf("HETZNER_API_TOKEN не задан")
	}

	fwServerIP = GetHetznerServerIP()

	// Найти сервер по IP
	serverID, serverName, err := findServerByIP(fwServerIP)
	if err != nil {
		return fmt.Errorf("не удалось найти сервер Hetzner с IP %s: %w", fwServerIP, err)
	}
	fwServerID = serverID
	fwServerNm = serverName

	// Найти фаервол привязанный к серверу
	firewallID, firewallName, err := findFirewallForServer(serverID)
	if err != nil {
		return fmt.Errorf("не удалось найти фаервол для сервера %d: %w", serverID, err)
	}
	fwFirewallID = firewallID
	fwFirewallNm = firewallName

	fwInited = true
	log.Printf("Hetzner Firewall: сервер '%s' (ID:%d, IP:%s), фаервол '%s' (ID:%d)",
		fwServerNm, fwServerID, fwServerIP, fwFirewallNm, fwFirewallID)
	return nil
}

func GetFirewallInfo() (*FirewallInfo, error) {
	if !IsFirewallConfigured() {
		return &FirewallInfo{Configured: false}, nil
	}

	if err := ensureFirewallInit(); err != nil {
		return nil, err
	}

	rules, err := GetFirewallRules()
	if err != nil {
		return nil, err
	}

	ufwRules, _ := GetUFWRules()

	return &FirewallInfo{
		Configured:   true,
		FirewallID:   fwFirewallID,
		FirewallName: fwFirewallNm,
		ServerID:     fwServerID,
		ServerName:   fwServerNm,
		HetznerIP:    fwServerIP,
		Rules:        rules,
		UFWRules:     ufwRules,
	}, nil
}

func GetFirewallRules() ([]FirewallRule, error) {
	if err := ensureFirewallInit(); err != nil {
		return nil, err
	}

	body, err := hetznerRequest("GET", fmt.Sprintf("/firewalls/%d", fwFirewallID), nil)
	if err != nil {
		return nil, err
	}

	var resp struct {
		Firewall hzFirewall `json:"firewall"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("ошибка парсинга ответа: %w", err)
	}

	return resp.Firewall.Rules, nil
}

func OpenFirewallPort(port int, protocol string, description string) error {
	// Hetzner Cloud Firewall
	if err := ensureFirewallInit(); err != nil {
		return err
	}

	rules, err := GetFirewallRules()
	if err != nil {
		return err
	}

	portStr := fmt.Sprintf("%d", port)

	alreadyOpen := false
	for _, r := range rules {
		if r.Direction == "in" && r.Protocol == protocol && r.Port == portStr {
			alreadyOpen = true
			break
		}
	}

	if !alreadyOpen {
		if description == "" {
			description = fmt.Sprintf("VPN port %d/%s", port, protocol)
		}

		rules = append(rules, FirewallRule{
			Direction:   "in",
			Protocol:    protocol,
			Port:        portStr,
			SourceIPs:   []string{"0.0.0.0/0", "::/0"},
			Description: description,
		})

		if err := setFirewallRules(rules); err != nil {
			return err
		}
	}

	// Локальный UFW на Hetzner
	if err := ufwAllow(port, protocol); err != nil {
		log.Printf("UFW: не удалось открыть %d/%s: %v", port, protocol, err)
	}

	return nil
}

func CloseFirewallPort(port int, protocol string) error {
	// Hetzner Cloud Firewall
	if err := ensureFirewallInit(); err != nil {
		return err
	}

	rules, err := GetFirewallRules()
	if err != nil {
		return err
	}

	portStr := fmt.Sprintf("%d", port)

	filtered := []FirewallRule{}
	for _, r := range rules {
		if r.Direction == "in" && r.Protocol == protocol && r.Port == portStr {
			continue // Убираем это правило
		}
		filtered = append(filtered, r)
	}

	if len(filtered) < len(rules) {
		if err := setFirewallRules(filtered); err != nil {
			return err
		}
	}

	// Локальный UFW на Hetzner
	if err := ufwDeny(port, protocol); err != nil {
		log.Printf("UFW: не удалось закрыть %d/%s: %v", port, protocol, err)
	}

	return nil
}

// --- UFW (local firewall on Hetzner) ---

func ufwAllow(port int, protocol string) error {
	rule := fmt.Sprintf("%d/%s", port, protocol)
	cmd := exec.Command("ufw", "allow", rule)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %w", string(output), err)
	}
	log.Printf("UFW: opened %s", rule)
	return nil
}

func ufwDeny(port int, protocol string) error {
	rule := fmt.Sprintf("%d/%s", port, protocol)
	cmd := exec.Command("ufw", "delete", "allow", rule)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %w", string(output), err)
	}
	log.Printf("UFW: closed %s", rule)
	return nil
}

// GetUFWRules возвращает список открытых портов в UFW
func GetUFWRules() ([]string, error) {
	cmd := exec.Command("ufw", "status")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("ufw status failed: %w", err)
	}
	return parseUFWStatus(string(output)), nil
}

func parseUFWStatus(output string) []string {
	rules := []string{}
	lines := strings.Split(output, "\n")
	started := false
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "---") {
			started = true
			continue
		}
		if !started || line == "" {
			continue
		}
		// Берём только IPv4 правила (пропускаем v6 дубли)
		if strings.Contains(line, "(v6)") {
			continue
		}
		rules = append(rules, line)
	}
	return rules
}

// --- Internal ---

func setFirewallRules(rules []FirewallRule) error {
	req := hzSetRulesReq{Rules: rules}
	_, err := hetznerRequest("POST", fmt.Sprintf("/firewalls/%d/actions/set_rules", fwFirewallID), req)
	return err
}

func findServerByIP(ip string) (int64, string, error) {
	body, err := hetznerRequest("GET", "/servers?per_page=50", nil)
	if err != nil {
		return 0, "", err
	}

	var resp hzServersResp
	if err := json.Unmarshal(body, &resp); err != nil {
		return 0, "", fmt.Errorf("ошибка парсинга: %w", err)
	}

	for _, s := range resp.Servers {
		if s.PublicNet.IPv4.IP == ip {
			return s.ID, s.Name, nil
		}
	}

	return 0, "", fmt.Errorf("сервер с IP %s не найден в Hetzner Cloud", ip)
}

func findFirewallForServer(serverID int64) (int64, string, error) {
	body, err := hetznerRequest("GET", "/firewalls?per_page=50", nil)
	if err != nil {
		return 0, "", err
	}

	var resp hzFirewallsResp
	if err := json.Unmarshal(body, &resp); err != nil {
		return 0, "", fmt.Errorf("ошибка парсинга: %w", err)
	}

	for _, fw := range resp.Firewalls {
		for _, at := range fw.AppliedTo {
			if at.Type == "server" && at.Server.ID == serverID {
				return fw.ID, fw.Name, nil
			}
		}
	}

	return 0, "", fmt.Errorf("фаервол для сервера ID %d не найден", serverID)
}

func hetznerRequest(method, path string, body interface{}) ([]byte, error) {
	var reqBody io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reqBody = bytes.NewReader(b)
	}

	req, err := http.NewRequest(method, hetznerAPIBase+path, reqBody)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Authorization", "Bearer "+fwToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ошибка HTTP запроса: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("ошибка чтения ответа: %w", err)
	}

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("Hetzner API ошибка %d: %s", resp.StatusCode, string(respBody))
	}

	return respBody, nil
}
