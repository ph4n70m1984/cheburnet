package adaptive

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"cheburnet/internal/config"
	"cheburnet/internal/engine/groups"
)

type StateController struct {
	state          *config.StateManager
	prober         *Prober
	classifier     *groups.PoolClassifier
	activeGroup    string
	lastActiveNode string

	classifiedPool map[string][]*config.GenericNode

	groupSwitchMu sync.Mutex
	mu            sync.RWMutex
}

func NewStateController(state *config.StateManager, prober *Prober) *StateController {
	initSentinelMetrics()

	cfg := state.Get()
	if prober != nil {
		prober.SetSecret(cfg.ClashAPISecret)
	}

	classifier, err := groups.NewPoolClassifier(cfg.NodeGroups)
	if err != nil {
		log.Printf("[controller] WARN: Defaulting classifier: %v", err)
		classifier, _ = groups.NewPoolClassifier(nil)
	}

	initialPool := classifier.ClassifyNodes(cfg.Nodes)

	activeGrp := strings.ToLower(strings.TrimSpace(cfg.ActiveGroup))
	if activeGrp == "" || activeGrp == "auto" {
		activeGrp = "general"
	}

	c := &StateController{
		state:          state,
		prober:         prober,
		classifier:     classifier,
		activeGroup:    activeGrp,
		classifiedPool: initialPool,
	}

	sentinelStateGauge.WithLabelValues(activeGrp).Set(1)
	return c
}

func (c *StateController) UpdatePool(cfg *config.CheburConfig) {
	c.groupSwitchMu.Lock()
	defer c.groupSwitchMu.Unlock()

	if c.prober != nil {
		c.prober.SetSecret(cfg.ClashAPISecret)
	}

	classifier, err := groups.NewPoolClassifier(cfg.NodeGroups)
	if err != nil {
		log.Printf("[controller] ERROR: Updating node groups failed: %v", err)
		return
	}

	pool := classifier.ClassifyNodes(cfg.Nodes)

	c.mu.Lock()
	c.classifier = classifier
	c.classifiedPool = pool

	if len(c.classifiedPool[c.activeGroup]) == 0 {
		log.Printf("[controller] WARN: Active group '%s' is empty, switching to 'general'", c.activeGroup)
		c.activeGroup = "general"
		if len(c.classifiedPool["general"]) == 0 && len(cfg.Nodes) > 0 {
			c.classifiedPool["general"] = cfg.Nodes
		}
	}
	c.mu.Unlock()

	var summary []string
	for grpName, list := range pool {
		summary = append(summary, fmt.Sprintf("%s=%d", grpName, len(list)))
	}
	sort.Strings(summary)
	log.Printf("[controller] Pool synchronized: %s", strings.Join(summary, ", "))
}

func (c *StateController) GetActiveGroupNodes() (string, []*config.GenericNode) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	grp := c.activeGroup
	nodes := c.classifiedPool[grp]

	if len(nodes) == 0 {
		nodes = c.classifiedPool["general"]
		if len(nodes) == 0 {
			cfg := c.state.Get()
			nodes = cfg.Nodes
		}
	}

	out := make([]*config.GenericNode, len(nodes))
	copy(out, nodes)
	return grp, out
}

func (c *StateController) SwitchGroup(ctx context.Context, targetGroup, reason string) error {
	c.groupSwitchMu.Lock()
	defer c.groupSwitchMu.Unlock()

	if c.prober == nil {
		return ErrClashAPINotConfigured
	}

	c.mu.RLock()
	current := c.activeGroup
	targetNodes := c.classifiedPool[targetGroup]
	c.mu.RUnlock()

	if current == targetGroup {
		return nil
	}

	if len(targetNodes) == 0 {
		return fmt.Errorf("cannot switch to empty group '%s'", targetGroup)
	}

	bestNodeTag := targetNodes[0].Tag
	if cachedBest := c.prober.getCachedNode(targetNodes); cachedBest != nil {
		bestNodeTag = cachedBest.Tag
	}

	switchCtx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer cancel()

	if err := c.prober.SwitchOutbound(switchCtx, config.MainSelectorTag, bestNodeTag); err != nil {
		return fmt.Errorf("switch outbound failed: %w", err)
	}

	c.mu.Lock()
	c.activeGroup = targetGroup
	c.lastActiveNode = bestNodeTag
	c.mu.Unlock()

	sentinelStateGauge.WithLabelValues(current).Set(0)
	sentinelStateGauge.WithLabelValues(targetGroup).Set(1)
	sentinelSwitchesTotal.WithLabelValues(current, targetGroup, reason).Inc()
	sentinelLastSwitchTimestamp.WithLabelValues(current, targetGroup).Set(float64(time.Now().Unix()))

	log.Printf("[controller] GROUP SWITCH [%s -> %s] reason=%s active_node='%s'",
		current, targetGroup, reason, bestNodeTag)

	return nil
}

func (c *StateController) Reassert(ctx context.Context) {
	if c.prober == nil {
		return
	}

	c.groupSwitchMu.Lock()
	defer c.groupSwitchMu.Unlock()

	c.mu.RLock()
	nodeTag := c.lastActiveNode
	grp := c.activeGroup
	nodes := c.classifiedPool[grp]
	c.mu.RUnlock()

	if nodeTag == "" && len(nodes) > 0 {
		nodeTag = nodes[0].Tag
	}
	if nodeTag == "" {
		return
	}

	reCtx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer cancel()

	if err := c.prober.SwitchOutbound(reCtx, config.MainSelectorTag, nodeTag); err != nil {
		log.Printf("[controller] WARN: Reassert selector '%s' to '%s' failed: %v, trying fallbacks...",
			config.MainSelectorTag, nodeTag, err)
		for _, fallbackTag := range []string{"proxy", "main-out", "auto"} {
			if fErr := c.prober.SwitchOutbound(reCtx, fallbackTag, nodeTag); fErr == nil {
				log.Printf("[controller] Reassert fallback to selector '%s' succeeded", fallbackTag)
				return
			}
		}
	} else {
		log.Printf("[controller] Reasserted selector '%s' to active node '%s'",
			config.MainSelectorTag, nodeTag)
	}
}
