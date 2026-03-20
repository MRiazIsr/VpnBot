package service

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// --- Public types ---

type ForwardRule struct {
	Port        int    `json:"port"`
	Protocol    string `json:"protocol"`
	Destination string `json:"destination"`
}

type PortForwardInfo struct {
	Configured bool          `json:"configured"`
	RuVDSIP    string        `json:"ruvds_ip,omitempty"`
	HetznerIP  string        `json:"hetzner_ip,omitempty"`
	Rules      []ForwardRule `json:"rules,omitempty"`
}

// --- Public API ---

func IsPortForwardConfigured() bool {
	return os.Getenv("RUVDS_IP") != ""
}

func GetRuVDSIP() string {
	return os.Getenv("RUVDS_IP")
}

func GetPortForwardInfo() (*PortForwardInfo, error) {
	if !IsPortForwardConfigured() {
		return &PortForwardInfo{Configured: false}, nil
	}

	rules, err := GetForwardRules()
	if err != nil {
		return nil, err
	}

	return &PortForwardInfo{
		Configured: true,
		RuVDSIP:    GetRuVDSIP(),
		HetznerIP:  GetHetznerServerIP(),
		Rules:      rules,
	}, nil
}

func GetForwardRules() ([]ForwardRule, error) {
	client, err := sshConnect()
	if err != nil {
		return nil, err
	}
	defer client.Close()

	output, err := runSSH(client, "iptables-save -t nat")
	if err != nil {
		return nil, fmt.Errorf("ошибка выполнения iptables-save: %w", err)
	}

	return parseIptablesRules(output, GetHetznerServerIP()), nil
}

func AddForward(port int, protocol string) error {
	client, err := sshConnect()
	if err != nil {
		return err
	}
	defer client.Close()

	hetznerIP := GetHetznerServerIP()
	portStr := strconv.Itoa(port)

	// DNAT в PREROUTING
	cmd := fmt.Sprintf("iptables -t nat -C PREROUTING -p %s --dport %s -j DNAT --to-destination %s:%s 2>/dev/null || iptables -t nat -A PREROUTING -p %s --dport %s -j DNAT --to-destination %s:%s",
		protocol, portStr, hetznerIP, portStr,
		protocol, portStr, hetznerIP, portStr)

	if _, err := runSSH(client, cmd); err != nil {
		return fmt.Errorf("ошибка добавления DNAT правила: %w", err)
	}

	// MASQUERADE в POSTROUTING
	cmd = fmt.Sprintf("iptables -t nat -C POSTROUTING -d %s -p %s --dport %s -j MASQUERADE 2>/dev/null || iptables -t nat -A POSTROUTING -d %s -p %s --dport %s -j MASQUERADE",
		hetznerIP, protocol, portStr,
		hetznerIP, protocol, portStr)

	if _, err := runSSH(client, cmd); err != nil {
		return fmt.Errorf("ошибка добавления MASQUERADE правила: %w", err)
	}

	persistIptables(client)
	return nil
}

func RemoveForward(port int, protocol string) error {
	client, err := sshConnect()
	if err != nil {
		return err
	}
	defer client.Close()

	hetznerIP := GetHetznerServerIP()
	portStr := strconv.Itoa(port)

	// Удаляем DNAT
	cmd := fmt.Sprintf("iptables -t nat -D PREROUTING -p %s --dport %s -j DNAT --to-destination %s:%s 2>/dev/null; true",
		protocol, portStr, hetznerIP, portStr)
	runSSH(client, cmd)

	// Удаляем MASQUERADE
	cmd = fmt.Sprintf("iptables -t nat -D POSTROUTING -d %s -p %s --dport %s -j MASQUERADE 2>/dev/null; true",
		hetznerIP, protocol, portStr)
	runSSH(client, cmd)

	persistIptables(client)
	return nil
}

// --- Internal ---

var dnatRegex = regexp.MustCompile(`-A PREROUTING.*-p (\w+).*--dport (\d+) -j DNAT --to-destination ([\d.]+:\d+)`)

func parseIptablesRules(output string, hetznerIP string) []ForwardRule {
	rules := []ForwardRule{}
	seen := make(map[string]bool)

	for _, line := range strings.Split(output, "\n") {
		matches := dnatRegex.FindStringSubmatch(line)
		if len(matches) < 4 {
			continue
		}

		proto := matches[1]
		port, _ := strconv.Atoi(matches[2])
		dest := matches[3]

		// Фильтруем только правила с нашим Hetzner IP
		if !strings.HasPrefix(dest, hetznerIP+":") {
			continue
		}

		key := fmt.Sprintf("%d/%s", port, proto)
		if seen[key] {
			continue
		}
		seen[key] = true

		rules = append(rules, ForwardRule{
			Port:        port,
			Protocol:    proto,
			Destination: dest,
		})
	}

	return rules
}

func persistIptables(client *ssh.Client) {
	// Пробуем несколько способов сохранить правила
	runSSH(client, "iptables-save > /etc/iptables/rules.v4 2>/dev/null; true")
	runSSH(client, "netfilter-persistent save 2>/dev/null; true")
}

func sshConnect() (*ssh.Client, error) {
	ruvdsIP := os.Getenv("RUVDS_IP")
	if ruvdsIP == "" {
		return nil, fmt.Errorf("RUVDS_IP не задан")
	}

	keyPath := os.Getenv("RUVDS_SSH_KEY_PATH")
	if keyPath == "" {
		home, _ := os.UserHomeDir()
		keyPath = filepath.Join(home, ".ssh", "id_rsa")
	}

	keyData, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, fmt.Errorf("не удалось прочитать SSH ключ %s: %w", keyPath, err)
	}

	signer, err := ssh.ParsePrivateKey(keyData)
	if err != nil {
		return nil, fmt.Errorf("не удалось распарсить SSH ключ: %w", err)
	}

	user := os.Getenv("RUVDS_SSH_USER")
	if user == "" {
		user = "root"
	}

	port := os.Getenv("RUVDS_SSH_PORT")
	if port == "" {
		port = "22"
	}

	config := &ssh.ClientConfig{
		User: user,
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(signer),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         10 * time.Second,
	}

	addr := net.JoinHostPort(ruvdsIP, port)
	client, err := ssh.Dial("tcp", addr, config)
	if err != nil {
		return nil, fmt.Errorf("не удалось подключиться по SSH к %s: %w", addr, err)
	}

	return client, nil
}

func runSSH(client *ssh.Client, command string) (string, error) {
	session, err := client.NewSession()
	if err != nil {
		return "", fmt.Errorf("не удалось создать SSH сессию: %w", err)
	}
	defer session.Close()

	output, err := session.CombinedOutput(command)
	return string(output), err
}
