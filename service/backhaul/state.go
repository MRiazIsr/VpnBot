package backhaul

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// TierState — здоровье одного backhaul'а в разрезе одного класса сервиса.
type TierState struct {
	Healthy    bool `json:"healthy"`
	ConsecOK   int  `json:"consec_ok"`
	ConsecFail int  `json:"consec_fail"`
	// HealthySince — с какого момента tier непрерывно здоров. Нулевое время,
	// если сейчас нездоров. Нужно для «вернуться только после стабилизации».
	HealthySince time.Time `json:"healthy_since,omitempty"`
	LastOK       time.Time `json:"last_ok,omitempty"`
	LastFail     time.Time `json:"last_fail,omitempty"`
	LastReason   string    `json:"last_reason,omitempty"`
	LastDownBps  int64     `json:"last_down_bps,omitempty"`
	LastUpBps    int64     `json:"last_up_bps,omitempty"`
}

// ClassState — состояние одного selector'а.
type ClassState struct {
	// Active — имя tier'а, на который сейчас указывает selector.
	Active string `json:"active"`
	// Forced — ручной пин. Пока не пуст, автопереключение отключено.
	Forced       string               `json:"forced,omitempty"`
	LastSwitchAt time.Time            `json:"last_switch_at,omitempty"`
	Tiers        map[string]TierState `json:"tiers"`
}

// State — persisted-состояние монитора. Переживает рестарт демона и
// перезагрузку хоста: после старта монитор не сбрасывает решение вслепую.
type State struct {
	Version   int                         `json:"version"`
	UpdatedAt time.Time                   `json:"updated_at"`
	Classes   map[ServiceClass]ClassState `json:"classes"`
}

const stateVersion = 1

// NewState — пустое состояние для заданных классов и tier'ов.
func NewState(classes []ServiceClass, tiers []Tier) *State {
	s := &State{Version: stateVersion, Classes: map[ServiceClass]ClassState{}}
	for _, cls := range classes {
		cs := ClassState{Tiers: map[string]TierState{}}
		for _, t := range tiers {
			cs.Tiers[t.Name] = TierState{}
		}
		s.Classes[cls] = cs
	}
	return s
}

// LoadState читает состояние; отсутствующий файл — не ошибка, вернётся пустое.
func LoadState(path string, classes []ServiceClass, tiers []Tier) (*State, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return NewState(classes, tiers), nil
	}
	if err != nil {
		return nil, fmt.Errorf("чтение состояния %s: %w", path, err)
	}
	var s State
	if err := json.Unmarshal(raw, &s); err != nil {
		// Битый файл состояния не должен блокировать failover.
		return NewState(classes, tiers), fmt.Errorf("состояние %s повреждено, начинаем с чистого: %w", path, err)
	}
	if s.Classes == nil {
		s.Classes = map[ServiceClass]ClassState{}
	}
	// Дозаполняем то, чего нет: конфиг мог обрасти новыми tier'ами/классами.
	for _, cls := range classes {
		cs, ok := s.Classes[cls]
		if !ok {
			cs = ClassState{}
		}
		if cs.Tiers == nil {
			cs.Tiers = map[string]TierState{}
		}
		for _, t := range tiers {
			if _, ok := cs.Tiers[t.Name]; !ok {
				cs.Tiers[t.Name] = TierState{}
			}
		}
		s.Classes[cls] = cs
	}
	s.Version = stateVersion
	return &s, nil
}

// Save пишет состояние атомарно (tmp + rename), чтобы падение посреди записи
// не оставило обрезанный JSON.
func (s *State) Save(path string) error {
	s.Version = stateVersion
	s.UpdatedAt = time.Now().UTC()
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o640); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
