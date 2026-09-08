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
	return &StateManager{
		config: cloneConfig(initial),
	}
}

// Get возвращает глубокую изолированную копию конфигурации (Snapshot)
func (s *StateManager) Get() CheburConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return *cloneConfig(s.config)
}

// Snapshot является алиасом Get для явного отражения семантики снапшота
func (s *StateManager) Snapshot() CheburConfig {
	return s.Get()
}

// Update выполняет атомарную мутацию состояния через замыкание под эксклюзивным Lock
func (s *StateManager) Update(fn func(cfg *CheburConfig)) CheburConfig {
	s.mu.Lock()
	defer s.mu.Unlock()

	cloned := cloneConfig(s.config)
	fn(cloned)
	s.config = cloned

	return *cloneConfig(s.config)
}

func (s *StateManager) SetEngine(engineName string) {
	s.Update(func(cfg *CheburConfig) {
		cfg.Engine = engineName
	})
}

func (s *StateManager) UpdateNodes(nodes []*GenericNode) {
	s.Update(func(cfg *CheburConfig) {
		cfg.Nodes = cloneNodes(nodes)
	})
}

func (s *StateManager) SetAutoHWID(enabled bool, custom string) {
	s.Update(func(cfg *CheburConfig) {
		cfg.AutoHWID = enabled
		cfg.CustomHWID = custom
	})
}

// cloneConfig выполняет полное глубокое копирование структуры и её вложенных ссылок
func cloneConfig(src *CheburConfig) *CheburConfig {
	if src == nil {
		return nil
	}

	dst := *src

	// 1. Копирование среза указателей на GenericNode с созданием новых структур
	dst.Nodes = cloneNodes(src.Nodes)

	// 2. Копирование среза указателей на BalancingGroup
	if src.Groups != nil {
		dst.Groups = make([]*BalancingGroup, len(src.Groups))
		for i, g := range src.Groups {
			if g != nil {
				gCopy := *g
				if g.Nodes != nil {
					gCopy.Nodes = append([]string(nil), g.Nodes...)
				}
				dst.Groups[i] = &gCopy
			}
		}
	}

	// 3. Копирование срезов структур по значению
	if src.Subscriptions != nil {
		dst.Subscriptions = append([]SubscriptionConfig(nil), src.Subscriptions...)
	}

	if src.ClientPolicies != nil {
		dst.ClientPolicies = append([]ClientPolicy(nil), src.ClientPolicies...)
	}

	// 4. Копирование срезов строк
	if src.ManualNodes != nil {
		dst.ManualNodes = append([]string(nil), src.ManualNodes...)
	}
	if src.RuleSets != nil {
		dst.RuleSets = append([]string(nil), src.RuleSets...)
	}
	if src.CustomDomains != nil {
		dst.CustomDomains = append([]string(nil), src.CustomDomains...)
	}
	if src.CustomSubnets != nil {
		dst.CustomSubnets = append([]string(nil), src.CustomSubnets...)
	}
	if src.LocalListFiles != nil {
		dst.LocalListFiles = append([]string(nil), src.LocalListFiles...)
	}

	return &dst
}

func cloneNodes(src []*GenericNode) []*GenericNode {
	if src == nil {
		return nil
	}
	dst := make([]*GenericNode, len(src))
	for i, node := range src {
		if node != nil {
			nodeCopy := *node
			dst[i] = &nodeCopy
		}
	}
	return dst
}
