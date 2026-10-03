package nodra

// Definition is a reusable named store factory, analogous to Pinia's
// defineStore and Zustand's reusable vanilla store creator.
type Definition[T any] struct {
	id      string
	initial func() T
}

// Define creates a store definition. Each New call gets independent state.
func Define[T any](id string, initial func() T) *Definition[T] {
	if initial == nil {
		initial = func() T { var zero T; return zero }
	}
	return &Definition[T]{id: id, initial: initial}
}
func (d *Definition[T]) ID() string {
	if d == nil {
		return ""
	}
	return d.id
}
func (d *Definition[T]) New() *Store[T] {
	if d == nil {
		return New(*new(T))
	}
	return newStore(d.id, d.initial())
}
func (s *Store[T]) ID() string {
	if s == nil {
		return ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.id
}
