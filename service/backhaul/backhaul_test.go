package backhaul

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"
	"time"
)

func testConfig() *Config {
	c := &Config{
		RelayListen: "::",
		BackendHost: "10.9.0.1",
		Tiers: []Tier{
			{Name: TierPrimary, Rank: 1, SocksPort: map[ServiceClass]int{ClassVLESS: 11080, ClassMTProto: 11090}},
			{Name: TierSecondary, Rank: 2, SocksPort: map[ServiceClass]int{ClassVLESS: 11081, ClassMTProto: 11091}},
			{Name: TierEmergency, Rank: 3, SocksPort: map[ServiceClass]int{ClassVLESS: 11082, ClassMTProto: 11092}},
		},
		Ports: []PortMap{
			{Tag: "in-ru-2059", Class: ClassVLESS, ListenPort: 2059, TargetPort: 21059},
			{Tag: "in-ru-2060", Class: ClassVLESS, ListenPort: 2060, TargetPort: 21060},
			{Tag: "in-mtproto-9443", Class: ClassMTProto, ListenPort: 9443, TargetPort: 21443},
		},
		ClashAPI: ClashAPIConfig{Listen: "127.0.0.1:19090", Secret: "s3cr3t"},
		Probe:    ProbeConfig{BaseURL: "http://10.9.0.1:18080"},
	}
	c.applyDefaults()
	return c
}

func TestValidateRejectsPublicClashAPI(t *testing.T) {
	c := testConfig()
	c.ClashAPI.Listen = "0.0.0.0:19090"
	if err := c.Validate(); err == nil {
		t.Fatal("ожидали отказ: API sing-box не должен слушать наружу")
	}
}

func TestValidateRejectsSharedSocksPortAcrossClasses(t *testing.T) {
	c := testConfig()
	c.Tiers[0].SocksPort[ClassMTProto] = c.Tiers[0].SocksPort[ClassVLESS]
	if err := c.Validate(); err == nil {
		t.Fatal("ожидали отказ: один socks-порт на оба класса — это общий поток")
	}
}

func TestValidateRejectsTooSmallProbePayload(t *testing.T) {
	c := testConfig()
	c.Probe.DownloadBytes = 4096
	if err := c.Validate(); err == nil {
		t.Fatal("ожидали отказ: проверка обязана гонять >30KB")
	}
}

