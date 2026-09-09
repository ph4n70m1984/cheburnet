package diagnostics

type HysteresisPolicy struct {
	FailuresToOpen   int
	SuccessesToClose int
}

type StateTracker struct {
	State            ProblemState
	Policy           HysteresisPolicy
	consecutiveFails int
	consecutiveOKs   int
}

func NewStateTracker(policy HysteresisPolicy) *StateTracker {
	if policy.FailuresToOpen <= 0 {
		policy.FailuresToOpen = 1
	}
	if policy.SuccessesToClose <= 0 {
		policy.SuccessesToClose = 1
	}
	return &StateTracker{
		State:  StateClear,
		Policy: policy,
	}
}

// Step возвращает 1 при переходе в StateActive, -1 при возврате в StateClear, иначе 0
func (t *StateTracker) Step(healthy bool) int {
	if !healthy {
		t.consecutiveOKs = 0
		t.consecutiveFails++

		switch t.State {
		case StateClear:
			if t.consecutiveFails >= t.Policy.FailuresToOpen {
				t.State = StateActive
				return 1
			}
			t.State = StatePending

		case StatePending:
			if t.consecutiveFails >= t.Policy.FailuresToOpen {
				t.State = StateActive
				return 1
			}

		case StateRecovering:
			t.State = StateActive
		}
		return 0
	}

	// healthy == true
	t.consecutiveFails = 0
	t.consecutiveOKs++

	switch t.State {
	case StatePending:
		t.State = StateClear

	case StateActive:
		if t.consecutiveOKs >= t.Policy.SuccessesToClose {
			t.State = StateClear
			return -1
		}
		t.State = StateRecovering

	case StateRecovering:
		if t.consecutiveOKs >= t.Policy.SuccessesToClose {
			t.State = StateClear
			return -1
		}
	}

	return 0
}
