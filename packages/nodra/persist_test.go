package nodra

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

type memoryStorage struct {
	data []byte
	err  error
}

func (m *memoryStorage) Load(context.Context) ([]byte, error) {
	return append([]byte(nil), m.data...), m.err
}
func (m *memoryStorage) Save(_ context.Context, data []byte) error {
	if m.err != nil {
		return m.err
	}
	m.data = append([]byte(nil), data...)
	return nil
}

type jsonCodec[T any] struct{}

func (jsonCodec[T]) Encode(value T) ([]byte, error) { return json.Marshal(value) }
func (jsonCodec[T]) Decode(data []byte) (T, error) {
	var value T
	err := json.Unmarshal(data, &value)
	return value, err
}

func TestPersistenceLoadSave(t *testing.T) {
	storage := &memoryStorage{}
	store := New(map[string]int{"count": 1})
	p := NewPersistence(store, storage, jsonCodec[map[string]int]{})
	if err := p.Save(context.Background()); err != nil {
		t.Fatal(err)
	}
	store.Set(map[string]int{"count": 9})
	if err := p.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	if store.Get()["count"] != 1 {
		t.Fatalf("restored = %#v", store.Get())
	}
	storage.err = errors.New("disk failed")
	if err := p.Save(context.Background()); !errors.Is(err, storage.err) {
		t.Fatalf("save error = %v", err)
	}
}

func TestAutoSaveWithErrorsReportsAndStops(t *testing.T) {
	storage := &memoryStorage{err: errors.New("disk failed")}
	store := New(0)
	p := NewPersistence(store, storage, jsonCodec[int]{})
	errs, stop := p.AutoSaveWithErrors(context.Background())
	store.Set(1)
	select {
	case err := <-errs:
		if !errors.Is(err, storage.err) {
			t.Fatalf("error = %v", err)
		}
	default:
		t.Fatal("expected persistence error")
	}
	stop()
	store.Set(2)
}
