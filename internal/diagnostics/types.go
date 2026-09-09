package diagnostics

import "time"

type Severity string

const (
	SeverityCritical Severity = "critical"
	SeverityError    Severity = "error"
	SeverityWarning  Severity = "warning"
)

// Состояния трекера гистерезиса
type ProblemState string

const (
	StateClear      ProblemState = "clear"
	StatePending    ProblemState = "pending"
	StateActive     ProblemState = "active"
	StateRecovering ProblemState = "recovering"
)

type CheckResult struct {
	CheckID   string
	Component string
	Healthy   bool
	Severity  Severity
	Message   string
	Action    string
	Details   map[string]interface{}
}

type Problem struct {
	ID          string                 `json:"id"`
	Component   string                 `json:"component"`
	Severity    Severity               `json:"severity"`
	Message     string                 `json:"message"`
	FirstSeen   time.Time              `json:"first_seen"`
	LastSeen    time.Time              `json:"last_seen"`
	Occurrences int                    `json:"occurrences"`
	Details     map[string]interface{} `json:"details,omitempty"`
	Symptoms    []string               `json:"symptoms,omitempty"`
	Recoverable bool                   `json:"recoverable"`
	Action      string                 `json:"action,omitempty"`
}

type DiagnosticSnapshot struct {
	Ready          bool       `json:"ready"`
	Timestamp      time.Time  `json:"timestamp"`
	Healthy        bool       `json:"healthy"`
	TotalChecks    int        `json:"total_checks"`
	Critical       int        `json:"critical"`
	Errors         int        `json:"errors"`
	Warnings       int        `json:"warnings"`
	ActiveProblems []*Problem `json:"problems"`
}

type DiagnosticEvent struct {
	Type      string              `json:"type"`
	Snapshot  *DiagnosticSnapshot `json:"snapshot,omitempty"`
	Problem   *Problem            `json:"problem,omitempty"`
	ProblemID string              `json:"problem_id,omitempty"`
}
