package backhaul

import (
	"encoding/json"
	"fmt"
)

// BuildRelayConfig — чистая функция: из Config собирает sing-box-конфиг
// relay-инстанса на RuVDS.
//
// Свойства, которые здесь удерживаются намеренно:
//   - inbound'ы типа "direct" с override_address/override_port — сырой TCP-relay.
//     RuVDS не терминирует ни Reality, ни ShadowTLS, ни MTProto и не знает ключей;
//   - никакого sniff/domain_strategy — поток не разбирается;
//   - никакого multiplex на socks-outbound'ах: каждое клиентское соединение
//     едет отдельным TCP-потоком, общего мультиплекса нет;
//   - у каждого класса сервиса СВОЙ selector и СВОЙ набор socks-адаптеров,
//     поэтому VLESS и MTProto переключаются независимо;
//   - последнее правило маршрутизации — reject: если что-то не совпало ни с
//     одним inbound-тегом, оно не должно утечь наружу с русского IP.
func BuildRelayConfig(c *Config) (map[string]any, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}

	inbounds := make([]any, 0, len(c.Ports))
	for _, p := range c.Ports {
		inbounds = append(inbounds, map[string]any{
			"type":             "direct",
			"tag":              p.Tag,
			"listen":           c.RelayListen,
			"listen_port":      p.ListenPort,
			"network":          "tcp",
			"override_address": c.BackendHost,
			"override_port":    p.TargetPort,
			"tcp_fast_open":    false,
		})
	}

	classes := c.ClassesInUse()
	tiers := c.SortedTiers()

	outbounds := make([]any, 0, len(tiers)*len(classes)+len(classes))
	for _, cls := range classes {
		for _, t := range tiers {
			outbounds = append(outbounds, map[string]any{
				"type":        "socks",
				"tag":         t.OutboundTag(cls),
				"server":      "127.0.0.1",
				"server_port": t.SocksPort[cls],
				"version":     "5",
			})
		}
	}
	for _, cls := range classes {
		members := make([]string, 0, len(tiers))
		for _, t := range tiers {
			members = append(members, t.OutboundTag(cls))
		}
		outbounds = append(outbounds, map[string]any{
			"type": "selector",
			"tag":  SelectorTag(cls),
			// Порядок членов = порядок приоритета: primary → secondary → emergency.
			"outbounds": members,
			"default":   members[0],
			// false: переключение не рвёт уже установленные соединения —
			// иначе каждый failover/recovery выбивал бы всех живых клиентов.
			"interrupt_exist_connections": false,
		})
	}

	rules := make([]any, 0, len(c.Ports)+1)
	for _, cls := range classes {
		tagsForClass := []string{}
		for _, p := range c.Ports {
			if p.Class == cls {
				tagsForClass = append(tagsForClass, p.Tag)
			}
		}
		if len(tagsForClass) == 0 {
			continue
		}
		rules = append(rules, map[string]any{
			"inbound":  tagsForClass,
			"outbound": SelectorTag(cls),
		})
	}
	// Catch-all: ничего лишнего наружу.
	rules = append(rules, map[string]any{"action": "reject"})

	cfg := map[string]any{
		"log": map[string]any{
			"level":     "warn",
			"timestamp": true,
		},
		"inbounds":  inbounds,
		"outbounds": outbounds,
		"route": map[string]any{
			"rules": rules,
		},
		// cache_file здесь намеренно НЕ включаем. Он потребовал бы заранее
		// созданного каталога (sing-box check это пропускает, а run падает),
		// и главное — он был бы вторым, конкурирующим хранилищем выбора
		// маршрута. Авторитет один: файл состояния монитора, который при
		// старте переустанавливает selector'ы в сохранённое положение.
		"experimental": map[string]any{
			"clash_api": map[string]any{
				"external_controller": c.ClashAPI.Listen,
				"secret":              c.ClashAPI.Secret,
			},
		},
	}
	return cfg, nil
}

// RenderRelayConfig — BuildRelayConfig + сериализация с отступами.
func RenderRelayConfig(c *Config) ([]byte, error) {
	cfg, err := BuildRelayConfig(c)
	if err != nil {
		return nil, err
	}
	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("сериализация конфига relay: %w", err)
	}
	return append(out, '\n'), nil
}
