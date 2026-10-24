package nodra

import "context"

// Reader is the observation surface shared by Store and ReadOnly. Only Nodra sources implement it; a read-only wrapper never exposes its backing Store. Snapshot ownership and callback concurrency are unchanged.
type Reader[T any] interface {
	Get() T
	State() T
	Snapshot() Snapshot[T]
	Version() uint64
	ID() string
	Done() <-chan struct{}
	Subscribe(func(Snapshot[T])) func()
	SubscribeWith(SubscribeOptions[T], func(Snapshot[T])) func()
	SubscribeMutations(func(Mutation[T])) func()
	SubscribeActions(func(ActionEvent)) func()
	Watch(context.Context, int) <-chan Snapshot[T]
	readStore() *Store[T]
}

// ReadOnly grants observation without Set, Update, Reset, action hooks or Close. Values still follow the source's cloning/immutability policy; this is a capability boundary, not a deep-freeze operation. Its zero value is an empty, invalid source (Done returns nil).
type ReadOnly[T any] struct{ store *Store[T] }

func (s *Store[T]) ReadOnly() ReadOnly[T] { return ReadOnly[T]{store: s} }
func (s *Store[T]) readStore() *Store[T]  { return s }
func (s *Store[T]) Done() <-chan struct{} {
	if s == nil {
		return nil
	}
	return s.done
}

func sourceStore[T any](source Reader[T]) *Store[T] {
	if source == nil {
		return nil
	}
	return source.readStore()
}

func (r ReadOnly[T]) readStore() *Store[T] { return r.store }
func (r ReadOnly[T]) Get() T               { return r.Snapshot().Value }
func (r ReadOnly[T]) State() T             { return r.Get() }
func (r ReadOnly[T]) Snapshot() Snapshot[T] {
	if r.store == nil {
		return Snapshot[T]{}
	}
	return r.store.Snapshot()
}
func (r ReadOnly[T]) Version() uint64 {
	if r.store == nil {
		return 0
	}
	return r.store.Version()
}
func (r ReadOnly[T]) ID() string            { return r.store.ID() }
func (r ReadOnly[T]) Done() <-chan struct{} { return r.store.Done() }
func (r ReadOnly[T]) Subscribe(fn func(Snapshot[T])) func() {
	return r.SubscribeWith(SubscribeOptions[T]{}, fn)
}
func (r ReadOnly[T]) SubscribeWith(options SubscribeOptions[T], fn func(Snapshot[T])) func() {
	if r.store == nil {
		return func() {}
	}
	return r.store.SubscribeWith(options, fn)
}
func (r ReadOnly[T]) SubscribeMutations(fn func(Mutation[T])) func() {
	if r.store == nil {
		return func() {}
	}
	return r.store.SubscribeMutations(fn)
}
func (r ReadOnly[T]) SubscribeActions(fn func(ActionEvent)) func() {
	if r.store == nil {
		return func() {}
	}
	return r.store.SubscribeActions(fn)
}
func (r ReadOnly[T]) Watch(ctx context.Context, buffer int) <-chan Snapshot[T] {
	if r.store == nil {
		ch := make(chan Snapshot[T])
		close(ch)
		return ch
	}
	return r.store.Watch(ctx, buffer)
}
