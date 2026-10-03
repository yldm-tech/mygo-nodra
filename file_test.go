package nodra

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestFileStorageAndJSONCodecRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "tasks.json")
	storage := FileStorage{Path: path}
	store := New(counterState{Count: 4, Name: "ready"})
	p := NewPersistence(store, storage, JSONCodec[counterState]{})
	if err := p.Save(context.Background()); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("perm=%v", info.Mode().Perm())
	}
	_ = store.Set(counterState{})
	if err := p.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	if store.Get().Name != "ready" || store.Get().Count != 4 {
		t.Fatalf("value=%+v", store.Get())
	}
}
func TestFileStorageHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	storage := FileStorage{Path: filepath.Join(t.TempDir(), "state.json")}
	if err := storage.Save(ctx, []byte("x")); !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v", err)
	}
}
