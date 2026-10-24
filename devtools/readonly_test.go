package devtools

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/yldm-tech/nodra"
)

func TestReadOnlySourceNeverBecomesRestorable(t *testing.T) {
	i := New(Options{AllowRestore: true})
	defer func() { _ = i.Close() }()
	store := nodra.New(1)
	stop, err := Attach(i, "status", store.ReadOnly())
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	if err := store.Set(2); err != nil {
		t.Fatal(err)
	}
	view := i.Snapshot().Stores[0]
	if view.Restorable || string(view.State) != "2" {
		t.Fatalf("read-only source: %+v", view)
	}
	if err := i.Apply("status", json.RawMessage(`3`)); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("apply: %v", err)
	}
	if store.Get() != 2 {
		t.Fatal("inspector mutated read-only source")
	}
}
