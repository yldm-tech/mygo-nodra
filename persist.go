package nodra

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

var ErrInvalidPersistence = errors.New("nodra: invalid persistence configuration")

var ErrNoBackup = errors.New("nodra: backup storage unavailable")

// PersistenceOptions validates decoded and saved state before it reaches the Store or storage. Validate must be pure and concurrency-safe, and must not mutate its input.
type PersistenceOptions[T any] struct{ Validate func(T) error }

// LoadError identifies the failing load stage without changing state or files. Backup distinguishes explicit backup loads. Unwrap preserves codec, validation and filesystem errors.
type LoadError struct {
	Stage  string
	Backup bool
	Err    error
}

func (e *LoadError) Error() string {
	return fmt.Sprintf("nodra: load %s (backup=%t): %v", e.Stage, e.Backup, e.Err)
}
func (e *LoadError) Unwrap() error { return e.Err }

// Storage is the minimal persistence backend used by Persistence. Files,
// key-value databases and encrypted stores can implement it.
type Storage interface {
	Load(context.Context) ([]byte, error)
	Save(context.Context, []byte) error
}

// Codec serializes and restores one state value.
type Codec[T any] interface {
	Encode(T) ([]byte, error)
	Decode([]byte) (T, error)
}

// Persistence connects a store to a Storage and Codec. It is explicit by
// default: call Load at startup and Save after successful application actions.
// Save calls on the same instance are serialized, including state capture and
// encoding. Separate instances sharing a storage destination are not coordinated.
type Persistence[T any] struct {
	saveMu  sync.Mutex
	store   *Store[T]
	storage Storage
	codec   Codec[T]
	options PersistenceOptions[T]
}

func NewPersistence[T any](store *Store[T], storage Storage, codec Codec[T]) *Persistence[T] {
	return NewPersistenceWithOptions(store, storage, codec, PersistenceOptions[T]{})
}

func NewPersistenceWithOptions[T any](store *Store[T], storage Storage, codec Codec[T], options PersistenceOptions[T]) *Persistence[T] {
	return &Persistence[T]{store: store, storage: storage, codec: codec, options: options}
}

func (p *Persistence[T]) Load(ctx context.Context) error {
	return p.load(ctx, false)
}

// LoadBackup explicitly restores validated backup state to memory. It never rewrites the primary file, so a corrupt original remains available for diagnosis. Call before starting autosave; a later Save intentionally writes the current state to disk.
func (p *Persistence[T]) LoadBackup(ctx context.Context) error { return p.load(ctx, true) }

func (p *Persistence[T]) load(ctx context.Context, backup bool) error {
	if p == nil || p.store == nil || p.storage == nil || p.codec == nil {
		return ErrInvalidPersistence
	}
	if ctx == nil {
		ctx = context.Background()
	}
	load := p.storage.Load
	if backup {
		source, ok := p.storage.(interface {
			LoadBackup(context.Context) ([]byte, error)
		})
		if !ok {
			return ErrNoBackup
		}
		load = source.LoadBackup
	}
	data, err := load(ctx)
	if err != nil {
		return &LoadError{Stage: "read", Backup: backup, Err: err}
	}
	value, err := p.codec.Decode(data)
	if err != nil {
		return &LoadError{Stage: "decode", Backup: backup, Err: err}
	}
	if p.options.Validate != nil {
		if err := p.options.Validate(value); err != nil {
			return &LoadError{Stage: "validate", Backup: backup, Err: err}
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return p.store.Set(value)
}
func (p *Persistence[T]) Save(ctx context.Context) error {
	_, err := p.save(ctx)
	return err
}

// save returns the version actually written, rather than a version sampled
// before waiting on another save. Async flush barriers use that acknowledgement.
func (p *Persistence[T]) save(ctx context.Context) (uint64, error) {
	if p == nil || p.store == nil || p.storage == nil || p.codec == nil {
		return 0, ErrInvalidPersistence
	}
	if ctx == nil {
		ctx = context.Background()
	}
	// Capture state only after taking the lock: a delayed older save must not
	// overwrite a newer one, even when encoding itself is slow.
	p.saveMu.Lock()
	defer p.saveMu.Unlock()
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	snapshot := p.store.Snapshot()
	if p.options.Validate != nil {
		if err := p.options.Validate(snapshot.Value); err != nil {
			return snapshot.Version, err
		}
	}
	data, err := p.codec.Encode(snapshot.Value)
	if err != nil {
		return snapshot.Version, err
	}
	return snapshot.Version, p.storage.Save(ctx, data)
}

// AutoSave subscribes to updates and saves synchronously after each update.
// The returned function stops autosaving. Use Batch to coalesce writes.
func (p *Persistence[T]) AutoSave(ctx context.Context) func() {
	if p == nil || p.store == nil {
		return func() {}
	}
	return p.store.Subscribe(func(Snapshot[T]) { _ = p.Save(ctx) })
}

// AutoSaveWithErrors subscribes to updates and reports persistence failures on
// a buffered channel. The channel closes when ctx is canceled or stop is called.
// Stop releases the background watcher but does not wait for in-flight saves.
func (p *Persistence[T]) AutoSaveWithErrors(ctx context.Context) (<-chan error, func()) {
	if p == nil || p.store == nil {
		ch := make(chan error)
		close(ch)
		return ch, func() {}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	errs := make(chan error, 8)
	var once sync.Once
	var errMu sync.Mutex
	closed := false
	stopped := make(chan struct{})
	var unsubscribe func()
	stop := func() {
		once.Do(func() {
			unsubscribe()
			errMu.Lock()
			closed = true
			close(errs)
			errMu.Unlock()
			close(stopped)
		})
	}
	unsubscribe = p.store.Subscribe(func(Snapshot[T]) {
		if err := p.Save(ctx); err != nil {
			errMu.Lock()
			defer errMu.Unlock()
			if closed {
				return
			}
			select {
			case errs <- err:
			default:
			}
		}
	})
	go func() {
		select {
		case <-stopped:
			return
		case <-ctx.Done():
			stop()
		case <-p.store.done:
			stop()
		}
	}()
	return errs, stop
}
