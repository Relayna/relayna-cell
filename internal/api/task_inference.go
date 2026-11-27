package api

import (
	"fmt"
	"net/http"

	"github.com/sarattha/relayna-cell/internal/engine"
)

func taskInferenceRoute(mux *http.ServeMux, m *engine.Manager) {
	mux.HandleFunc("GET /v1alpha1/cells/{id}/task-inference", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		c, err := m.Get(id)
		if err != nil || c.Spec.TaskInference == nil {
			failure(w, fmt.Errorf("task inference Cell not found"), http.StatusNotFound)
			return
		}
		summary, err := m.Store.TaskInference(r.Context(), id)
		if err != nil {
			failure(w, fmt.Errorf("could not read task inference audit"), http.StatusServiceUnavailable)
			return
		}
		write(w, summary)
	})
}
