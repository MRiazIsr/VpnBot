package service

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
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

	output, err := runSSH(client, "nft list table ip relay")
	if err != nil {
		return nil, fmt.Errorf("ошибка nft list table ip relay (миграция на nftables не выполнена?): %w", err)
	}
	return parseNftForwards(output, GetHetznerServerIP()), nil
}

func AddForward(port int, protocol string) error {
	client, err := sshConnect()
	if err != nil {
		return err
	}
	defer client.Close()

	conf, err := runSSH(client, "cat "+nftConfPath)
	if err != nil {
		return fmt.Errorf("не удалось прочитать %s: %w", nftConfPath, err)
	}
	newConf, err := insertForward(conf, port, protocol, GetHetznerServerIP())
	if err != nil {
		return err
	}
	if newConf == conf {
		return nil
	}
	return applyNftConf(client, newConf)
}

func RemoveForward(port int, protocol string) error {
	client, err := sshConnect()
	if err != nil {
		return err
	}
	defer client.Close()

	conf, err := runSSH(client, "cat "+nftConfPath)
	if err != nil {
		return fmt.Errorf("не удалось прочитать %s: %w", nftConfPath, err)
	}
	newConf, err := removeForward(conf, port, protocol)
	if err != nil {
		return err
	}
	if newConf == conf {
		return nil
	}
	return applyNftConf(client, newConf)
}

// --- Internal ---

func persistIptables(client *ssh.Client) {
	// Пробуем несколько способов сохранить правила
	runSSH(client, "iptables-save > /etc/iptables/rules.v4 2>/dev/null; true")
	runSSH(client, "netfilter-persistent save 2>/dev/null; true")
}

// applyNftConf writes conf to a temp file, validates it with `nft -c -f`
// (rejects on syntax/semantic error WITHOUT touching the live ruleset),
// atomically replaces /etc/nftables.conf, then reloads with `nft -f`.
// On validation failure the live config and ruleset are unchanged.
func applyNftConf(client *ssh.Client, conf string) error {
	tmp := nftConfPath + ".vpnbot.tmp"
	heredoc := fmt.Sprintf("cat > %s <<'VPNBOT_NFT_EOF'\n%s\nVPNBOT_NFT_EOF", tmp, conf)
	if _, err := runSSH(client, heredoc); err != nil {
		return fmt.Errorf("не удалось записать временный конфиг: %w", err)
	}
	if out, err := runSSH(client, "nft -c -f "+tmp); err != nil {
		runSSH(client, "rm -f "+tmp)
		return fmt.Errorf("nft валидация не прошла, конфиг НЕ изменён: %v (%s)", err, strings.TrimSpace(out))
	}
	if _, err := runSSH(client, fmt.Sprintf("mv %s %s && nft -f %s", tmp, nftConfPath, nftConfPath)); err != nil {
		return fmt.Errorf("не удалось применить nft конфиг: %w", err)
	}
	return nil
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
