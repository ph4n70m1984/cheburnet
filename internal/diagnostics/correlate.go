package diagnostics

var parentMap = map[string]string{
	"engine.port_unavailable":       "engine.process_down",
	"dns.listener_down":             "engine.process_down",
	"dns.proxy_unavailable":         "engine.process_down",
	"connectivity.proxy_failed":     "engine.process_down",
	"connectivity.proxy_e2e_failed": "engine.process_down",
	"nodes.no_available":            "engine.process_down",
}

func Correlate(problems map[string]*Problem) []Problem {
	// Создаем независимые копии, чтобы не портить объекты под RLock
	clones := make(map[string]*Problem, len(problems))
	for id, p := range problems {
		if p == nil {
			continue
		}
		cp := *p
		cp.Symptoms = nil
		clones[id] = &cp
	}

	roots := make(map[string]*Problem)
	for id, prob := range clones {
		parentID, hasParent := parentMap[id]
		if hasParent && clones[parentID] != nil {
			parent := clones[parentID]
			parent.Symptoms = append(parent.Symptoms, prob.Message)
		} else {
			roots[id] = prob
		}
	}

	result := make([]Problem, 0, len(roots))
	for _, p := range roots {
		result = append(result, *p)
	}
	return result
}
