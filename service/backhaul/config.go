// Package backhaul описывает тройной backhaul RuVDS → Hetzner:
// три независимо переключаемых маршрута, каждый из которых представлен на
// RuVDS локальным SOCKS5-адаптером на 127.0.0.1.
//
// Схема:
//
//	Телефон ──(прежний адрес и порт RuVDS)──▶ sing-box relay (RuVDS)
//	                                            │ direct inbound, override → backend Hetzner
//	                                            ▼
//	                             selector (свой на каждый класс сервиса)
//	                                ├─ primary   : SOCKS5 127.0.0.1:1108x  (frp ← IL residential → WG → Hetzner)
//	                                ├─ secondary : SOCKS5 127.0.0.1:1108x  (sing-box vless+ws → YC → Hetzner)
//	                                └─ emergency : SOCKS5 127.0.0.1:1108x  (ssh -D, инициирует RuVDS)
//
// Направление emergency инвертировано против изначального замысла осознанно:
// замер 02.08.2026 показал, что RuVDS не принимает входящий TCP извне РФ, и
// `ssh -R` с Hetzner установиться не может в принципе. Подробности —
// docs/ruvds-rescue.md.
//
// Reality/ShadowTLS/MTProto завершаются ТОЛЬКО на Hetzner: RuVDS передаёт
// сырые TCP-потоки и не владеет ключами.
package backhaul

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
)

// ServiceClass — класс сервиса, для которого держится отдельный selector.
type ServiceClass string

const (
	ClassVLESS   ServiceClass = "vless"
	ClassMTProto ServiceClass = "mtproto"
)

// AllClasses — порядок обхода классов; фиксированный, чтобы генерация конфига
// была детерминированной.
var AllClasses = []ServiceClass{ClassVLESS, ClassMTProto}

// Валидные имена tier'ов, в порядке предпочтения.
const (
	TierPrimary   = "primary"
	TierSecondary = "secondary"
	TierEmergency = "emergency"
)

// Tier — один backhaul-маршрут. Rank задаёт приоритет: 1 — primary.
// SocksPort — порт локального SOCKS5-адаптера (127.0.0.1) для каждого класса.
// Порты РАЗНЫЕ для разных классов: требование «не создавать один общий
// мультиплексированный поток для всех пользователей и обоих сервисов».
type Tier struct {
	Name      string               `json:"name"`
	Rank      int                  `json:"rank"`
	SocksPort map[ServiceClass]int `json:"socks_port"`
	// Disabled — tier описан, но физически ещё/уже не существует (например,
	// резидентный узел не подключён). Такой tier не попадает ни в selector'ы,
	// ни в опрос: иначе монитор бесконечно ронял бы заведомо отсутствующий
	// маршрут и засорял журнал. Включение обратно — правка флага, регенерация
	// конфига, sing-box check, reload.
	Disabled bool `json:"disabled,omitempty"`
	// Extra — произвольные метаданные для логов/диагностики (описание транспорта).
	Extra map[string]string `json:"extra,omitempty"`
}

// SocksAddr — адрес локального адаптера этого tier'а для класса cls.
func (t Tier) SocksAddr(cls ServiceClass) string {
	return fmt.Sprintf("127.0.0.1:%d", t.SocksPort[cls])
}

// OutboundTag — тег sing-box outbound для (tier, class).
func (t Tier) OutboundTag(cls ServiceClass) string {
	return fmt.Sprintf("bh-%s-%s", t.Name, cls)
}

// PortMap — публичный порт RuVDS → backend-порт Hetzner. Публичный порт НЕ
// меняется никогда (профили на телефонах остаются прежними); backend-порт
// живёт на Hetzner и слушает только на WG-адресе.
type PortMap struct {
	Tag        string       `json:"tag"`         // тег relay-inbound, напр. "in-ru-2059"
	Class      ServiceClass `json:"class"`       // какой selector обслуживает
	ListenPort int          `json:"listen_port"` // публичный порт на RuVDS
	TargetPort int          `json:"target_port"` // backend-порт на Hetzner (за WG)
	Comment    string       `json:"comment,omitempty"`
}

