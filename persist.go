package nodra

import (
	"context"
	"errors"
	"sync"
)

var ErrInvalidPersistence = errors.New("nodra: invalid persistence configuration")

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
type Persistence[T any] struct {
	store   *Store[T]
	storage Storage
	codec   Codec[T]
}

func NewPersistence[T any](store *Store[T], storage Storage, codec Codec[T]) *Persistence[T] {
	return &Persistence[T]{store: store, storage: storage, codec: codec}
}
func (p *Persistence[T]) Load(ctx context.Context) error {
	if p == nil || p.store == nil || p.storage == nil || p.codec == nil {
		return ErrInvalidPersistence
	}
	data, err := p.storage.Load(ctx)
	if err != nil {
		return err
	}
	value, err := p.codec.Decode(data)
	if err != nil {
		return err
	}
	return p.store.Set(value)
}
func (p *Persistence[T]) Save(ctx context.Context) error {
	if p == nil || p.store == nil || p.storage == nil || p.codec == nil {
		return ErrInvalidPersistence
	}
	data, err := p.codec.Encode(p.store.Get())
	if err != nil {
		return err
	}
	return p.storage.Save(ctx, data)
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
	var unsubscribe func()
	stop := func() { once.Do(func() { unsubscribe(); errMu.Lock(); closed = true; close(errs); errMu.Unlock() }) }
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
		case <-ctx.Done():
			stop()
		case <-p.store.done:
			stop()
		}
	}()
	return errs, stop
}
