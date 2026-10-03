package nodra

import "testing"

func TestHistoryUndoRedo(t *testing.T) {
	store := New(0)
	history := Track(store, 10)
	store.Set(1)
	store.Set(2)
	if !history.CanUndo() || history.CanRedo() {
		t.Fatal("history flags after updates")
	}
	if err := history.Undo(); err != nil || store.Get() != 1 {
		t.Fatalf("undo value=%d err=%v", store.Get(), err)
	}
	if err := history.Redo(); err != nil || store.Get() != 2 {
		t.Fatalf("redo value=%d err=%v", store.Get(), err)
	}
	store.Set(3)
	if history.CanRedo() {
		t.Fatal("redo should clear after a new update")
	}
	history.Close()
}
