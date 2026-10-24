package nodra

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/gofrs/flock"
)

var ErrFileConflict = errors.New("nodra: file content changed since last observation")

// FileFingerprint identifies the bytes observed at a destination. Exists distinguishes a missing file from an empty file. Equal-content replacements and changes reverted before the next check are indistinguishable.
type FileFingerprint struct {
	Exists bool
	SHA256 [sha256.Size]byte
}

// FileConflictError reports conflicting content without embedding file data. Rejected saves leave primary and backup bytes unchanged. Load explicitly acknowledges current primary content; use a different destination to preserve both versions instead.
type FileConflictError struct {
	Path     string
	Expected FileFingerprint
	Actual   FileFingerprint
}

func (e *FileConflictError) Error() string { return fmt.Sprintf("%v: %s", ErrFileConflict, e.Path) }
func (e *FileConflictError) Unwrap() error { return ErrFileConflict }

// CheckedFileStorage opts into content-conflict detection for a FileStorage destination. Use the returned pointer without copying it. Cooperating instances serialize checks and replacements through a persistent Path+".lock" sidecar. Never remove that sidecar while any instance may be active. Plain FileStorage and editors do not honor the advisory lock: their edits completed before the check are detected, but a concurrent uncooperative write can race the replacement. Use regular local files and the same canonical destination; hard-link aliases and network filesystem locking are outside this contract.
type CheckedFileStorage struct {
	file     FileStorage
	access   chan struct{}
	expected FileFingerprint
}

// WithConflictDetection creates the parent directory/lock sidecar and observes current primary bytes as the initial baseline, including absence. It does not load or validate Store state. Parent directory aliases are canonicalized; primary symlinks and nonregular files are rejected. All cooperating writers must opt in; ordinary FileStorage behavior remains unchanged.
func (f FileStorage) WithConflictDetection(ctx context.Context) (*CheckedFileStorage, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if f.Path == "" {
		return nil, ErrInvalidPersistence
	}
	absolute, err := filepath.Abs(f.Path)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(absolute), 0755); err != nil {
		return nil, err
	}
	dir, err := filepath.EvalSymlinks(filepath.Dir(absolute))
	if err != nil {
		return nil, err
	}
	f.Path = filepath.Join(dir, filepath.Base(absolute))
	c := &CheckedFileStorage{file: f, access: make(chan struct{}, 1)}
	err = c.withLock(ctx, func() error {
		_, fingerprint, err := fingerprintFile(f.Path)
		if err == nil || errors.Is(err, os.ErrNotExist) {
			c.expected = fingerprint
			return nil
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	return c, nil
}

// Load explicitly accepts the bytes read as the new baseline. A missing primary acknowledges absence and still returns os.ErrNotExist. Decode/validation remains Persistence's responsibility; stop on those errors before starting autosave.
func (c *CheckedFileStorage) Load(ctx context.Context) (data []byte, err error) {
	err = c.withLock(ctx, func() error {
		var fingerprint FileFingerprint
		var readErr error
		data, fingerprint, readErr = fingerprintFile(c.file.Path)
		if readErr == nil || errors.Is(readErr, os.ErrNotExist) {
			c.expected = fingerprint
		}
		return readErr
	})
	return data, err
}

// LoadBackup reads the backup without acknowledging changed primary content. Recovery does not silently authorize replacing external primary edits.
func (c *CheckedFileStorage) LoadBackup(ctx context.Context) (data []byte, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	err = c.withLock(ctx, func() error {
		var readErr error
		data, readErr = c.file.LoadBackup(ctx)
		return readErr
	})
	return data, err
}

// Save rejects content that differs from the last successful Load or primary replacement. A new instance uses the constructor's baseline. A failed backup can follow a successful primary write; that primary is acknowledged so a retry does not conflict with this instance's own write. Conflicts leave primary and backup untouched and remain conflicts until the application explicitly loads/reconciles or chooses another destination.
func (c *CheckedFileStorage) Save(ctx context.Context, data []byte) error {
	if ctx == nil {
		ctx = context.Background()
	}
	return c.withLock(ctx, func() error {
		_, actual, err := fingerprintFile(c.file.Path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if actual != c.expected {
			return &FileConflictError{Path: c.file.Path, Expected: c.expected, Actual: actual}
		}
		return c.file.save(ctx, data, func() {
			c.expected = FileFingerprint{Exists: true, SHA256: sha256.Sum256(data)}
		})
	})
}

func (c *CheckedFileStorage) withLock(ctx context.Context, fn func() error) (err error) {
	if c == nil || c.access == nil {
		return ErrInvalidPersistence
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// The channel serializes one instance with a cancelable wait and gives Go's race detector an in-process happens-before edge in addition to the OS lock.
	select {
	case <-ctx.Done():
		return ctx.Err()
	case c.access <- struct{}{}:
	}
	lock := flock.New(c.file.Path + ".lock")
	defer func() {
		err = errors.Join(err, lock.Close())
		<-c.access
	}()
	locked, err := lock.TryLockContext(ctx, 10*time.Millisecond)
	if err != nil {
		return err
	}
	if !locked {
		return errors.New("nodra: file lock not acquired")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return fn()
}

func fingerprintFile(path string) ([]byte, FileFingerprint, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, FileFingerprint{}, err
	}
	if !info.Mode().IsRegular() {
		return nil, FileFingerprint{}, fmt.Errorf("%w: conflict detection requires a regular non-symlink file: %s", ErrInvalidPersistence, path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, FileFingerprint{}, err
	}
	return data, FileFingerprint{Exists: true, SHA256: sha256.Sum256(data)}, nil
}
