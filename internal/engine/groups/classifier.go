package groups

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"

	"cheburnet/internal/config"
)

var validGroupNameRegex = regexp.MustCompile(`^[a-z0-9_-]{1,32}$`)

type compiledGroup struct {
	name     string
	priority int
	patterns []*regexp.Regexp
}

type PoolClassifier struct {
	mu     sync.RWMutex
	groups []compiledGroup
}

func NewPoolClassifier(groupConfigs []config.NodeFilterGroup) (*PoolClassifier, error) {
	var compiled []compiledGroup

	for _, g := range groupConfigs {
		if !g.Enabled {
			continue
		}

		name := strings.ToLower(strings.TrimSpace(g.Name))
		if !validGroupNameRegex.MatchString(name) {
			return nil, fmt.Errorf("invalid group name '%s': must match ^[a-z0-9_-]{1,32}$", g.Name)
		}

		cg := compiledGroup{
			name:     name,
			priority: g.Priority,
		}

		for _, rawPattern := range g.Regex {
			p := strings.TrimSpace(rawPattern)
			if p == "" {
				continue
			}
			re, err := regexp.Compile(p)
			if err != nil {
				return nil, fmt.Errorf("group '%s' invalid regex '%s': %w", name, p, err)
			}
			cg.patterns = append(cg.patterns, re)
		}

		if len(cg.patterns) > 0 {
			compiled = append(compiled, cg)
		}
	}

	sort.SliceStable(compiled, func(i, j int) bool {
		return compiled[i].priority > compiled[j].priority
	})

	return &PoolClassifier{groups: compiled}, nil
}

func (pc *PoolClassifier) ClassifyNodes(nodes []*config.GenericNode) map[string][]*config.GenericNode {
	pc.mu.RLock()
	defer pc.mu.RUnlock()

	result := make(map[string][]*config.GenericNode)
	for _, g := range pc.groups {
		result[g.name] = make([]*config.GenericNode, 0)
	}
	result["general"] = make([]*config.GenericNode, 0)

	for _, n := range nodes {
		if n == nil {
			continue
		}

		matchedGroup := ""
		for _, g := range pc.groups {
			for _, re := range g.patterns {
				if re.MatchString(n.Tag) {
					matchedGroup = g.name
					break
				}
			}
			if matchedGroup != "" {
				break
			}
		}

		if matchedGroup != "" {
			result[matchedGroup] = append(result[matchedGroup], n)
		} else {
			result["general"] = append(result["general"], n)
		}
	}

	return result
}
