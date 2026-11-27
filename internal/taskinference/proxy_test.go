package taskinference

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sarattha/relayna-cell/pkg/cell"
)

func TestTaskInferenceMediatesOnlyApprovedChat(t *testing.T) {
	var hits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		headers := map[string]string{
			"Authorization": "Bearer fixed-key", "X-Relayna-Worker-Token": "worker-secret",
			"X-Relayna-Task-Id": "task-1", "X-Relayna-Run-Id": "attempt-2", "X-Relayna-Task-Service": "service-a",
			"Cookie": "", "X-Arbitrary": "",
		}
		for key, want := range headers {
			if got := r.Header.Get(key); got != want {
				t.Errorf("upstream header %s = %q, want %q", key, got, want)
			}
		}
		var body struct{ Model string }
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Model != "model-a" || r.URL.Path != chatPath {
			t.Errorf("upstream request sent unexpected model or path: %q %q %v", body.Model, r.URL.Path, err)
		}
		w.Header().Set("Set-Cookie", "private")
		_, _ = w.Write([]byte(`{"id":"reply"}`))
	}))
	defer upstream.Close()
	var decisions []cell.TaskInferenceDecision
	rejectAudit := false
	p := newProxy(upstream.URL+chatPath, "model-a", "service-a", "fixed-key", "worker-secret",
		func(id, kind string, data any) error {
			if rejectAudit {
				return errors.New("journal unavailable")
			}
			if id != "cell-3" || kind != cell.TaskInferenceEventKind {
				t.Errorf("unexpected audit source %q %q", id, kind)
			}
			decisions = append(decisions, data.(cell.TaskInferenceDecision))
			return nil
		})
	defer p.Close()
	for _, tt := range []struct {
		path, body string
		status     int
		decision   cell.TaskInferenceDecision
	}{
		{chatPath + "?x=1", `{"model":"model-a"}`, 403, cell.TaskInferenceDecision{Decision: "deny", Reason: "route_not_allowed"}},
		{chatPath, `{"model":"model-b"}`, 403, cell.TaskInferenceDecision{Decision: "deny", Reason: "model_not_allowed"}},
		{chatPath, `{"model":"model-a","stream":true}`, 403, cell.TaskInferenceDecision{Decision: "deny", Reason: "chat_option_not_allowed"}},
		{chatPath, `{"model":"model-a","tools":[]}`, 403, cell.TaskInferenceDecision{Decision: "deny", Reason: "chat_option_not_allowed"}},
		{chatPath, `{"model":"model-b","model":"model-a","stream":false}`, 200, cell.TaskInferenceDecision{Decision: "result", Status: 200}},
	} {
		request := httptest.NewRequest("POST", "http://task-inference.cell"+tt.path, strings.NewReader(tt.body))
		for _, h := range []string{"Authorization", "X-Relayna-Worker-Token", "X-Relayna-Task-Id", "X-Relayna-Run-Id", "X-Relayna-Task-Service", "Cookie", "X-Arbitrary"} {
			request.Header.Set(h, "untrusted")
		}
		result := httptest.NewRecorder()
		p.Serve(result, request, "cell-3", cell.TaskInference{TaskID: "task-1", AttemptID: "attempt-2"})
		if result.Code != tt.status || decisions[len(decisions)-1] != tt.decision || len(result.Header().Values("Set-Cookie")) != 0 {
			t.Fatalf("unexpected result: %d %v %s", result.Code, decisions, result.Body)
		}
	}
	rejectAudit = true
	r := httptest.NewRecorder()
	p.Serve(r, httptest.NewRequest("POST", chatPath, strings.NewReader(`{"model":"model-a"}`)), "cell-3", cell.TaskInference{})
	if r.Code != 503 || hits.Load() != 1 || decisions[len(decisions)-2].Decision != "allow" {
		t.Fatalf("unexpected audit/forwarding state: %d %d %v", r.Code, hits.Load(), decisions)
	}
}

func TestProtectedConfiguration(t *testing.T) {
	dir := t.TempDir()
	configPath, key, worker := filepath.Join(dir, "config"), filepath.Join(dir, "key"), filepath.Join(dir, "worker")
	write := func(path string, b []byte, mode os.FileMode) {
		t.Helper()
		if err := os.WriteFile(path, b, mode); err != nil {
			t.Fatal(err)
		}
	}
	write(key, []byte("example-key\n"), 0600)
	write(worker, []byte("example-worker"), 0600)
	check := func(endpoint string, allowed bool) {
		t.Helper()
		b, _ := json.Marshal(config{endpoint, "example-model", "example-service", key, worker})
		write(configPath, b, 0600)
		p, err := Open(configPath, nil)
		if (err == nil) != allowed {
			t.Fatalf("unexpected config result: %v", err)
		}
		if p != nil {
			p.Close()
		}
	}
	check("http://127.0.0.1:8888"+chatPath, true)
	check("https://gateway.example"+chatPath, true)
	check("http://gateway.example"+chatPath, false)
	check("https://gateway.example/other", false)
	if err := os.Chmod(key, 0640); err != nil {
		t.Fatal(err)
	}
	check("https://gateway.example"+chatPath, false)
}
