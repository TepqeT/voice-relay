// Package statefile reads the bot's state files (data/*.json, *.jsonl)
// without letting a path escape the directory it names (gosec G304).
package statefile

import (
	"os"
	"path/filepath"
)

// DirPerm is the permission of the state directories: owner and group only
// (gosec G301).
const DirPerm os.FileMode = 0o750

// ReadFile reads path through an os.Root opened on its directory, so the
// read cannot follow ".." or a symbolic link out of that directory.
func ReadFile(path string) ([]byte, error) {
	root, name, err := open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	return root.ReadFile(name)
}

// Open opens path for reading the same way. The file stays usable after the
// root is closed.
func Open(path string) (*os.File, error) {
	root, name, err := open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	return root.Open(name)
}

func open(path string) (*os.Root, string, error) {
	clean := filepath.Clean(path)
	root, err := os.OpenRoot(filepath.Dir(clean))
	if err != nil {
		return nil, "", err
	}
	return root, filepath.Base(clean), nil
}
