// Package taskinference mediates the dedicated SDK task inference route.
package taskinference

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/sarattha/relayna-cell/pkg/cell"
	"golang.org/x/sys/unix"
)

const chatPath = "/v1/chat/completions"

type config struct {
	GatewayURL      string `json:"gateway_url"`
	Model           string `json:"model"`
	TaskService     string `json:"task_service"`
	GatewayKeyFile  string `json:"gateway_key_file"`
	WorkerTokenFile string `json:"worker_token_file"`
}

func Open(path string, log func(string, string, any) error) (*Proxy, error) {
	data, err := readProtected(path, 16<<10)
	if err != nil {
		return nil, fmt.Errorf("read task inference config: %w", err)
	}
	var cfg config
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil || decoder.Decode(new(any)) != io.EOF {
		return nil, fmt.Errorf("invalid task inference config JSON")
	}
	u, err := url.Parse(cfg.GatewayURL)
	if err != nil || u.Hostname() == "" || u.User != nil || u.Fragment != "" ||
		u.RawQuery != "" || u.ForceQuery || u.EscapedPath() != chatPath ||
		u.Scheme != "https" && !(u.Scheme == "http" && net.ParseIP(u.Hostname()).IsLoopback()) {
		return nil, fmt.Errorf("gateway_url must be an HTTPS chat completions URL or an HTTP literal loopback URL")
	}
	if cfg.Model == "" || len(cfg.Model) > 256 || strings.TrimSpace(cfg.Model) != cfg.Model ||
		!cell.ValidTaskInferenceID(cfg.TaskService) {
		return nil, fmt.Errorf("an exact model and a Gateway task service are required")
	}
	key, err := readToken(cfg.GatewayKeyFile)
	if err != nil {
		return nil, fmt.Errorf("read Gateway key file: %w", err)
	}
	worker, err := readToken(cfg.WorkerTokenFile)
	if err != nil {
		return nil, fmt.Errorf("read worker token file: %w", err)
	}
	return newProxy(u.String(), cfg.Model, cfg.TaskService, key, worker, log), nil
}

func readToken(path string) (string, error) {
	data, err := readProtected(path, 4096)
	if err != nil {
		return "", err
	}
	token := strings.TrimSuffix(strings.TrimSuffix(string(data), "\n"), "\r")
	if token == "" {
		return "", fmt.Errorf("token is empty")
	}
	for _, b := range []byte(token) {
		if b < 33 || b > 126 {
			return "", fmt.Errorf("token must be printable ASCII without whitespace")
		}
	}
	return token, nil
}

func readProtected(path string, limit int64) ([]byte, error) {
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("file path must be absolute")
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open protected file: %w", err)
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	metadata, ok := st.Sys().(*syscall.Stat_t)
	if !ok || !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 || metadata.Uid != uint32(os.Geteuid()) {
		return nil, fmt.Errorf("expected an owner-only regular file owned by the daemon user")
	}
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(b)) > limit {
		return nil, fmt.Errorf("could not read protected file within size limit")
	}
	return b, nil
}
