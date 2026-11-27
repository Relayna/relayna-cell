package network

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sarattha/relayna-cell/pkg/cell"
)

func TestTaskInferenceCellsHaveOnlyTheSyntheticDestination(t *testing.T) {
	var gotDecision cell.TaskInferenceDecision
	p := New(func(_ string, _ string, data any) error {
		gotDecision, _ = data.(cell.TaskInferenceDecision)
		return nil
	})
	granted := cell.TaskInference{TaskID: "task", AttemptID: "attempt"}
	p.Register("192.0.2.1", Grant{CellID: "cell-a", Context: context.Background(), TaskInference: &granted})
	p.Register("192.0.2.2", Grant{CellID: "cell-b", Context: context.Background(),
		Allow: []cell.Endpoint{{Host: cell.TaskInferenceHost, Port: 80}}})
	calls := 0
	p.TaskInference = func(w http.ResponseWriter, r *http.Request, id string, task cell.TaskInference) {
		calls++
		if id != "cell-a" || task != granted {
			t.Errorf("unexpected trusted context %s %+v", id, task)
		}
		w.WriteHeader(http.StatusOK)
	}

	for _, tt := range []struct {
		method, url, peer string
		want              int
	}{
		{"POST", "http://task-inference.cell/v1/chat/completions", "192.0.2.1:1234", 200},
		{"POST", "http://task-inference.cell/v1/chat/completions", "192.0.2.2:1234", 403},
		{"POST", "http://gateway.example/v1/chat/completions", "192.0.2.1:1234", 403},
		{"CONNECT", "task-inference.cell:80", "192.0.2.1:1234", 403},
	} {
		req := httptest.NewRequest(tt.method, tt.url, nil)
		req.RemoteAddr = tt.peer
		w := httptest.NewRecorder()
		p.ServeHTTP(w, req)
		if w.Code != tt.want {
			t.Errorf("%s %s from %s returned %d, want %d", tt.method, tt.url, tt.peer, w.Code, tt.want)
		}
	}
	if calls != 1 || gotDecision.Decision != "deny" || gotDecision.Reason != "destination_not_allowed" {
		t.Fatalf("unexpected mediation: %d calls, last recorded decision %+v", calls, gotDecision)
	}
}
