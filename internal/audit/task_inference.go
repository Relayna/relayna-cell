package audit

import (
	"context"
	"encoding/json"

	"github.com/sarattha/relayna-cell/pkg/cell"
)

const taskInferenceSummaryLimit = 64

func (s *Store) TaskInference(ctx context.Context, id string) (cell.TaskInferenceSummary, error) {
	summary := cell.TaskInferenceSummary{Events: []cell.TaskInferenceEvent{}}
	rows, err := s.db.QueryContext(ctx, `SELECT body FROM events
WHERE json_extract(body,'$.cell_id')=? AND json_extract(body,'$.source')='network'
AND json_extract(body,'$.event_kind')=? ORDER BY seq LIMIT ?`, id, cell.TaskInferenceEventKind, taskInferenceSummaryLimit+1)
	if err != nil {
		return summary, err
	}
	defer rows.Close()
	for rows.Next() {
		if len(summary.Events) == taskInferenceSummaryLimit {
			summary.Truncated = true
			break
		}
		var data []byte
		if err = rows.Scan(&data); err != nil {
			return summary, err
		}
		var event struct {
			cell.TaskInferenceEvent
			Data cell.TaskInferenceDecision `json:"data"`
		}
		if err = json.Unmarshal(data, &event); err != nil {
			return summary, err
		}
		event.TaskInferenceDecision = event.Data
		summary.Events = append(summary.Events, event.TaskInferenceEvent)
	}
	return summary, rows.Err()
}
