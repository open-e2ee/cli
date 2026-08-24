package credential

import "sync"

type Memory struct {
	mu     sync.Mutex
	values map[string]Credential
}

func NewMemory() *Memory {
	return &Memory{values: make(map[string]Credential)}
}

func (m *Memory) Get(profile string) (Credential, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	value, ok := m.values[profile]
	if !ok {
		return Credential{}, ErrNotFound
	}
	value.Source = "memory"
	return value, nil
}

func (m *Memory) Set(profile string, value Credential) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.values[profile] = value
	return nil
}

func (m *Memory) Delete(profile string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.values, profile)
	return nil
}
