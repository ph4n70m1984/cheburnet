package config

import (
	"sync"
)

type StateManager struct {
	mu     sync.RWMutex
	config *CheburConfig
}

func NewStateManager(initial *CheburConfig) *StateManager {
	if initial == nil {
		initial = &CheburConfig{
			Engine:     "sing-box",
			TProxyPort: 1602,
			DNSPort:    53,
			MixedPort:  4534,
			AutoHWID:   true,
		}
	}
	return &StateManager{config: initial}
}

func (s *StateManager) Get() *CheburConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.config
}

func (s *StateManager) SetEngine(engineName string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.config.Engine = engineName
}

func (s *StateManager) UpdateNodes(nodes []*GenericNode) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.config.Nodes = nodes
}

func (s *StateManager) SetAutoHWID(enabled bool, custom string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.config.AutoHWID = enabled
	s.config.CustomHWID = custom
}