// ProbeConfig — параметры активной проверки backhaul'а.
// Проверка НЕ ping и НЕ tcp-connect: реальный upload+download через SOCKS5
// с измерением скорости и детектом зависания.
type ProbeConfig struct {
	// URL пробника на стороне Hetzner (слушает только на WG-адресе).
	BaseURL string `json:"base_url"`
	// Сколько байт скачивать и заливать за одну проверку.
	DownloadBytes int64 `json:"download_bytes"`
	UploadBytes   int64 `json:"upload_bytes"`
	// Общий дедлайн одной проверки, секунды.
	TimeoutSec int `json:"timeout_sec"`
	// Максимальная пауза без единого байта — детект «повисшего» соединения.
	StallSec int `json:"stall_sec"`
	// Минимально допустимая скорость, байт/с. Ниже — проверка считается провалом
	// (канал жив, но непригоден).
	MinBytesPerSec int64 `json:"min_bytes_per_sec"`
	// Проверять ли remote-DNS через SOCKS5 (CONNECT по доменному имени).
	CheckRemoteDNS bool   `json:"check_remote_dns"`
	DNSProbeHost   string `json:"dns_probe_host,omitempty"`
}

// MonitorConfig — гистерезис и hold-down, чтобы исключить постоянные переключения.
type MonitorConfig struct {
	IntervalSec int `json:"interval_sec"` // период опроса
	// Сколько подряд провалов, чтобы признать tier мёртвым (failover).
	FailThreshold int `json:"fail_threshold"`
	// Сколько подряд успехов, чтобы признать tier живым (recovery).
	RecoverThreshold int `json:"recover_threshold"`
	// Минимальное время между двумя переключениями одного и того же selector'а.
	HoldDownSec int `json:"hold_down_sec"`
	// Сколько времени tier должен непрерывно быть здоровым, прежде чем на него
	// разрешено вернуться с менее приоритетного (anti-flapping).
	StableBeforeReturnSec int `json:"stable_before_return_sec"`
}

// ClashAPIConfig — локальный официальный API sing-box (только 127.0.0.1).
type ClashAPIConfig struct {
	Listen string `json:"listen"` // "127.0.0.1:19090"
	Secret string `json:"secret"`
}

// Config — полная конфигурация backhaul-подсистемы. Живёт на RuVDS в
// /etc/vpnbot/backhaul.json. Секретов, кроме локального API-секрета, не содержит.
type Config struct {
	// RelayListen — на каком адресе relay принимает клиентов. "::" — все.
	RelayListen string `json:"relay_listen"`
	// BackendHost — адрес Hetzner внутри защищённого канала (WG). Один и тот же
	// для всех трёх backhaul'ов: WG-адрес Hetzner локален для самого Hetzner,
	// поэтому и ssh/wss-выходы (они физически на Hetzner) его видят.
	BackendHost string `json:"backend_host"`

	Tiers []Tier    `json:"tiers"`
	Ports []PortMap `json:"ports"`

	ClashAPI ClashAPIConfig `json:"clash_api"`
	Probe    ProbeConfig    `json:"probe"`
	Monitor  MonitorConfig  `json:"monitor"`

	// StatePath — persisted-состояние монитора (переживает рестарт).
	StatePath string `json:"state_path"`
	// LogPath — файл structured-логов (JSON lines); пусто → stderr.
	LogPath string `json:"log_path"`
	// RelayConfigPath — куда генератор пишет sing-box-конфиг relay-инстанса.
	RelayConfigPath string `json:"relay_config_path"`
}

// SelectorTag — тег selector'а для класса.
func SelectorTag(cls ServiceClass) string { return "sel-" + string(cls) }

