package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/sarattha/relayna-cell/pkg/cell"
	"golang.org/x/sys/unix"
)

type taskProfile struct {
	Image          string   `json:"image"`
	CPU            float64  `json:"cpu"`
	MemoryMiB      int64    `json:"memory_mib"`
	WorkspaceMiB   int64    `json:"workspace_mib"`
	PIDs           int64    `json:"pids"`
	TimeoutSeconds int      `json:"timeout_seconds"`
	Inference      bool     `json:"inference"`
	ArtifactPaths  []string `json:"artifacts,omitempty"`
}

func taskDefaults(image string, timeout int) cell.Spec {
	return cell.Spec{Image: image, Mode: "session", CPU: 1, MemoryMiB: 256,
		WorkspaceMiB: 16, PIDs: 32, TimeoutSeconds: timeout}
}

func (p taskProfile) spec() cell.Spec {
	s := taskDefaults(p.Image, 60)
	if p.CPU != 0 {
		s.CPU = p.CPU
	}
	if p.MemoryMiB != 0 {
		s.MemoryMiB = p.MemoryMiB
	}
	if p.WorkspaceMiB != 0 {
		s.WorkspaceMiB = p.WorkspaceMiB
	}
	if p.PIDs != 0 {
		s.PIDs = p.PIDs
	}
	if p.TimeoutSeconds != 0 {
		s.TimeoutSeconds = p.TimeoutSeconds
	}
	return s
}

func loadTaskProfile(filename, name string) (taskProfile, error) {
	if !filepath.IsAbs(filename) || name == "" {
		return taskProfile{}, fmt.Errorf("--profiles requires an absolute file and --profile requires a name")
	}
	fd, err := unix.Open(filename, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return taskProfile{}, fmt.Errorf("could not open profile file: %w", err)
	}
	f := os.NewFile(uintptr(fd), filename)
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return taskProfile{}, err
	}
	owner, ok := st.Sys().(*syscall.Stat_t)
	if !ok || owner.Uid != uint32(os.Geteuid()) || !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 {
		return taskProfile{}, fmt.Errorf("profile file must be an owner-only regular file owned by the launcher user")
	}
	const maxProfile = 64 << 10
	data, err := io.ReadAll(io.LimitReader(f, maxProfile+1))
	if err != nil || len(data) > maxProfile || !isJSONObject(data) {
		return taskProfile{}, fmt.Errorf("profile file must contain one JSON object of at most 64 KiB")
	}
	var catalog struct {
		Profiles map[string]taskProfile `json:"profiles"`
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&catalog); err != nil {
		return taskProfile{}, fmt.Errorf("invalid profile configuration: %w", err)
	}
	profile, ok := catalog.Profiles[name]
	if !ok {
		return taskProfile{}, fmt.Errorf("unknown task profile %q", name)
	}
	if err := validateArtifactPaths(profile.ArtifactPaths); err != nil {
		return taskProfile{}, err
	}
	return profile, nil
}

func validateArtifactPaths(paths []string) error {
	if len(paths) > 8 {
		return fmt.Errorf("task profiles allow at most eight artifact files")
	}
	seen := make(map[string]bool)
	for _, p := range paths {
		if !filepath.IsLocal(p) || p == "." || path.Clean(p) != p || len(p) > 256 || strings.ContainsAny(p, "\\\x00\r\n") || seen[p] {
			return fmt.Errorf("artifact paths must be distinct, clean workspace-relative file paths")
		}
		for parent := path.Dir(p); parent != "."; parent = path.Dir(parent) {
			if seen[parent] {
				return fmt.Errorf("an artifact cannot be the parent of another artifact")
			}
		}
		for other := range seen {
			if strings.HasPrefix(other, p+"/") {
				return fmt.Errorf("an artifact cannot be the parent of another artifact")
			}
		}
		seen[p] = true
	}
	return nil
}
