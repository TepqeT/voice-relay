package statefile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadFileAndOpen(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "state.json")
	if err := os.WriteFile(p, []byte(`{"a":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	b, err := ReadFile(p)
	if err != nil || string(b) != `{"a":1}` {
		t.Fatalf("ReadFile = %q, %v", b, err)
	}
	f, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 7)
	if _, err := f.Read(buf); err != nil || string(buf) != `{"a":1}` {
		t.Fatalf("file usable after the root is closed: %q, %v", buf, err)
	}
	_ = f.Close()
}

func TestMissingFileIsNotExist(t *testing.T) {
	if _, err := ReadFile(filepath.Join(t.TempDir(), "none.json")); !os.IsNotExist(err) {
		t.Fatalf("err = %v, want not exist", err)
	}
	if _, err := Open(filepath.Join(t.TempDir(), "nodir", "none.json")); !os.IsNotExist(err) {
		t.Fatalf("missing directory: err = %v, want not exist", err)
	}
}

// A symbolic link out of the state directory is refused.
func TestSymlinkEscapeRefused(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(outside, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	link := filepath.Join(dir, "state.json")
	if err := os.Symlink(outside, link); err != nil {
		t.Skip(err)
	}
	if _, err := ReadFile(link); err == nil {
		t.Fatal("a link out of the directory must not be read")
	}
}