// SortedTiers возвращает ВКЛЮЧЁННЫЕ tier'ы по возрастанию Rank (primary первым).
// Отключённые сюда не попадают — их нет ни в конфиге sing-box, ни в опросе.
func (c *Config) SortedTiers() []Tier {
	out := make([]Tier, 0, len(c.Tiers))
	for _, t := range c.Tiers {
		if !t.Disabled {
			out = append(out, t)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Rank < out[j].Rank })
	return out
}

// AllTiersSorted — включая отключённые. Нужен для отчётов и для того, чтобы
// файл состояния не терял историю выключенного маршрута.
func (c *Config) AllTiersSorted() []Tier {
	out := make([]Tier, len(c.Tiers))
	copy(out, c.Tiers)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Rank < out[j].Rank })
	return out
}

// TierByName — поиск tier'а по имени.
func (c *Config) TierByName(name string) (Tier, bool) {
	for _, t := range c.Tiers {
		if t.Name == name {
			return t, true
		}
	}
	return Tier{}, false
}

// ClassesInUse — классы, для которых реально описаны порты.
func (c *Config) ClassesInUse() []ServiceClass {
	seen := map[ServiceClass]bool{}
	for _, p := range c.Ports {
		seen[p.Class] = true
	}
	out := []ServiceClass{}
	for _, cls := range AllClasses {
		if seen[cls] {
			out = append(out, cls)
		}
	}
	return out
}

// Validate проверяет инварианты, нарушение которых сломало бы прод.
func (c *Config) Validate() error {
	if c.BackendHost == "" {
		return fmt.Errorf("backend_host пуст")
	}
	if len(c.Tiers) == 0 {
		return fmt.Errorf("не задан ни один tier")
	}
	if len(c.Ports) == 0 {
		return fmt.Errorf("не задан ни один порт")
	}

	ranks := map[int]string{}
	names := map[string]bool{}
	socksPorts := map[int]string{}
	for _, t := range c.Tiers {
		if t.Name == "" {
			return fmt.Errorf("tier без имени")
		}
		if names[t.Name] {
			return fmt.Errorf("дублирующийся tier %q", t.Name)
		}
		names[t.Name] = true
		if prev, ok := ranks[t.Rank]; ok {
			return fmt.Errorf("tier %q и %q имеют одинаковый rank %d", prev, t.Name, t.Rank)
		}
		ranks[t.Rank] = t.Name

		if t.Disabled {
			// У выключенного tier'а адаптеров нет — требовать от него порты
			// бессмысленно. Но и занимать ими чужие номера он не должен.
			continue
		}
		for _, cls := range c.ClassesInUse() {
			port, ok := t.SocksPort[cls]
			if !ok || port <= 0 || port > 65535 {
				return fmt.Errorf("tier %q: нет валидного socks_port для класса %s", t.Name, cls)
			}
			key := fmt.Sprintf("%s/%s", t.Name, cls)
			if prev, dup := socksPorts[port]; dup {
				return fmt.Errorf("socks-порт %d занят дважды: %s и %s", port, prev, key)
			}
			socksPorts[port] = key
		}
	}
	if len(c.SortedTiers()) == 0 {
		return fmt.Errorf("все tier'ы отключены — маршрутизировать некуда")
	}

	listen := map[int]string{}
	tags := map[string]bool{}
	for _, p := range c.Ports {
		if p.Tag == "" {
			return fmt.Errorf("порт %d без тега", p.ListenPort)
		}
		if tags[p.Tag] {
			return fmt.Errorf("дублирующийся тег inbound %q", p.Tag)
		}
		tags[p.Tag] = true
		if p.ListenPort <= 0 || p.ListenPort > 65535 {
			return fmt.Errorf("inbound %q: некорректный listen_port %d", p.Tag, p.ListenPort)
		}
		if p.TargetPort <= 0 || p.TargetPort > 65535 {
			return fmt.Errorf("inbound %q: некорректный target_port %d", p.Tag, p.TargetPort)
		}
		if prev, dup := listen[p.ListenPort]; dup {
			return fmt.Errorf("listen_port %d занят дважды: %s и %s", p.ListenPort, prev, p.Tag)
		}
		listen[p.ListenPort] = p.Tag
		if p.Class != ClassVLESS && p.Class != ClassMTProto {
			return fmt.Errorf("inbound %q: неизвестный класс %q", p.Tag, p.Class)
		}
	}

	if c.ClashAPI.Listen == "" {
		return fmt.Errorf("clash_api.listen пуст")
	}
	if !isLoopbackAddr(c.ClashAPI.Listen) {
		return fmt.Errorf("clash_api.listen=%q должен быть на 127.0.0.1 — наружу API не выставляем", c.ClashAPI.Listen)
	}
	if c.ClashAPI.Secret == "" {
		return fmt.Errorf("clash_api.secret пуст — API без секрета не поднимаем")
	}

	if c.Monitor.FailThreshold < 1 {
		return fmt.Errorf("monitor.fail_threshold должен быть ≥1")
	}
	if c.Monitor.RecoverThreshold < 1 {
		return fmt.Errorf("monitor.recover_threshold должен быть ≥1")
	}
	if c.Monitor.IntervalSec < 1 {
		return fmt.Errorf("monitor.interval_sec должен быть ≥1")
	}
	if c.Probe.DownloadBytes < 30*1024 {
		return fmt.Errorf("probe.download_bytes=%d — требуется >30KB", c.Probe.DownloadBytes)
	}
	if c.Probe.UploadBytes < 30*1024 {
		return fmt.Errorf("probe.upload_bytes=%d — требуется >30KB", c.Probe.UploadBytes)
	}
	if c.Probe.BaseURL == "" {
		return fmt.Errorf("probe.base_url пуст")
	}
	if c.Probe.TimeoutSec < 1 {
		return fmt.Errorf("probe.timeout_sec должен быть ≥1")
	}
	return nil
}

