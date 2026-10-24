package nodra

import "errors"

var ErrInvalidBinding = errors.New("nodra: invalid field binding")

// Field binds an immediate editor to one field of shared state. A write runs against the latest Store candidate, preserving unrelated concurrent changes. Read/write callbacks follow the Store ownership policy; use NewWithClone for mutable references. Keep form drafts separate when edits require explicit Save/Cancel.
type Field[T, V any] struct {
	store *Store[T]
	read  func(T) V
	write func(*T, V) error
}

func BindField[T, V any](store *Store[T], read func(T) V, write func(*T, V) error) (Field[T, V], error) {
	if store == nil || read == nil || write == nil {
		return Field[T, V]{}, ErrInvalidBinding
	}
	return Field[T, V]{store: store, read: read, write: write}, nil
}

// Get reads an isolated Store snapshot before selecting the field. Returned reference values have the source's clone policy.
func (f Field[T, V]) Get() V {
	if f.store == nil {
		var zero V
		return zero
	}
	return f.read(f.store.Get())
}

func (f Field[T, V]) Set(value V) error {
	if f.store == nil {
		return ErrInvalidBinding
	}
	return f.store.UpdateErr(func(state *T) error { return f.write(state, value) })
}

// Edit draws/edits a local field value and commits it only when edit reports a change. The editor runs outside Store locks, so it can render MyGo controls. Validation errors are returned to the owner; failed writes preserve shared state.
func (f Field[T, V]) Edit(edit func(*V) bool) error {
	if f.store == nil || edit == nil {
		return ErrInvalidBinding
	}
	value := f.Get()
	if !edit(&value) {
		return nil
	}
	return f.Set(value)
}
