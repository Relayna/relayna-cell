package cell

import "time"

const TaskInferenceHost = "task-inference.cell"

// TaskInference identifies an operator-authorized SDK task attempt.
type TaskInference struct {
	TaskID    string `json:"task_id"`
	AttemptID string `json:"attempt_id"`
}

// ValidTaskInferenceID matches the Gateway task route's literal ID syntax.
func ValidTaskInferenceID(id string) bool {
	if len(id) == 0 || len(id) > 256 {
		return false
	}
	for i, c := range []byte(id) {
		if c >= '0' && c <= '9' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' {
			continue
		}
		if i == 0 || c != '.' && c != '_' && c != '~' && c != '-' {
			return false
		}
	}
	return true
}

const TaskInferenceEventKind = "task.inference"

type TaskInferenceDecision struct {
	Decision string `json:"decision"`
	Status   int    `json:"status,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

type TaskInferenceEvent struct {
	EventID   string    `json:"event_id"`
	Timestamp time.Time `json:"timestamp"`
	TaskInferenceDecision
}

type TaskInferenceSummary struct {
	Events    []TaskInferenceEvent `json:"events"`
	Truncated bool                 `json:"truncated"`
}
