package main

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sarattha/relayna-cell/pkg/cell"
)

func TestTaskProfileExportsArtifactsAfterConfirmedCleanup(t *testing.T) {
	for _, tc := range []struct {
		name  string
		fault string
	}{
		{name: "success"},
		{name: "missing artifact", fault: "file"},
		{name: "cleanup failure", fault: "cleanup"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			artifacts := filepath.Join(dir, "artifacts")
			if err := os.Mkdir(artifacts, 0700); err != nil {
				t.Fatal(err)
			}
			profiles := filepath.Join(dir, "profiles.json")
			if err := os.WriteFile(profiles, []byte(`{"profiles":{"reports":{"image":"reports:1","cpu":2,"memory_mib":512,"workspace_mib":64,"pids":24,"timeout_seconds":15,"artifacts":["report/output.txt"]}}}`), 0600); err != nil {
				t.Fatal(err)
			}
			socketDir, err := os.MkdirTemp("", "cell-profile-")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(socketDir)
			socket := filepath.Join(socketDir, "api.sock")
			l, err := net.Listen("unix", socket)
			if err != nil {
				t.Fatal(err)
			}
			mux := http.NewServeMux()
			var steps []string
			mux.HandleFunc("POST /v1alpha1/cells", func(w http.ResponseWriter, r *http.Request) {
				steps = append(steps, "create")
				var spec cell.Spec
				err := json.NewDecoder(r.Body).Decode(&spec)
				if err != nil || spec.Image != "reports:1" || spec.CPU != 2 || spec.MemoryMiB != 512 ||
					spec.WorkspaceMiB != 64 || spec.PIDs != 24 || spec.TimeoutSeconds != 15 ||
					spec.TaskProfile != "reports" || len(spec.Allow) != 0 || spec.TaskInference != nil {
					t.Errorf("unexpected profile create: %+v, %v", spec, err)
				}
				_ = json.NewEncoder(w).Encode(cell.Cell{ID: "cell-1"})
			})
			mux.HandleFunc("POST /v1alpha1/cells/cell-1/exec", func(w http.ResponseWriter, _ *http.Request) {
				steps = append(steps, "exec")
				e := json.NewEncoder(w)
				_ = e.Encode(cell.Frame{ExecutionID: "exec-1", Stream: "stdout", Data: []byte(`{}`)})
				zero := 0
				_ = e.Encode(cell.Frame{ExecutionID: "exec-1", Stream: "exit", ExitCode: &zero})
			})
			mux.HandleFunc("GET /v1alpha1/cells/cell-1/files", func(w http.ResponseWriter, r *http.Request) {
				steps = append(steps, "export")
				if r.URL.Query().Get("path") != "report/output.txt" {
					t.Error("export did not use the profile artifact path")
				}
				if tc.fault == "file" {
					w.WriteHeader(422)
					return
				}
				_, _ = io.WriteString(w, "hello")
			})
			mux.HandleFunc("DELETE /v1alpha1/cells/cell-1", func(w http.ResponseWriter, _ *http.Request) {
				steps = append(steps, "delete")
				w.WriteHeader(http.StatusNoContent)
			})
			mux.HandleFunc("GET /v1alpha1/cells/cell-1", func(w http.ResponseWriter, _ *http.Request) {
				state := cell.Cell{ID: "cell-1", State: "deleted"}
				if tc.fault == "cleanup" {
					state.Error = "cleanup failed: original runtime remains"
				}
				_ = json.NewEncoder(w).Encode(state)
			})
			srv := http.Server{Handler: mux, ReadHeaderTimeout: time.Second}
			go func() { _ = srv.Serve(l) }()
			defer srv.Close()

			args := []string{"--profiles", profiles, "--profile", "reports", "--artifact-dir", artifacts, "--socket", socket}
			var stdout, stderr strings.Builder
			code := run(context.Background(), args, strings.NewReader(`{"task_id":"report-1","attempt_id":"try-1","payload":{}}`), &stdout, &stderr)
			var result taskResponse
			if err := json.Unmarshal([]byte(stdout.String()), &result); err != nil {
				t.Fatalf("launcher response: %v, %s", err, stderr.String())
			}
			if strings.Join(steps, ",") != "create,exec,export,delete" || result.Profile != "reports" {
				t.Fatalf("unexpected execution: %v, %+v", steps, result)
			}
			if tc.fault != "" {
				entries, err := os.ReadDir(artifacts)
				if err != nil || len(entries) != 0 || code != 1 || result.ArtifactManifest != "" {
					t.Fatalf("failed task left artifacts: %v, %v, %+v", entries, err, result)
				}
				return
			}
			if code != 0 || result.ArtifactManifest == "" {
				t.Fatalf("artifact not published: %+v", result)
			}
			manifest := filepath.Join(artifacts, result.ArtifactManifest)
			data, err := os.ReadFile(manifest)
			if err != nil || !strings.Contains(string(data), "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824") ||
				!strings.Contains(string(data), `"task_id":"report-1"`) {
				t.Fatalf("unexpected manifest: %s, %v", data, err)
			}
			content, err := os.ReadFile(filepath.Join(filepath.Dir(manifest), "files", "report", "output.txt"))
			if err != nil || string(content) != "hello" {
				t.Fatalf("unexpected artifact: %q, %v", content, err)
			}
		})
	}
}

func TestTaskProfileRejectsUnsafeArtifactPaths(t *testing.T) {
	for _, paths := range [][]string{{"../report"}, {"/report"}, {"."}, {"a", "a/b"}, {"a/b", "a"}, {"a", "a"}, {"a/../b"}} {
		if err := validateArtifactPaths(paths); err == nil {
			t.Fatalf("accepted artifact paths %v", paths)
		}
	}
}
