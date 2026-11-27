package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"syscall"

	"github.com/sarattha/relayna-cell/pkg/client"
)

const maxArtifactBytes = 4 << 20
const maxArtifactTotal = 16 << 20

type artifact struct {
	Path   string `json:"path"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}

type artifactExport struct {
	root  *os.Root
	paths []string
}

func openArtifactExport(dir string, paths []string) (*artifactExport, error) {
	if dir == "" && len(paths) == 0 {
		return nil, nil
	}
	if len(paths) == 0 || !filepath.IsAbs(dir) {
		return nil, fmt.Errorf("artifact export requires a profile artifact list and an absolute --artifact-dir")
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	st, err := root.Stat(".")
	if err == nil {
		owner, ok := st.Sys().(*syscall.Stat_t)
		if !ok || owner.Uid != uint32(os.Geteuid()) || !st.IsDir() || st.Mode().Perm()&0077 != 0 {
			err = fmt.Errorf("artifact directory must be owner-only and owned by the launcher user")
		}
	}
	if err != nil {
		root.Close()
		return nil, err
	}
	return &artifactExport{root: root, paths: paths}, nil
}

type stagedArtifacts struct {
	root  *os.Root
	id    string
	files []artifact
}

func (a *artifactExport) collect(ctx context.Context, c *client.Client, ids taskResponse) (*stagedArtifacts, error) {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	staged := &stagedArtifacts{root: a.root, id: "attempt-" + hex.EncodeToString(nonce[:])}
	if err := a.root.Mkdir(staged.pending(), 0700); err != nil {
		return nil, err
	}
	complete := false
	defer func() {
		if !complete {
			staged.discard()
		}
	}()
	remaining := int64(maxArtifactTotal)
	for _, name := range a.paths {
		dest := path.Join(staged.pending(), "files", name)
		if err := a.root.MkdirAll(path.Dir(dest), 0700); err != nil {
			return nil, err
		}
		item, err := a.copyFile(ctx, c, ids.CellID, name, dest, min(remaining, maxArtifactBytes))
		if err != nil {
			return nil, err
		}
		for dir := path.Dir(dest); dir != staged.pending(); dir = path.Dir(dir) {
			if err := syncArtifactDir(a.root, dir); err != nil {
				return nil, err
			}
		}
		remaining -= item.Bytes
		staged.files = append(staged.files, item)
	}
	manifest := struct {
		TaskID      string     `json:"task_id"`
		AttemptID   string     `json:"attempt_id"`
		CellID      string     `json:"cell_id"`
		ExecutionID string     `json:"execution_id"`
		Profile     string     `json:"profile"`
		Artifacts   []artifact `json:"artifacts"`
	}{ids.TaskID, ids.AttemptID, ids.CellID, ids.ExecutionID, ids.Profile, staged.files}
	f, err := a.root.OpenFile(path.Join(staged.pending(), "manifest.json"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return nil, err
	}
	err = json.NewEncoder(f).Encode(manifest)
	if err == nil {
		err = f.Sync()
	}
	if err = errors.Join(err, f.Close()); err != nil {
		return nil, err
	}
	if err := syncArtifactDir(a.root, staged.pending()); err != nil {
		return nil, err
	}
	complete = true
	return staged, nil
}

func (a *artifactExport) copyFile(ctx context.Context, c *client.Client, id, name, dest string, limit int64) (artifact, error) {
	res, err := c.Request(ctx, http.MethodGet, "/v1alpha1/cells/"+url.PathEscape(id)+"/files?path="+url.QueryEscape(name), nil)
	if err != nil {
		return artifact{}, err
	}
	defer res.Body.Close()
	f, err := a.root.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return artifact{}, err
	}
	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, hash), io.LimitReader(res.Body, limit+1))
	if err == nil && n > limit {
		err = fmt.Errorf("artifact output exceeds the configured limit")
	}
	if err == nil {
		err = f.Sync()
	}
	return artifact{Path: name, Bytes: n, SHA256: hex.EncodeToString(hash.Sum(nil))}, errors.Join(err, f.Close())
}

func (s *stagedArtifacts) pending() string {
	return "." + s.id
}

func (s *stagedArtifacts) discard() {
	_ = s.root.RemoveAll(s.pending())
}

func (s *stagedArtifacts) publish() (string, error) {
	if err := s.root.Rename(s.pending(), s.id); err != nil {
		return "", err
	}
	if err := syncArtifactDir(s.root, "."); err != nil {
		_ = s.root.RemoveAll(s.id)
		return "", err
	}
	return path.Join(s.id, "manifest.json"), nil
}

func syncArtifactDir(root *os.Root, dir string) error {
	f, err := root.Open(dir)
	if err != nil {
		return err
	}
	return errors.Join(f.Sync(), f.Close())
}
