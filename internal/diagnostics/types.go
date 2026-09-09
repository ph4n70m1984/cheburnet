package diagnostics

import (
	"time"

	"cheburnet/internal/engine"
)

type Severity string

const (
	SeverityCritical Severity = "critical"
	SeverityError    Severity = "error"
	SeverityWarning  Severity = "warning"
)

type ProblemState string

const (
	StateClear      ProblemState = "clear"
	StatePending    ProblemState = "pending"
	StateActive     ProblemState = "active"
	StateRecovering ProblemState = "recovering"
)

type Problem struct {
	ID          string         `json:"id"`
	Component   string         `json:"component"`
	Severity    Severity       `json:"severity"`
	Message     string         `json:"message"`
	FirstSeen   time.Time      `json:"first_seen"`
	LastSeen    time.Time      `json:"last_seen"`
	Occurrences uint32         `json:"occurrences"`
	Details     map[string]any `json:"details,omitempty"`
	Recoverable bool           `json:"recoverable"`
	Action      string         `json:"action,omitempty"`
	ParentID    string         `json:"parent_id,omitempty"`
	Symptoms    []string       `json:"symptoms,omitempty"`
}

type CheckResult struct {
	CheckID   string
	Component string
	Healthy   bool
	Severity  Severity
	Message   string
	Details   map[string]any
	Action    string
}

type DiagnosticSnapshot struct {
	Timestamp      time.Time `json:"timestamp"`
	Healthy        bool      `json:"healthy"`
	TotalChecks    int       `json:"total_checks"`
	Critical       int       `json:"critical"`
	Errors         int       `json:"errors"`
	Warnings       int       `json:"warnings"`
	ActiveProblems []Problem `json:"problems"`
}

type DiagnosticEvent struct {
	Type      string              `json:"type"`
	Problem   *Problem            `json:"problem,omitempty"`
	ProblemID string              `json:"problem_id,omitempty"`
	Snapshot  *DiagnosticSnapshot `json:"snapshot,omitempty"`
}

type HealthSnapshot = engine.HealthSnapshot
