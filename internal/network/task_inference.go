package network

import (
	"net/http"

	"github.com/sarattha/relayna-cell/pkg/cell"
)

const taskInferenceAuthority = cell.TaskInferenceHost + ":80"

func (p *Proxy) serveTaskInference(w http.ResponseWriter, r *http.Request, g Grant, authority string) {
	if g.TaskInference == nil || p.TaskInference == nil {
		p.Log(g.CellID, "network.deny", map[string]string{"destination": taskInferenceAuthority, "reason": "task inference is not granted"})
		http.Error(w, "task inference denied", http.StatusForbidden)
		return
	}
	if authority != taskInferenceAuthority || r.Method == http.MethodConnect {
		if err := p.Log(g.CellID, cell.TaskInferenceEventKind, cell.TaskInferenceDecision{
			Decision: "deny", Reason: "destination_not_allowed",
		}); err != nil {
			http.Error(w, "audit unavailable", http.StatusServiceUnavailable)
			return
		}
		http.Error(w, "task inference destination denied", http.StatusForbidden)
		return
	}
	p.TaskInference(w, r, g.CellID, *g.TaskInference)
}
