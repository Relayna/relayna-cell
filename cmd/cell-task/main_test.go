package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sarattha/relayna-cell/pkg/cell"
	"github.com/sarattha/relayna-cell/pkg/client"
)

func TestExecuteTask(t *testing.T) {
	zero, nonzero := 0, 7
	tests := []struct {
		name       string
		frames     []cell.Frame
		cancel     bool
		savedError string
		wantError  string
	}{
		{
			name: "split stdout and separate stderr",
			frames: []cell.Frame{
				{Stream: "stdout", Data: []byte(`{"echo":`)},
				{Stream: "stderr", Data: []byte("private diagnostic")},
				{Stream: "stdout", Data: []byte(`{"q":"hello"}}`)},
				{Stream: "exit", ExitCode: &zero},
			},
		},
		{
			name:      "nonzero exit",
			frames:    []cell.Frame{{Stream: "exit", ExitCode: &nonzero}},
			wantError: "exited with code 7",
		},
		{
			name:      "truncated stream",
			frames:    []cell.Frame{{Stream: "stdout", Data: []byte(`{}`)}},
			wantError: "execution failed",
		},
		{
			name: "invalid result",
			frames: []cell.Frame{
				{Stream: "stdout", Data: []byte(`{} {}`)},
				{Stream: "exit", ExitCode: &zero},
			},
			wantError: "stdout must be one",
		},
		{
			name:      "stdout limit",
			frames:    []cell.Frame{{Stream: "stdout", Data: bytes.Repeat([]byte("x"), maxJSON+1)}},
			wantError: "exceeded output limit",
		},
		{
			name:       "cancellation still deletes",
			cancel:     true,
			savedError: "execution cancelled or output failed: interrupted",
			wantError:  "canceled or timed out",
		},
		{
			name: "runtime removal failure suppresses result",
			frames: []cell.Frame{
				{Stream: "stdout", Data: []byte(`{}`)},
				{Stream: "exit", ExitCode: &zero},
			},
			savedError: "cleanup failed: container still exists",
			wantError:  "cleanup could not be confirmed",
		},
	}
	req := taskRequest{"task:with-delimiter", "attempt/2", json.RawMessage(`{"q":"hello"}`)}
	spec := cell.Spec{Image: "fixture:1", Mode: "session", TimeoutSeconds: 10}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var deleted atomic.Bool
			mux := http.NewServeMux()
			mux.HandleFunc("POST /v1alpha1/cells", func(w http.ResponseWriter, r *http.Request) {
				var got cell.Spec
				if err := json.NewDecoder(r.Body).Decode(&got); err != nil || got.Mode != "session" ||
					got.Image != spec.Image || got.TimeoutSeconds != spec.TimeoutSeconds ||
					len(got.Allow) != 0 || len(got.Command) != 0 || got.Name != taskName(req.TaskID, req.AttemptID) {
					t.Errorf("unexpected create request: %+v, %v", got, err)
				}
				_ = json.NewEncoder(w).Encode(cell.Cell{ID: "cell-1", State: "running"})
			})
			mux.HandleFunc("POST /v1alpha1/cells/cell-1/exec", func(w http.ResponseWriter, r *http.Request) {
				var got cell.ExecRequest
				if err := json.NewDecoder(r.Body).Decode(&got); err != nil || len(got.Command) != 1 ||
					got.Command[0] != "/opt/relayna-cell/task" || !bytes.Equal(got.Stdin, req.Payload) ||
					got.TimeoutSeconds != spec.TimeoutSeconds {
					t.Errorf("unexpected exec request: %+v, %v", got, err)
				}
				if tt.cancel {
					cancel()
					return
				}
				for _, frame := range tt.frames {
					frame.ExecutionID = "exec-1"
					_ = json.NewEncoder(w).Encode(frame)
				}
			})
			mux.HandleFunc("DELETE /v1alpha1/cells/cell-1", func(w http.ResponseWriter, r *http.Request) {
				deleted.Store(true)
				w.WriteHeader(http.StatusNoContent)
			})
			mux.HandleFunc("GET /v1alpha1/cells/cell-1", func(w http.ResponseWriter, r *http.Request) {
				if !deleted.Load() {
					t.Error("cleanup inspection preceded deletion")
				}
				_ = json.NewEncoder(w).Encode(cell.Cell{ID: "cell-1", State: "deleted", Error: tt.savedError})
			})
			srv := httptest.NewServer(mux)
			defer srv.Close()
			c := &client.Client{HTTP: &http.Client{Transport: &http.Transport{
				DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
					return (&net.Dialer{}).DialContext(ctx, network, srv.Listener.Addr().String())
				},
			}}}
			defer c.HTTP.CloseIdleConnections()
			got := executeTask(ctx, c, spec, req)
			if got.CellID != "cell-1" || got.TaskID != req.TaskID || got.AttemptID != req.AttemptID || !deleted.Load() {
				t.Fatalf("missing cleanup or correlation: %+v", got)
			}
			if tt.wantError == "" {
				if got.Status != "completed" || string(got.Result) != `{"echo":{"q":"hello"}}` ||
					got.ExecutionID != "exec-1" || got.Error != "" {
					t.Fatalf("unexpected result: %+v", got)
				}
			} else if got.Status != "failed" || len(got.Result) != 0 || !strings.Contains(got.Error, tt.wantError) {
				t.Fatalf("expected failure containing %q, got %+v", tt.wantError, got)
			}
		})
	}
}

func TestInvalidTaskInvocationProducesNoResult(t *testing.T) {
	args := []string{"--image", "fixture:1", "--socket", filepath.Join(t.TempDir(), "unused.sock")}
	requests := []string{
		`{"task_id":"t","attempt_id":"a","payload":{},"command":["override"]}`,
		`{"task_id":"t","attempt_id":"a","payload":[]}`,
		`{"task_id":"t","attempt_id":"","payload":{}}`,
		`{"task_id":"t","attempt_id":"a","payload":{}} {}`,
		strings.Repeat(" ", maxJSON+1),
	}
	for _, input := range requests {
		var stdout, stderr bytes.Buffer
		code := run(context.Background(), args, strings.NewReader(input), &stdout, &stderr)
		if code != 1 || stdout.Len() != 0 || stderr.Len() == 0 {
			t.Fatalf("invalid input accepted: code=%d stdout=%q stderr=%q", code, &stdout, &stderr)
		}
	}
}

func TestTaskNameMatchesAuditContract(t *testing.T) {
	const want = "sdk-task:8463725ef96a72d6f6ac7609e5370790e2155ded96957c6d30381cbc75eebc6a"
	if got := taskName("task-a", "attempt-1"); got != want {
		t.Fatalf("task name = %q, want %q", got, want)
	}
}
