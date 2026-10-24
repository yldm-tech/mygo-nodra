package nodra

import (
	"encoding/json"
	"reflect"
	"testing"
)

func FuzzVersionedJSONDecode(f *testing.F) {
	for _, seed := range []string{`{"name":"a","values":[1,2]}`, `{"schemaVersion":1,"state":{"name":"a"}}`, `null`, `{`, `{"schemaVersion":9,"state":{}}`} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) > 1<<16 {
			t.Skip()
		}
		type state struct {
			Name   string
			Values []int
		}
		codec := VersionedJSONCodec[state]{Version: 1, AllowUnversioned: true, Migrations: map[int]JSONMigration{0: func(v json.RawMessage) (json.RawMessage, error) { return v, nil }}}
		value, err := codec.Decode(raw)
		if err != nil {
			return
		}
		encoded, err := codec.Encode(value)
		requireNoError(t, err)
		decoded, err := codec.Decode(encoded)
		requireNoError(t, err)
		if !reflect.DeepEqual(value, decoded) {
			t.Fatal("round trip changed state")
		}
	})
}

func FuzzHistoryStateMachine(f *testing.F) {
	f.Add([]byte{0, 4, 1, 2, 3, 1, 1, 2})
	f.Add([]byte{0, 0, 0, 0, 1, 1, 2, 0, 2})
	f.Fuzz(func(t *testing.T, operations []byte) {
		if len(operations) > 4096 {
			t.Skip()
		}
		store := New(0)
		history := Track(store, 8)
		defer func() { _ = history.Close() }()
		var undo, redo []int
		current := 0
		for _, op := range operations {
			switch op % 4 {
			case 0, 3:
				undo = append(undo, current)
				if len(undo) > 8 {
					undo = undo[len(undo)-8:]
				}
				redo = nil
				current = int(op)
				if op%4 == 0 {
					requireNoError(t, store.Set(current))
				} else {
					requireNoError(t, store.Batch(func() { _ = store.Set(-1); _ = store.Set(current) }))
				}
			case 1:
				err := history.Undo()
				if len(undo) == 0 {
					if err != ErrNoUndo {
						t.Fatalf("undo error=%v", err)
					}
					break
				}
				requireNoError(t, err)
				redo = append(redo, current)
				current, undo = undo[len(undo)-1], undo[:len(undo)-1]
			case 2:
				err := history.Redo()
				if len(redo) == 0 {
					if err != ErrNoRedo {
						t.Fatalf("redo error=%v", err)
					}
					break
				}
				requireNoError(t, err)
				undo = append(undo, current)
				current, redo = redo[len(redo)-1], redo[:len(redo)-1]
			}
			if store.Get() != current || history.CanUndo() != (len(undo) > 0) || history.CanRedo() != (len(redo) > 0) {
				t.Fatalf("state diverged after operation %d", op)
			}
		}
	})
}
