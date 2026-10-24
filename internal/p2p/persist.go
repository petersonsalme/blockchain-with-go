package p2p

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// LoadAddrs reads the persisted addresses; a missing file is an empty list.
func LoadAddrs(path string) ([]string, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return []string{}, nil
	}
	if err != nil {
		return nil, err
	}
	addrs := []string{}
	if err := json.Unmarshal(raw, &addrs); err != nil {
		return nil, err
	}
	return addrs, nil
}

// SaveAddrs writes the addresses as a JSON array with mode 0o600, through a
// temporary file in the same directory that is renamed over path.
func SaveAddrs(path string, addrs []string) error {
	if addrs == nil {
		addrs = []string{}
	}
	raw, err := json.Marshal(addrs)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".peers-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if err := writeAndClose(f, raw); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

type tmpFile interface {
	Write([]byte) (int, error)
	Chmod(os.FileMode) error
	Sync() error
	Close() error
}

// writeAndClose writes raw, sets mode 0o600 and syncs; the file is closed on
// every path.
func writeAndClose(f tmpFile, raw []byte) error {
	if _, err := f.Write(raw); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}
