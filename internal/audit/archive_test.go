package audit

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestArchiveExportsPreservesAuthoritativeEvidence(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	archive := t.TempDir()
	if err := os.Chmod(archive, 0700); err != nil {
		t.Fatal(err)
	}
	if err := store.Emit("cell-1", "exec-1", "control", "execution.finished", nil); err != nil {
		t.Fatal(err)
	}
	source := store.file.Name()
	before := time.Now().Add(-time.Hour)
	if err := store.ArchiveExports(archive, before); err != nil {
		t.Fatal(err)
	}
	expected, err := os.ReadFile(source)
	if err != nil {
		t.Fatal("fresh export must remain hot", err)
	}
	if err := os.Chtimes(source, before, before); err != nil {
		t.Fatal(err)
	}
	final := filepath.Join(archive, filepath.Base(source))

	// A colliding archive must leave the authoritative and live evidence intact.
	if err := os.WriteFile(final, []byte("wrong journal"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := store.ArchiveExports(archive, before); err == nil {
		t.Fatal("accepted a conflicting archive segment")
	}
	if got, err := os.ReadFile(source); err != nil || !bytes.Equal(expected, got) {
		t.Fatalf("hot export was lost or modified: %v", err)
	}
	if err := os.WriteFile(final, expected, 0600); err != nil {
		t.Fatal(err)
	}

	// Crash recovery can encounter both copies. Identical archives can be reused.
	store.fault = errJournalFull
	if err := store.ArchiveExports(archive, before); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(source); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old export still present: %v", err)
	}
	if got, err := os.ReadFile(final); err != nil || !bytes.Equal(expected, got) {
		t.Fatalf("archive did not preserve byte identity: %v", err)
	}
	if store.total != 0 || store.Healthy() != nil {
		t.Fatalf("archive did not release hot capacity: %d, %v", store.total, store.Healthy())
	}
	if err := store.Emit("cell-1", "", "control", "cell.deleted", nil); err != nil {
		t.Fatal(err)
	}
	evidence, err := store.Events("cell-1", 0, 10)
	if err != nil || len(evidence) != 2 || evidence[0].Sequence != 1 || evidence[1].Sequence != 2 {
		t.Fatalf("journal evidence changed: %+v, %v", evidence, err)
	}
	next := store.file.Name()
	later := time.Now().Add(time.Hour)
	if err := store.ArchiveExports(archive, later); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(archive, filepath.Base(next))); err != nil {
		t.Fatal("new segment was not archived", err)
	}
}
