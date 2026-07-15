package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tompscanlan/coinrollhunter/internal/store"
)

// AC11: backup is now a COMPLETE, RESTORABLE DIRECTORY bundle (db + photo originals), and
// the old single-.db form is a HARD ERROR — nobody silently receives a backup missing
// their photos. The derivative cache (regenerable) is in NEITHER.
func TestBackupWritesADirectoryBundleAndRefusesTheDotDBForm(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "crh.db")

	s, err := store.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	// Seed an original beside the DB, plus a (regenerable) cache sibling that must NOT be
	// backed up.
	owner := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	writeFile(t, filepath.Join(dir, "photos", owner, "photo1.jpg"), "original bytes")
	writeFile(t, filepath.Join(dir, "photos-cache", owner, "photo1-thumb.jpg"), "regenerable")

	// The old form is refused with a clear message.
	err = runBackup([]string{"--db", src, filepath.Join(dir, "backup.db")})
	if err == nil {
		t.Fatal("backup to a .db path succeeded — the old single-file form must be a hard error")
	}
	if !strings.Contains(err.Error(), "directory") {
		t.Errorf("hard-error message does not mention a directory: %v", err)
	}

	// The directory form writes crh.db + the photos ORIGINALS, and nothing of the cache.
	dest := filepath.Join(dir, "bundle")
	if err := runBackup([]string{"--db", src, dest}); err != nil {
		t.Fatalf("directory backup failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "crh.db")); err != nil {
		t.Errorf("bundle is missing crh.db: %v", err)
	}
	if got := readFile(t, filepath.Join(dest, "photos", owner, "photo1.jpg")); got != "original bytes" {
		t.Errorf("bundle photo original = %q, want the source bytes", got)
	}
	if _, err := os.Stat(filepath.Join(dest, "photos-cache")); !os.IsNotExist(err) {
		t.Error("the regenerable derivative cache was copied into the backup — it should be excluded")
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// copyTree must follow a symlinked ROOT (Codex review, om-usga): os.Stat follows the symlink
// so the IsDir check passes, but filepath.WalkDir does NOT — so a symlinked photos/ root (the
// originals moved to another drive and symlinked back) would be visited as one non-regular
// entry and skipped, and the backup would silently copy ZERO files: a backup that lies.
func TestCopyTreeFollowsASymlinkedRoot(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real-photos")
	writeFile(t, filepath.Join(real, "owner", "pic.jpg"), "the original bytes")
	link := filepath.Join(base, "photos")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable on this platform: %v", err)
	}
	dst := filepath.Join(base, "bundle-photos")
	n, err := copyTree(link, dst)
	if err != nil {
		t.Fatalf("copyTree through a symlinked root: %v", err)
	}
	if n != 1 {
		t.Fatalf("copied %d files through a symlinked photos/ root, want 1 — a symlinked root must not silently back up nothing", n)
	}
	if got := readFile(t, filepath.Join(dst, "owner", "pic.jpg")); got != "the original bytes" {
		t.Errorf("copied content = %q, want the original bytes", got)
	}
}