func TestBuildRelayConfigShape(t *testing.T) {
	c := testConfig()
	raw, err := RenderRelayConfig(c)
	if err != nil {
		t.Fatalf("RenderRelayConfig: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("сгенерирован невалидный JSON: %v", err)
	}

	inbounds := got["inbounds"].([]any)
	if len(inbounds) != 3 {
		t.Fatalf("ожидали 3 inbound, получили %d", len(inbounds))
	}
	for _, ib := range inbounds {
		m := ib.(map[string]any)
		if m["type"] != "direct" {
			t.Errorf("inbound %v должен быть direct (сырой TCP-relay), а не %v", m["tag"], m["type"])
		}
		if m["override_address"] != "10.9.0.1" {
			t.Errorf("inbound %v: override_address=%v, ожидали backend Hetzner", m["tag"], m["override_address"])
		}
		if m["network"] != "tcp" {
			t.Errorf("inbound %v: network=%v, ожидали tcp", m["tag"], m["network"])
		}
		// Ключевое: relay не должен разбирать поток.
		if _, ok := m["sniff"]; ok {
			t.Errorf("inbound %v: sniff не должен присутствовать — relay не разбирает поток", m["tag"])
		}
		if _, ok := m["tls"]; ok {
			t.Errorf("inbound %v: на RuVDS не должно быть TLS — Reality завершается только на Hetzner", m["tag"])
		}
	}

	outbounds := got["outbounds"].([]any)
	selectors := map[string][]string{}
	socksPorts := map[string]float64{}
	for _, ob := range outbounds {
		m := ob.(map[string]any)
		switch m["type"] {
		case "selector":
			members := []string{}
			for _, x := range m["outbounds"].([]any) {
				members = append(members, x.(string))
			}
			selectors[m["tag"].(string)] = members
		case "socks":
			socksPorts[m["tag"].(string)] = m["server_port"].(float64)
			if m["server"] != "127.0.0.1" {
				t.Errorf("socks-outbound %v смотрит не на loopback: %v", m["tag"], m["server"])
			}
			if _, ok := m["multiplex"]; ok {
				t.Errorf("socks-outbound %v: multiplex запрещён — не сводим всех в один поток", m["tag"])
			}
		}
	}

	if len(selectors) != 2 {
		t.Fatalf("ожидали 2 независимых selector'а, получили %d: %v", len(selectors), selectors)
	}
	vless := selectors["sel-vless"]
	mtproto := selectors["sel-mtproto"]
	if len(vless) != 3 || len(mtproto) != 3 {
		t.Fatalf("в каждом selector'е должно быть 3 backhaul'а: %v / %v", vless, mtproto)
	}
	if vless[0] != "bh-primary-vless" || vless[2] != "bh-emergency-vless" {
		t.Errorf("порядок приоритета в sel-vless нарушен: %v", vless)
	}
	// Независимость: наборы членов не пересекаются.
	for _, a := range vless {
		for _, b := range mtproto {
			if a == b {
				t.Fatalf("selector'ы делят outbound %q — переключение перестанет быть независимым", a)
			}
		}
	}
	if socksPorts["bh-primary-vless"] == socksPorts["bh-primary-mtproto"] {
		t.Error("primary использует один socks-порт на оба класса")
	}

	route := got["route"].(map[string]any)
	rules := route["rules"].([]any)
	last := rules[len(rules)-1].(map[string]any)
	if last["action"] != "reject" {
		t.Errorf("последнее правило должно быть reject, получили %v", last)
	}
	if _, ok := route["final"]; ok {
		t.Error("route.final не задаём: всё маршрутизируется явно, остальное — reject")
	}

	exp := got["experimental"].(map[string]any)
	api := exp["clash_api"].(map[string]any)
	if api["external_controller"] != "127.0.0.1:19090" {
		t.Errorf("API должен быть на loopback, получили %v", api["external_controller"])
	}
}

// --- решения / гистерезис ---

func healthyState(cfg *Config, active string) ClassState {
	cs := ClassState{Active: active, Tiers: map[string]TierState{}}
	for _, t := range cfg.Tiers {
		cs.Tiers[t.Name] = TierState{Healthy: true, ConsecOK: 10, HealthySince: time.Now().Add(-time.Hour)}
	}
	return cs
}

func TestApplyProbeNeedsConsecutiveFailures(t *testing.T) {
	cfg := testConfig()
	cfg.Monitor.FailThreshold = 3
	now := time.Now()
	ts := TierState{Healthy: true, HealthySince: now.Add(-time.Hour)}

	for i := 1; i <= 2; i++ {
		ts = ApplyProbe(now, cfg.Monitor, ts, ProbeResult{OK: false, Phase: "download", Reason: "timeout"})
		if !ts.Healthy {
			t.Fatalf("после %d провала tier не должен считаться мёртвым", i)
		}
	}
	ts = ApplyProbe(now, cfg.Monitor, ts, ProbeResult{OK: false, Phase: "download", Reason: "timeout"})
	if ts.Healthy {
		t.Fatal("после 3 провалов подряд tier обязан стать мёртвым")
	}
}

func TestApplyProbeNeedsConsecutiveSuccesses(t *testing.T) {
	cfg := testConfig()
	cfg.Monitor.RecoverThreshold = 5
	now := time.Now()
	ts := TierState{Healthy: false}

	for i := 1; i <= 4; i++ {
		ts = ApplyProbe(now, cfg.Monitor, ts, ProbeResult{OK: true, DownBps: 1 << 20})
		if ts.Healthy {
			t.Fatalf("после %d успеха tier не должен считаться живым", i)
		}
	}
	ts = ApplyProbe(now, cfg.Monitor, ts, ProbeResult{OK: true, DownBps: 1 << 20})
	if !ts.Healthy {
		t.Fatal("после 5 успехов подряд tier обязан ожить")
	}
	if ts.HealthySince.IsZero() {
		t.Fatal("HealthySince должен проставиться при переходе в healthy")
	}
}

func TestDecideFailoverIsImmediateWhenActiveDead(t *testing.T) {
	cfg := testConfig()
	now := time.Now()
	cs := healthyState(cfg, TierPrimary)
	cs.LastSwitchAt = now.Add(-time.Second) // hold-down ещё не прошёл
	cs.Tiers[TierPrimary] = TierState{Healthy: false, LastReason: "download: stall"}

	d := Decide(now, cfg, ClassVLESS, cs)
	if !d.Switch || d.To != TierSecondary || d.Kind != "failover" {
		t.Fatalf("ожидали немедленный failover на secondary, получили %+v", d)
	}
}

func TestDecideFailoverCascadesToEmergency(t *testing.T) {
	cfg := testConfig()
	now := time.Now()
	cs := healthyState(cfg, TierSecondary)
	cs.Tiers[TierPrimary] = TierState{Healthy: false}
	cs.Tiers[TierSecondary] = TierState{Healthy: false}

	d := Decide(now, cfg, ClassVLESS, cs)
	if !d.Switch || d.To != TierEmergency {
		t.Fatalf("ожидали каскад на emergency, получили %+v", d)
	}
}

func TestDecideRecoveryHeldByHoldDown(t *testing.T) {
	cfg := testConfig()
	cfg.Monitor.HoldDownSec = 180
	cfg.Monitor.StableBeforeReturnSec = 1
	now := time.Now()
	cs := healthyState(cfg, TierSecondary)
	cs.LastSwitchAt = now.Add(-10 * time.Second)

	d := Decide(now, cfg, ClassVLESS, cs)
	if d.Switch {
		t.Fatalf("возврат должен быть придержан hold-down, получили %+v", d)
	}
	if !d.Held {
		t.Fatalf("ожидали Held=true, получили %+v", d)
	}
}

func TestDecideRecoveryHeldUntilStable(t *testing.T) {
	cfg := testConfig()
	cfg.Monitor.HoldDownSec = 1
	cfg.Monitor.StableBeforeReturnSec = 300
	now := time.Now()
	cs := healthyState(cfg, TierSecondary)
	cs.LastSwitchAt = now.Add(-time.Hour)
	// primary только что ожил
	cs.Tiers[TierPrimary] = TierState{Healthy: true, HealthySince: now.Add(-30 * time.Second)}

	d := Decide(now, cfg, ClassVLESS, cs)
	if d.Switch {
		t.Fatalf("возврат до стабилизации запрещён, получили %+v", d)
	}

	cs.Tiers[TierPrimary] = TierState{Healthy: true, HealthySince: now.Add(-10 * time.Minute)}
	d = Decide(now, cfg, ClassVLESS, cs)
	if !d.Switch || d.To != TierPrimary || d.Kind != "recovery" {
		t.Fatalf("после стабилизации ожидали возврат на primary, получили %+v", d)
	}
}

func TestDecideNoHealthyKeepsCurrent(t *testing.T) {
	cfg := testConfig()
	now := time.Now()
	cs := healthyState(cfg, TierSecondary)
	for name := range cs.Tiers {
		cs.Tiers[name] = TierState{Healthy: false}
	}
	d := Decide(now, cfg, ClassVLESS, cs)
	if d.Switch {
		t.Fatalf("при полном отсутствии здоровых маршрутов переключаться некуда: %+v", d)
	}
	if d.To != TierSecondary {
		t.Fatalf("должны остаться на текущем, получили %+v", d)
	}
}

func TestDecideForcedPinBeatsAutomatics(t *testing.T) {
	cfg := testConfig()
	now := time.Now()
	cs := healthyState(cfg, TierEmergency)
	cs.Forced = TierEmergency
	// primary полностью здоров, но пин обязан победить
	d := Decide(now, cfg, ClassVLESS, cs)
	if d.Switch {
		t.Fatalf("при активном пине автоматика молчит: %+v", d)
	}
	if d.To != TierEmergency {
		t.Fatalf("пин должен удерживать emergency, получили %+v", d)
	}
}

// --- независимость классов на уровне монитора ---

type fakeSwitcher struct {
	sel map[string]string
}

func (f *fakeSwitcher) Selected(_ context.Context, s string) (string, error) { return f.sel[s], nil }
func (f *fakeSwitcher) Select(_ context.Context, s, m string) error          { f.sel[s] = m; return nil }

type scriptedProber struct {
	// fail[class][tier] = true → проверка проваливается
	fail map[ServiceClass]map[string]bool
}

func (p *scriptedProber) Probe(_ context.Context, _ ProbeConfig, t Tier, cls ServiceClass) ProbeResult {
	if p.fail[cls][t.Name] {
		return ProbeResult{Tier: t.Name, Class: cls, OK: false, Phase: "download", Reason: "искусственный обрыв"}
	}
	return ProbeResult{Tier: t.Name, Class: cls, OK: true, DownBps: 1 << 20, UpBps: 1 << 20,
		DownBytes: 128 << 10, UpBytes: 128 << 10}
}

func TestMonitorSwitchesClassesIndependently(t *testing.T) {
	cfg := testConfig()
	cfg.Monitor.FailThreshold = 2
	cfg.Monitor.RecoverThreshold = 1
	cfg.StatePath = t.TempDir() + "/state.json"

	sw := &fakeSwitcher{sel: map[string]string{
		"sel-vless":   "bh-primary-vless",
		"sel-mtproto": "bh-primary-mtproto",
	}}
	// Ломаем primary ТОЛЬКО для vless.
	pr := &scriptedProber{fail: map[ServiceClass]map[string]bool{
		ClassVLESS:   {TierPrimary: true},
		ClassMTProto: {},
	}}
	st := NewState(cfg.ClassesInUse(), cfg.Tiers)
	st.Classes[ClassVLESS] = ClassState{Active: TierPrimary, Tiers: map[string]TierState{
		TierPrimary:   {Healthy: true, HealthySince: time.Now().Add(-time.Hour)},
		TierSecondary: {Healthy: true, HealthySince: time.Now().Add(-time.Hour)},
		TierEmergency: {Healthy: true, HealthySince: time.Now().Add(-time.Hour)},
	}}
	st.Classes[ClassMTProto] = ClassState{Active: TierPrimary, Tiers: map[string]TierState{
		TierPrimary:   {Healthy: true, HealthySince: time.Now().Add(-time.Hour)},
		TierSecondary: {Healthy: true, HealthySince: time.Now().Add(-time.Hour)},
		TierEmergency: {Healthy: true, HealthySince: time.Now().Add(-time.Hour)},
	}}

	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	m := NewMonitorWith(cfg, sw, pr, st, logger)

	ctx := context.Background()
	m.Tick(ctx)
	m.Tick(ctx)

	if got := sw.sel["sel-vless"]; got != "bh-secondary-vless" {
		t.Errorf("vless должен был уйти на secondary, а он на %q", got)
	}
	if got := sw.sel["sel-mtproto"]; got != "bh-primary-mtproto" {
		t.Errorf("mtproto трогать не следовало, а он на %q", got)
	}
}

func TestMonitorDryRunDoesNotSwitch(t *testing.T) {
	cfg := testConfig()
	cfg.Monitor.FailThreshold = 1
	cfg.StatePath = t.TempDir() + "/state.json"

	sw := &fakeSwitcher{sel: map[string]string{
		"sel-vless":   "bh-primary-vless",
		"sel-mtproto": "bh-primary-mtproto",
	}}
	pr := &scriptedProber{fail: map[ServiceClass]map[string]bool{
		ClassVLESS:   {TierPrimary: true},
		ClassMTProto: {TierPrimary: true},
	}}
	st := NewState(cfg.ClassesInUse(), cfg.Tiers)
	for _, cls := range cfg.ClassesInUse() {
		cs := st.Classes[cls]
		cs.Active = TierPrimary
		cs.Tiers[TierPrimary] = TierState{Healthy: true, HealthySince: time.Now().Add(-time.Hour)}
		cs.Tiers[TierSecondary] = TierState{Healthy: true, HealthySince: time.Now().Add(-time.Hour)}
		st.Classes[cls] = cs
	}

	m := NewMonitorWith(cfg, sw, pr, st, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	m.DryRun = true
	m.Tick(context.Background())

	if sw.sel["sel-vless"] != "bh-primary-vless" {
		t.Error("dry-run не должен трогать selector")
	}
}

func TestDisabledTierIsAbsentFromConfigAndRouting(t *testing.T) {
	cfg := testConfig()
	// Израильский резидентный узел ещё не подключён.
	cfg.Tiers[0].Disabled = true

	raw, err := RenderRelayConfig(cfg)
	if err != nil {
		t.Fatalf("RenderRelayConfig: %v", err)
	}
	if string(raw) == "" {
		t.Fatal("пустой конфиг")
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("невалидный JSON: %v", err)
	}
	for _, ob := range got["outbounds"].([]any) {
		m := ob.(map[string]any)
		tag, _ := m["tag"].(string)
		if tag == "bh-primary-vless" || tag == "bh-primary-mtproto" {
			t.Fatalf("отключённый tier не должен попадать в конфиг: %s", tag)
		}
		if m["type"] == "selector" {
			for _, x := range m["outbounds"].([]any) {
				if s := x.(string); s == "bh-primary-vless" || s == "bh-primary-mtproto" {
					t.Fatalf("selector %v ссылается на отключённый tier %s", m["tag"], s)
				}
			}
		}
	}

	// И маршрут обязан уехать с отключённого tier'а немедленно.
	cs := healthyState(cfg, TierPrimary)
	d := Decide(time.Now(), cfg, ClassVLESS, cs)
	if !d.Switch || d.To != TierSecondary {
		t.Fatalf("ожидали немедленный уход с отключённого primary, получили %+v", d)
	}
}

func TestValidateRejectsAllTiersDisabled(t *testing.T) {
	cfg := testConfig()
	for i := range cfg.Tiers {
		cfg.Tiers[i].Disabled = true
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("ожидали отказ: маршрутизировать некуда")
	}
}

func TestSyncReassertsPersistedChoiceAfterRestart(t *testing.T) {
	cfg := testConfig()
	cfg.StatePath = t.TempDir() + "/state.json"

	// sing-box перезапустился и сбросил selector на default (primary),
	// хотя монитор до этого увёл трафик на emergency.
	sw := &fakeSwitcher{sel: map[string]string{
		"sel-vless":   "bh-primary-vless",
		"sel-mtproto": "bh-primary-mtproto",
	}}
	st := NewState(cfg.ClassesInUse(), cfg.Tiers)
	for _, cls := range cfg.ClassesInUse() {
		cs := st.Classes[cls]
		cs.Active = TierEmergency
		st.Classes[cls] = cs
	}

	m := NewMonitorWith(cfg, sw, &scriptedProber{}, st, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	m.Sync(context.Background())

	if sw.sel["sel-vless"] != "bh-emergency-vless" {
		t.Errorf("selector не восстановлен: %q — рестарт вернул бы клиентов на мёртвый primary",
			sw.sel["sel-vless"])
	}
	if sw.sel["sel-mtproto"] != "bh-emergency-mtproto" {
		t.Errorf("mtproto не восстановлен: %q", sw.sel["sel-mtproto"])
	}
}

func TestSyncAdoptsLiveValueOnFirstRun(t *testing.T) {
	cfg := testConfig()
	cfg.StatePath = t.TempDir() + "/state.json"
	sw := &fakeSwitcher{sel: map[string]string{
		"sel-vless":   "bh-secondary-vless",
		"sel-mtproto": "bh-primary-mtproto",
	}}
	st := NewState(cfg.ClassesInUse(), cfg.Tiers) // Active пустой

	m := NewMonitorWith(cfg, sw, &scriptedProber{}, st, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	m.Sync(context.Background())

	if got := m.State().Classes[ClassVLESS].Active; got != TierSecondary {
		t.Errorf("на первом запуске надо принять живое значение, получили %q", got)
	}
	if sw.sel["sel-vless"] != "bh-secondary-vless" {
		t.Error("на первом запуске selector трогать не следовало")
	}
}

func TestRelayConfigHasNoCacheFile(t *testing.T) {
	// cache_file проходит `sing-box check`, но валит `run`, если каталога нет,
	// и заводит второе хранилище выбора маршрута.
	raw, err := RenderRelayConfig(testConfig())
	if err != nil {
		t.Fatalf("RenderRelayConfig: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if _, ok := got["experimental"].(map[string]any)["cache_file"]; ok {
		t.Fatal("cache_file не должен попадать в конфиг relay")
	}
}

func TestStateRoundTrip(t *testing.T) {
	cfg := testConfig()
	path := t.TempDir() + "/state.json"
	st := NewState(cfg.ClassesInUse(), cfg.Tiers)
	cs := st.Classes[ClassVLESS]
	cs.Active = TierSecondary
	cs.Forced = TierEmergency
	st.Classes[ClassVLESS] = cs
	if err := st.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	back, err := LoadState(path, cfg.ClassesInUse(), cfg.Tiers)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if back.Classes[ClassVLESS].Active != TierSecondary || back.Classes[ClassVLESS].Forced != TierEmergency {
		t.Fatalf("состояние не пережило round-trip: %+v", back.Classes[ClassVLESS])
	}
}
