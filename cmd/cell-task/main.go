// cell-task runs one SDK task attempt through the trusted Cell operator API.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/sarattha/relayna-cell/pkg/cell"
	"github.com/sarattha/relayna-cell/pkg/client"
)

const (
	maxJSON      = 256 << 10
	maxID        = 256
	taskProgram  = "/opt/relayna-cell/task"
	cleanupLimit = 35 * time.Second
)

type taskRequest struct {
	TaskID    string          `json:"task_id"`
	AttemptID string          `json:"attempt_id"`
	Payload   json.RawMessage `json:"payload"`
}

type taskResponse struct {
	Status           string                     `json:"status"`
	TaskID           string                     `json:"task_id"`
	AttemptID        string                     `json:"attempt_id"`
	CellID           string                     `json:"cell_id,omitempty"`
	ExecutionID      string                     `json:"execution_id,omitempty"`
	Result           json.RawMessage            `json:"result,omitempty"`
	Error            string                     `json:"error,omitempty"`
	Profile          string                     `json:"profile,omitempty"`
	ArtifactManifest string                     `json:"artifact_manifest,omitempty"`
	Inference        *cell.TaskInferenceSummary `json:"inference,omitempty"`
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("cell-task", flag.ContinueOnError)
	flags.SetOutput(stderr)
	image := flags.String("image", "", "operator-configured OCI image reference")
	socket := flags.String("socket", "/run/relayna-cell/api.sock", "local operator Unix socket")
	timeout := flags.Int("timeout", 60, "execution and Cell lifetime in seconds (1–300)")
	inference := flags.Bool("inference", false, "allow the configured task inference route")
	profileFile := flags.String("profiles", "", "absolute path to an owner-only task profile catalog")
	profileName := flags.String("profile", "", "operator-selected task profile name")
	artifactDir := flags.String("artifact-dir", "", "absolute path to a private artifact output directory")
	if err := flags.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 1
	}
	if flags.NArg() != 0 || !filepath.IsAbs(*socket) {
		fmt.Fprintln(stderr, "cell-task needs an absolute socket path and accepts no positional arguments")
		return 1
	}
	spec := taskDefaults(*image, *timeout)
	var artifactPaths []string
	if *profileFile != "" || *profileName != "" {
		profile, err := loadTaskProfile(*profileFile, *profileName)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		spec = profile.spec()
		var conflict bool
		flags.Visit(func(f *flag.Flag) {
			switch f.Name {
			case "image":
				conflict = conflict || *image != spec.Image
			case "timeout":
				conflict = conflict || *timeout != spec.TimeoutSeconds
			case "inference":
				conflict = conflict || *inference != profile.Inference
			}
		})
		if conflict {
			fmt.Fprintln(stderr, "launcher flags disagree with the selected task profile")
			return 1
		}
		*inference = profile.Inference
		artifactPaths = profile.ArtifactPaths
	}
	if spec.TimeoutSeconds < 1 || spec.TimeoutSeconds > 300 {
		fmt.Fprintln(stderr, "task timeout must be 1–300 seconds")
		return 1
	}
	spec.TaskProfile = *profileName
	if err := spec.Validate(); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	export, err := openArtifactExport(*artifactDir, artifactPaths)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if export != nil {
		defer export.root.Close()
	}
	req, err := readRequest(stdin)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if *inference {
		spec.TaskInference = &cell.TaskInference{TaskID: req.TaskID, AttemptID: req.AttemptID}
		if err := spec.Validate(); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	c := client.New(*socket)
	defer c.HTTP.CloseIdleConnections()
	response := executeTask(ctx, c, spec, req, *profileName, export)
	enc := json.NewEncoder(stdout)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(response); err != nil {
		fmt.Fprintln(stderr, "could not write task response")
		return 1
	}
	if response.Status == "completed" {
		return 0
	}
	return 1
}

func readRequest(r io.Reader) (taskRequest, error) {
	var req taskRequest
	data, err := io.ReadAll(io.LimitReader(r, maxJSON+1))
	if err != nil || len(data) > maxJSON {
		return req, fmt.Errorf("cannot read task request within 256 KiB")
	}
	if !isJSONObject(data) {
		return req, fmt.Errorf("stdin must contain one UTF-8 JSON object")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		return req, fmt.Errorf("task request accepts only task_id, attempt_id and payload")
	}
	if len(req.TaskID) == 0 || len(req.TaskID) > maxID || len(req.AttemptID) == 0 || len(req.AttemptID) > maxID {
		return req, fmt.Errorf("task_id and attempt_id must each be strings of 1–256 UTF-8 bytes")
	}
	if !isJSONObject(req.Payload) {
		return req, fmt.Errorf("task payload must be a JSON object")
	}
	return req, nil
}

func isJSONObject(data []byte) bool {
	trimmed := bytes.TrimSpace(data)
	return len(trimmed) > 0 && trimmed[0] == '{' && utf8.Valid(data) && json.Valid(data)
}

// The operator-created Cell name is recorded in trusted cell.requested
// evidence. Length prefixes keep arbitrary opaque IDs unambiguous.
func taskName(taskID, attemptID string) string {
	key := fmt.Sprintf("%d:%s%d:%s", len(taskID), taskID, len(attemptID), attemptID)
	return fmt.Sprintf("sdk-task:%x", sha256.Sum256([]byte(key)))
}

