package forge

import (
	"crypto/rand"
	"errors"
	"io"
	"os"
	"path/filepath"
)

// Called while holding Forge's data-directory lock. The key is host-owned auth
// state, not a lease ledger. Back it up with config.json and Kata's database.
func loadExecutionSigner(dir string) (executionSigner, error) {
	path := filepath.Join(dir, "execution-key")
	f, err := openPrivateFile(path, os.O_RDONLY)
	if errors.Is(err, os.ErrNotExist) {
		key := make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return executionSigner{}, err
		}
		temp, err := os.CreateTemp(dir, ".execution-key-*")
		if err != nil {
			return executionSigner{}, err
		}
		defer os.Remove(temp.Name())
		_, writeErr := temp.Write(key)
		if err := errors.Join(writeErr, temp.Sync(), temp.Close()); err != nil {
			return executionSigner{}, err
		}
		if err := os.Link(temp.Name(), path); err != nil && !errors.Is(err, os.ErrExist) {
			return executionSigner{}, err
		}
		directory, err := os.Open(dir)
		if err != nil {
			return executionSigner{}, err
		}
		if err := errors.Join(directory.Sync(), directory.Close()); err != nil {
			return executionSigner{}, err
		}
		return loadExecutionSigner(dir)
	}
	if err != nil {
		return executionSigner{}, err
	}
	key, readErr := io.ReadAll(io.LimitReader(f, 33))
	if err := errors.Join(readErr, f.Close()); err != nil {
		return executionSigner{}, err
	}
	if len(key) != 32 {
		return executionSigner{}, errors.New("forge: invalid execution signing key; refusing to replace it")
	}
	return executionSigner{key: key}, nil
}
