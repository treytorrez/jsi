package transfer_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/treyt/jsi/internal/transfer"
)

// TestFilesFromPaths covers the send-list builder: stat, base-name
// sanitization, and rejection of dirs/missing/non-regular paths.
func TestFilesFromPaths(t *testing.T) {
	dir := t.TempDir()
	write := func(t *testing.T, name string, size int) string {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, make([]byte, size), 0o644); err != nil {
			t.Fatalf("WriteFile %s: %v", name, err)
		}
		return p
	}

	plain := write(t, "photo.jpg", 123)
	dotty := write(t, "..weird", 10) // sanitizes to the dotfile ".weird"
	empty := write(t, "empty.bin", 0)

	files, err := transfer.FilesFromPaths([]string{plain, dotty, empty})
	if err != nil {
		t.Fatalf("FilesFromPaths: %v", err)
	}
	if len(files) != 3 {
		t.Fatalf("got %d files, want 3", len(files))
	}
	if files[0].Path != plain || files[0].Name != "photo.jpg" || files[0].Size != 123 {
		t.Errorf("file 0: got %+v", files[0])
	}
	if files[1].Name != ".weird" {
		t.Errorf("file 1 name: got %q, want %q", files[1].Name, ".weird")
	}
	if files[2].Size != 0 {
		t.Errorf("file 2 size: got %d, want 0", files[2].Size)
	}

	cases := map[string]string{
		"missing":    filepath.Join(dir, "nope.bin"),
		"directory":  dir,
		"nonregular": "/dev/null",
	}
	for name, path := range cases {
		if _, err := transfer.FilesFromPaths([]string{path}); err == nil {
			t.Errorf("%s: FilesFromPaths(%q) succeeded, want error", name, path)
		}
	}
}
