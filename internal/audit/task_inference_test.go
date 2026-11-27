package audit

import (
	"context"
	"testing"

	"github.com/sarattha/relayna-cell/pkg/cell"
)

func TestTaskInferenceSummaryIsBoundedAndTrusted(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	emit := func(id, source string, data any) {
		t.Helper()
		if err := store.Emit(id, "", source, cell.TaskInferenceEventKind, data); err != nil {
			t.Fatal(err)
		}
	}
	emit("cell-a", "workload", cell.TaskInferenceDecision{Decision: "spoofed"})
	emit("cell-b", "network", cell.TaskInferenceDecision{Decision: "other Cell"})
	for range taskInferenceSummaryLimit + 1 {
		emit("cell-a", "network", map[string]any{"decision": "result", "status": 200, "extra": "not exported"})
	}
	summary, err := store.TaskInference(context.Background(), "cell-a")
	if err != nil || !summary.Truncated || len(summary.Events) != taskInferenceSummaryLimit {
		t.Fatalf("unexpected summary %v: %v", summary, err)
	}
	for _, event := range summary.Events {
		if event.Decision != "result" || event.Status != 200 || event.EventID == "" || event.Timestamp.IsZero() {
			t.Fatalf("unexpected summary event %+v", event)
		}
	}
}