// isLoopbackAddr — грубая, но достаточная проверка «слушаем только на loopback».
func isLoopbackAddr(hostPort string) bool {
	h := hostPort
	if i := strings.LastIndex(hostPort, ":"); i > 0 {
		h = hostPort[:i]
	}
	h = strings.Trim(h, "[]")
	return h == "127.0.0.1" || h == "localhost" || h == "::1" ||
		strings.HasPrefix(h, "127.")
}

// LoadConfig читает и валидирует конфиг.
func LoadConfig(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("чтение %s: %w", path, err)
	}
	var c Config
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("разбор %s: %w", path, err)
	}
	c.applyDefaults()
	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &c, nil
}

func (c *Config) applyDefaults() {
	if c.RelayListen == "" {
		c.RelayListen = "::"
	}
	if c.StatePath == "" {
		c.StatePath = "/var/lib/vpnbot/backhaul-state.json"
	}
	if c.RelayConfigPath == "" {
		c.RelayConfigPath = "/etc/sing-box-relay/config.json"
	}
	if c.Monitor.IntervalSec == 0 {
		c.Monitor.IntervalSec = 20
	}
	if c.Monitor.FailThreshold == 0 {
		c.Monitor.FailThreshold = 3
	}
	if c.Monitor.RecoverThreshold == 0 {
		c.Monitor.RecoverThreshold = 5
	}
	if c.Monitor.HoldDownSec == 0 {
		c.Monitor.HoldDownSec = 180
	}
	if c.Monitor.StableBeforeReturnSec == 0 {
		c.Monitor.StableBeforeReturnSec = 300
	}
	if c.Probe.DownloadBytes == 0 {
		c.Probe.DownloadBytes = 128 * 1024
	}
	if c.Probe.UploadBytes == 0 {
		c.Probe.UploadBytes = 128 * 1024
	}
	if c.Probe.TimeoutSec == 0 {
		c.Probe.TimeoutSec = 15
	}
	if c.Probe.StallSec == 0 {
		c.Probe.StallSec = 5
	}
	if c.Probe.MinBytesPerSec == 0 {
		c.Probe.MinBytesPerSec = 24 * 1024
	}
}
