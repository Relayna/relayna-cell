package audit

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
)

var exportSegment = regexp.MustCompile(`^events-[0-9]{20}\.ndjson$`)

// ArchiveExports retains sealed NDJSON exports outside the hot Alloy directory.
// The authoritative SQLite rows stay available for queries and replay.
func (s *Store) ArchiveExports(archiveDir string, before time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !filepath.IsAbs(archiveDir) {
		return fmt.Errorf("audit archive directory must be absolute")
	}
	archive, err := filepath.EvalSymlinks(archiveDir)
	if err != nil {
		return err
	}
	logs, err := filepath.EvalSymlinks(s.dir)
	if err != nil {
		return err
	}
	logs, err = filepath.Abs(logs)
	if err != nil {
		return err
	}
	if archive == logs || strings.HasPrefix(archive, logs+string(filepath.Separator)) {
		return fmt.Errorf("audit archive directory must be outside the hot logs directory")
	}
	archiveHandle, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer archiveHandle.Close()
	st, err := archiveHandle.Stat()
	if err != nil {
		return err
	}
	owner, ok := st.Sys().(*syscall.Stat_t)
	if !ok || owner.Uid != uint32(os.Geteuid()) || !st.IsDir() || st.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("audit archive directory must be owner-only")
	}

	if s.file != nil {
		st, err := s.file.Stat()
		if err != nil {
			return err
		}
		if !st.ModTime().After(before) {
			if err := s.file.Close(); err != nil {
				return err
			}
			s.file = nil
			s.size = 0
		}
	}

	entries, err := os.ReadDir(logs)
	if err != nil {
		return err
	}
	logHandle, err := os.Open(logs)
	if err != nil {
		return err
	}
	defer logHandle.Close()
	for _, entry := range entries {
		if !entry.Type().IsRegular() || !exportSegment.MatchString(entry.Name()) ||
			s.file != nil && filepath.Base(s.file.Name()) == entry.Name() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.ModTime().After(before) {
			continue
		}
		source := filepath.Join(logs, entry.Name())
		if err := copyExport(source, archive, archiveHandle); err != nil {
			return fmt.Errorf("archive audit segment %q: %w", entry.Name(), err)
		}
		if err := os.Remove(source); err != nil {
			return err
		}
		s.total -= info.Size()
		if err := logHandle.Sync(); err != nil {
			return err
		}
	}
	// A copy failure or corruption must never clear a journal fault.
	if errors.Is(s.fault, errJournalFull) && s.total+(256<<10)+128 <= MaxJournalBytes {
		s.fault = nil
	}
	return nil
}

// Sync the archive copy before removing the source. A matching archive left
// by an interrupted run is safe to reuse.
func copyExport(source, dir string, archive *os.File) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	final := filepath.Join(dir, filepath.Base(source))
	if old, err := os.Lstat(final); err == nil {
		if !old.Mode().IsRegular() {
			return fmt.Errorf("archive destination is not a regular file")
		}
		if err := compareExport(input, final); err != nil {
			return err
		}
		return archive.Sync()
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	temp, err := os.CreateTemp(dir, ".audit-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	_, err = io.Copy(temp, input)
	if err == nil {
		err = temp.Sync()
	}
	if err = errors.Join(err, temp.Close()); err != nil {
		return err
	}
	if err := os.Link(temp.Name(), final); err != nil {
		return err
	}
	return archive.Sync()
}

func compareExport(input *os.File, path string) error {
	existing, err := os.Open(path)
	if err != nil {
		return err
	}
	defer existing.Close()
	a, b := sha256.New(), sha256.New()
	if _, err := io.Copy(a, input); err != nil {
		return err
	}
	if _, err := io.Copy(b, existing); err != nil {
		return err
	}
	if !bytes.Equal(a.Sum(nil), b.Sum(nil)) {
		return fmt.Errorf("archive destination has different contents")
	}
	return nil
}
