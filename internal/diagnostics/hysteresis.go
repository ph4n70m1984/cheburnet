package diagnostics

import "time"

type HysteresisPolicy struct {
	FailuresToOpen   int
	SuccessesToClose int
}

type StateTracker struct {
	Policy       HysteresisPolicy
	State        ProblemState
	Failures     int
	Successes    int
	FirstFailure time.Time
	LastChange   time.Time
}

func NewStateTracker(policy HysteresisPolicy) *StateTracker {
	return &StateTracker{
		Policy: policy,
		State:  StateClear,
	}
}

func (t *StateTracker) Step(healthy bool) int {
	now := time.Now()
	if !healthy {
		t.Successes = 0
		t.Failures++

		switch t.State {
		case StateClear:
			t.State = StatePending
			t.FirstFailure = now
			if t.Failures >= t.Policy.FailuresToOpen {
				t.State = StateActive
				t.LastChange = now
				return 1
			}
		case StatePending:
			if t.Failures >= t.Policy.FailuresToOpen {
				t.State = StateActive
				t.LastChange = now
				return 1
			}
		case StateRecovering:
			t.State = StateActive
			t.LastChange = now
		case StateActive:
		}
	} else {
		t.Failures = 0
		t.Successes++

		switch t.State {
		case StatePending:
			t.State = StateClear
			t.LastChange = now
		case StateActive:
			t.State = StateRecovering
			if t.Successes >= t.Policy.SuccessesToClose {
				t.State = StateClear
				t.LastChange = now
				return -1
			}
		case StateRecovering:
			if t.Successes >= t.Policy.SuccessesToClose {
				t.State = StateClear
				t.LastChange = now
				return -1
			}
		case StateClear:
		}
	}
	return 0
}
