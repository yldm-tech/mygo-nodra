package nodra

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// JSONCodec encodes a store value using encoding/json.
type JSONCodec[T any] struct{}

func (JSONCodec[T]) Encode(value T) ([]byte, error) { return json.MarshalIndent(value, "", "  ") }
func (JSONCodec[T]) Decode(data []byte) (T, error) {
	var value T
	err := json.Unmarshal(data, &value)
	return value, err
}

// FileStorage stores encoded state at Path. Save writes a temporary file in
// the same directory and renames it into place, so readers see a complete file.
type FileStorage struct {
	Path string
	Perm os.FileMode
}

func (f FileStorage) Load(ctx context.Context) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if f.Path == "" {
		return nil, ErrInvalidPersistence
	}
	return os.ReadFile(f.Path)
}
func (f FileStorage) Save(ctx context.Context, data []byte) (err error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	if f.Path == "" {
		return ErrInvalidPersistence
	}
	dir := filepath.Dir(f.Path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".nodra-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()
	perm := f.Perm
	if perm == 0 {
		perm = 0600
	}
	if err = tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err = tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err = tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = os.Rename(name, f.Path); err != nil {
		return fmt.Errorf("nodra: replace %s: %w", f.Path, err)
	}
	return nil
}