func executeTask(parent context.Context, c *client.Client, spec cell.Spec, req taskRequest, profile string, export *artifactExport) (response taskResponse) {
	ctx, cancel := context.WithTimeout(parent, time.Duration(spec.TimeoutSeconds)*time.Second)
	defer cancel()
	response = taskResponse{Status: "failed", TaskID: req.TaskID, AttemptID: req.AttemptID, Profile: profile}
	var staged *stagedArtifacts
	spec.Name = taskName(req.TaskID, req.AttemptID)
	spec.TaskProfile = profile
	created, err := c.Create(ctx, spec)
	response.CellID = created.ID
	if created.ID != "" {
		defer func() {
			// A canceled execution must still attempt deletion.
			cleanupCtx, stop := context.WithTimeout(context.Background(), cleanupLimit)
			defer stop()
			if err := deleteTaskCell(cleanupCtx, c, created.ID); err != nil {
				response.Status = "failed"
				response.Error = "Cell cleanup could not be confirmed"
				response.Result = nil
			}
			if spec.TaskInference != nil {
				summary, err := taskInferenceSummary(cleanupCtx, c, created.ID)
				if err == nil {
					response.Inference = &summary
				} else if response.Status == "completed" {
					response.Status = "failed"
					response.Error = "Cell task inference audit could not be read"
					response.Result = nil
				}
			}
			if response.Status == "completed" && parent.Err() != nil {
				response.Status = "failed"
				response.Error = "Cell task canceled"
				response.Result = nil
			}
			if staged != nil {
				defer staged.discard()
				if response.Status == "completed" {
					manifest, err := staged.publish()
					if err != nil {
						response.Status = "failed"
						response.Error = "Cell artifact export could not be published"
						response.Result = nil
					} else {
						response.ArtifactManifest = manifest
					}
				}
			}
		}()
	}
	if err != nil || created.ID == "" {
		response.Error = "Cell creation failed"
		if spec.TaskInference != nil {
			response.Error = "Cell task inference session creation failed"
		}
		return
	}

	var output bytes.Buffer
	var exitCode *int
	err = c.Exec(ctx, created.ID, cell.ExecRequest{
		Command: []string{taskProgram}, Stdin: req.Payload, TimeoutSeconds: spec.TimeoutSeconds,
	}, func(frame cell.Frame) error {
		if exitCode != nil || frame.ExecutionID == "" ||
			(response.ExecutionID != "" && response.ExecutionID != frame.ExecutionID) {
			return fmt.Errorf("invalid Cell execution stream")
		}
		response.ExecutionID = frame.ExecutionID
		switch frame.Stream {
		case "stdout":
			if frame.ExitCode != nil || len(frame.Data) > maxJSON-output.Len() {
				return fmt.Errorf("invalid or oversized task stdout")
			}
			output.Write(frame.Data)
		case "stderr":
			if frame.ExitCode != nil {
				return fmt.Errorf("invalid Cell stderr frame")
			}
		case "exit":
			if frame.ExitCode == nil || len(frame.Data) != 0 {
				return fmt.Errorf("invalid Cell exit frame")
			}
			exitCode = frame.ExitCode
		default:
			return fmt.Errorf("unexpected Cell execution frame")
		}
		return nil
	})
	switch {
	case ctx.Err() != nil:
		response.Error = "Cell task canceled or timed out"
	case err != nil || exitCode == nil:
		response.Error = "Cell task execution failed or exceeded output limit"
	case *exitCode != 0:
		response.Error = fmt.Sprintf("Cell task exited with code %d", *exitCode)
	case !isJSONObject(output.Bytes()):
		response.Error = "Cell task stdout must be one UTF-8 JSON object"
	default:
		if export != nil {
			staged, err = export.collect(ctx, c, response)
			if err != nil {
				response.Error = "Cell required artifact export failed"
				return
			}
		}
		response.Status = "completed"
		response.Result = output.Bytes()
	}
	return
}

func taskInferenceSummary(ctx context.Context, c *client.Client, id string) (cell.TaskInferenceSummary, error) {
	var summary cell.TaskInferenceSummary
	response, err := c.Request(ctx, http.MethodGet, "/v1alpha1/cells/"+url.PathEscape(id)+"/task-inference", nil)
	if err != nil {
		return summary, err
	}
	defer response.Body.Close()
	err = json.NewDecoder(response.Body).Decode(&summary)
	return summary, err
}

func deleteTaskCell(ctx context.Context, c *client.Client, id string) error {
	path := "/v1alpha1/cells/" + url.PathEscape(id)
	response, err := c.Request(ctx, http.MethodDelete, path, nil)
	if err != nil {
		return err
	}
	response.Body.Close()

	// The delete endpoint waits for teardown and preserves the Cell's error.
	// The engine records failed runtime removal with this specific prefix.
	response, err = c.Request(ctx, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	var saved cell.Cell
	if err := json.NewDecoder(response.Body).Decode(&saved); err != nil {
		return err
	}
	if saved.ID != id || saved.State != "deleted" || strings.HasPrefix(saved.Error, "cleanup failed:") {
		return fmt.Errorf("Cell did not reach a clean deleted state")
	}
	return nil
}
