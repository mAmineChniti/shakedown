package runner

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadMigrationsRecursive(t *testing.T) {
	dir := t.TempDir()
	nested := filepath.Join(dir, "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "001_a.sql"), []byte("SELECT 1;"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "002_b.sql"), []byte("SELECT 2;"), 0o644); err != nil {
		t.Fatal(err)
	}
	files, err := LoadMigrations([]string{dir})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Fatalf("got %d files, want 2", len(files))
	}
}