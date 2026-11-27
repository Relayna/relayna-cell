package taskinference

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/sarattha/relayna-cell/pkg/cell"
)

const maxRequest = 256 << 10
const maxResponse = 1 << 20

type Proxy struct {
	gateway, model, service, key, worker string
	transport                            *http.Transport
	log                                  func(string, string, any) error
}

func newProxy(gateway, model, service, key, worker string, log func(string, string, any) error) *Proxy {
	return &Proxy{gateway: gateway, model: model, service: service, key: key, worker: worker, log: log,
		transport: &http.Transport{
			DialContext:            (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
			TLSHandshakeTimeout:    5 * time.Second,
			ResponseHeaderTimeout:  30 * time.Second,
			IdleConnTimeout:        30 * time.Second,
			MaxResponseHeaderBytes: 16 << 10,
		},
	}
}

func (p *Proxy) Close() { p.transport.CloseIdleConnections() }

func (p *Proxy) Serve(w http.ResponseWriter, r *http.Request, id string, task cell.TaskInference) {
	record := func(decision string, status int, reason string) bool {
		if err := p.log(id, cell.TaskInferenceEventKind, cell.TaskInferenceDecision{
			Decision: decision, Reason: reason, Status: status,
		}); err != nil {
			http.Error(w, "task inference audit unavailable", http.StatusServiceUnavailable)
			return false
		}
		return true
	}
	fail := func(decision, reason string, status int) {
		if record(decision, 0, reason) {
			http.Error(w, reason, status)
		}
	}
	if r.Method != http.MethodPost || r.URL.EscapedPath() != chatPath || r.URL.RawQuery != "" || r.URL.ForceQuery {
		fail("deny", "route_not_allowed", http.StatusForbidden)
		return
	}
	input, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRequest))
	if err != nil {
		fail("deny", "request_size_or_read_error", http.StatusBadRequest)
		return
	}
	var chat map[string]json.RawMessage
	if json.Unmarshal(input, &chat) != nil || chat == nil {
		fail("deny", "invalid_json", http.StatusBadRequest)
		return
	}
	var model string
	if json.Unmarshal(chat["model"], &model) != nil || model != p.model {
		fail("deny", "model_not_allowed", http.StatusForbidden)
		return
	}
	for field, value := range chat {
		switch field {
		case "model", "messages", "temperature", "top_p", "max_tokens", "max_completion_tokens", "seed", "stop":
		case "stream":
			if bytes.Equal(value, []byte("false")) {
				continue
			}
			fallthrough
		default:
			fail("deny", "chat_option_not_allowed", http.StatusForbidden)
			return
		}
	}
	// Re-encode validated fields so the upstream sees the same model even
	// when the input object contained duplicate members.
	body, err := json.Marshal(chat)
	if err != nil {
		fail("deny", "invalid_json", http.StatusBadRequest)
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, p.gateway, bytes.NewReader(body))
	if err != nil {
		fail("error", "upstream_request_failed", http.StatusBadGateway)
		return
	}
	// Only construct headers from trusted configuration and the registered
	// Cell grant. Workload-supplied headers never go to Gateway.
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.key)
	req.Header.Set("X-Relayna-Worker-Token", p.worker)
	req.Header.Set("X-Relayna-Task-Id", task.TaskID)
	req.Header.Set("X-Relayna-Run-Id", task.AttemptID)
	req.Header.Set("X-Relayna-Task-Service", p.service)
	if !record("allow", 0, "") {
		return
	}
	resp, err := p.transport.RoundTrip(req)
	if err != nil {
		fail("error", "upstream_unavailable", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		if record("result", resp.StatusCode, "upstream_rejected") {
			status := resp.StatusCode
			if status < 400 || status > 599 {
				status = http.StatusBadGateway
			}
			http.Error(w, "task inference upstream rejected the request", status)
		}
		return
	}
	result, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse+1))
	if err != nil || len(result) > maxResponse || !json.Valid(result) {
		fail("error", "invalid_upstream_response", http.StatusBadGateway)
		return
	}
	if record("result", resp.StatusCode, "") {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(result)
	}
}
