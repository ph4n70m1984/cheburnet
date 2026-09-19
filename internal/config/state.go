package config

import (
	"fmt"
	"sync"
)

type StateManager struct {
	mu      sync.RWMutex
	config  *CheburConfig
	storage *UCIStorage
}

func NewStateManager(initial *CheburConfig, storage *UCIStorage) *StateManager {
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
		config:  cloneConfig(initial),
		storage: storage,
	}
}

// Get возвращает глубокую изолированную копию конфигурации (Snapshot)[cite: 12]
func (s *StateManager) Get() CheburConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return *cloneConfig(s.config)
}

// Clone возвращает указатель на глубокую изолированную копию конфигурации для подготовки кандидата[cite: 12]
func (s *StateManager) Clone() *CheburConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneConfig(s.config)
}

// Snapshot является алиасом Get для явного отражения семантики снапшота[cite: 12]
func (s *StateManager) Snapshot() CheburConfig {
	return s.Get()
}

// Commit атомарно фиксирует новую конфигурацию и опционально синхронизирует декларативные параметры в UCI[cite: 12]
func (s *StateManager) Commit(validated *CheburConfig, persistUCI bool) (CheburConfig, error) {
	if validated == nil {
		return s.Get(), nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// 1. Если требуется персистентность (изменение портов, режимов и флагов из API)[cite: 12]
	if persistUCI && s.storage != nil {
		if err := s.storage.SaveCoreSettings(validated); err != nil {
			return *cloneConfig(s.config), fmt.Errorf("failed to persist state to UCI: %w", err)
		}
	}

	// 2. Атомарное обновление оперативной памяти демона[cite: 12]
	s.config = cloneConfig(validated)
	return *cloneConfig(s.config), nil
}

// Update выполняет атомарную мутацию состояния через замыкание под эксклюзивным Lock[cite: 12]
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

	// 1. Копирование среза указателей на GenericNode с созданием новых структур[cite: 12]
	dst.Nodes = cloneNodes(src.Nodes)

	// 2. Копирование среза указателей на BalancingGroup[cite: 12]
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

	// 3. Копирование срезов структур конфигурации[cite: 12]
	if src.Subscriptions != nil {
		dst.Subscriptions = append([]SubscriptionConfig(nil), src.Subscriptions...)
	}

	if src.ClientPolicies != nil {
		dst.ClientPolicies = append([]ClientPolicy(nil), src.ClientPolicies...)
	}

	// Глубокое копирование RoutePolicies и вложенных в них срезов
	if src.RoutePolicies != nil {
		dst.RoutePolicies = make([]RoutePolicy, len(src.RoutePolicies))
		for i, rp := range src.RoutePolicies {
			rpCopy := rp
			if rp.RuleSets != nil {
				rpCopy.RuleSets = append([]string(nil), rp.RuleSets...)
			}
			if rp.Domains != nil {
				rpCopy.Domains = append([]string(nil), rp.Domains...)
			}
			if rp.Subnets != nil {
				rpCopy.Subnets = append([]string(nil), rp.Subnets...)
			}
			dst.RoutePolicies[i] = rpCopy
		}
	}

	// Копирование CustomSRSRulesets
	if src.CustomSRSRulesets != nil {
		dst.CustomSRSRulesets = append([]CustomSRSRule(nil), src.CustomSRSRulesets...)
	}

	// 4. Копирование срезов строк[cite: 12]
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
	if src.CustomPorts != nil {
		dst.CustomPorts = append([]string(nil), src.CustomPorts...)
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
