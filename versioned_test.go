package nodra

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

type migratedState struct {
	Label string `json:"label"`
	Count int    `json:"count"`
}

func testMigrations() VersionedJSONCodec[migratedState] {
	return VersionedJSONCodec[migratedState]{
		Version: 2, AllowUnversioned: true,
		Migrations: map[int]JSONMigration{
			0: func(raw json.RawMessage) (json.RawMessage, error) {
				var old struct{ Name string }
				if err := json.Unmarshal(raw, &old); err != nil {
					return nil, err
				}
				return json.Marshal(map[string]string{"label": old.Name})
			},
			1: func(raw json.RawMessage) (json.RawMessage, error) {
				var state migratedState
				if err := json.Unmarshal(raw, &state); err != nil {
					return nil, err
				}
				state.Count = 1
				return json.Marshal(state)
			},
		},
	}
}

func TestVersionedJSONMigratesLegacyAndOlderEnvelopes(t *testing.T) {
	codec := testMigrations()
	for _, raw := range []string{
		`{"name":"word"}`,
		`{"schemaVersion":0,"state":{"name":"word"}}`,
		`{"schemaVersion":1,"state":{"label":"word"}}`,
	} {
		got, err := codec.Decode([]byte(raw))
		requireNoError(t, err)
		if got != (migratedState{Label: "word", Count: 1}) {
			t.Fatalf("migration of %s = %+v", raw, got)
		}
		encoded, err := codec.Encode(got)
		requireNoError(t, err)
		var envelope jsonEnvelope
		requireNoError(t, json.Unmarshal(encoded, &envelope))
		if envelope.Version == nil || *envelope.Version != 2 {
			t.Fatalf("encoded envelope = %s", encoded)
		}
		again, err := codec.Decode(encoded)
		requireNoError(t, err)
		if again != got {
			t.Fatalf("round trip = %+v, want %+v", again, got)
		}
	}
}

func TestVersionedJSONMigrationFailureLeavesStoreAndFileUnchanged(t *testing.T) {
	want := errors.New("cannot migrate")
	for _, failure := range []string{"migration", "missing", "future", "invalid-output"} {
		t.Run(failure, func(t *testing.T) {
			codec := testMigrations()
			storage := &memoryStorage{data: []byte(`{"name":"word"}`)}
			expected := want
			switch failure {
			case "migration":
				codec.Migrations[1] = func(json.RawMessage) (json.RawMessage, error) { return nil, want }
			case "missing":
				delete(codec.Migrations, 1)
				expected = ErrMissingMigration
			case "future":
				storage.data = []byte(`{"schemaVersion":3,"state":{}}`)
				expected = ErrUnsupportedVersion
			case "invalid-output":
				codec.Migrations[1] = func(json.RawMessage) (json.RawMessage, error) { return []byte(`{`), nil }
				expected = ErrInvalidPersistence
			}
			original := string(storage.data)
			initial := migratedState{Label: "keep", Count: 99}
			store := New(initial)
			p := NewPersistence(store, storage, codec)
			stop := p.AutoSave(context.Background())
			defer stop()
			if err := p.Load(context.Background()); !errors.Is(err, expected) {
				t.Fatalf("load error = %v, want %v", err, expected)
			}
			if store.Get() != initial || string(storage.data) != original || store.Version() != 0 {
				t.Fatal("failed migration modified state or storage")
			}
		})
	}
}

func TestVersionedJSONLoadOnlyPersistsOnRequest(t *testing.T) {
	storage := &memoryStorage{data: []byte(`{"name":"word"}`)}
	store := New(migratedState{})
	p := NewPersistence(store, storage, testMigrations())
	requireNoError(t, p.Load(context.Background()))
	if string(storage.data) != `{"name":"word"}` {
		t.Fatal("Load rewrote storage without Save")
	}
	requireNoError(t, p.Save(context.Background()))
	var envelope jsonEnvelope
	requireNoError(t, json.Unmarshal(storage.data, &envelope))
	if envelope.Version == nil || *envelope.Version != 2 {
		t.Fatalf("saved schema = %s", storage.data)
	}
}

func TestVersionedJSONRejectsMalformedEnvelopesAndInvalidTypes(t *testing.T) {
	codec := testMigrations()
	for _, raw := range []string{
		`{`, `{"schemaVersion":null,"state":{}}`, `{"schemaVersion":"2","state":{}}`,
		`{"schemaVersion":2}`, `{"schemaVersion":-1,"state":{}}`,
		`{"schemaVersion":2,"state":{"count":"wrong"}}`,
	} {
		if _, err := codec.Decode([]byte(raw)); err == nil {
			t.Errorf("accepted invalid JSON/schema: %s", raw)
		}
	}
	codec.AllowUnversioned = false
	if _, err := codec.Decode([]byte(`{"label":"raw"}`)); !errors.Is(err, ErrInvalidPersistence) {
		t.Fatalf("legacy data accepted without opt-in: %v", err)
	}
	invalid := VersionedJSONCodec[int]{}
	if _, err := invalid.Encode(1); !errors.Is(err, ErrInvalidPersistence) {
		t.Fatalf("invalid encode configuration = %v", err)
	}
	if _, err := invalid.Decode([]byte(`1`)); !errors.Is(err, ErrInvalidPersistence) {
		t.Fatalf("invalid decode configuration = %v", err)
	}
}

func TestVersionedJSONSupportsNonObjectState(t *testing.T) {
	codec := VersionedJSONCodec[[]int]{Version: 1, AllowUnversioned: true, Migrations: map[int]JSONMigration{
		0: func(raw json.RawMessage) (json.RawMessage, error) { return raw, nil },
	}}
	value, err := codec.Decode([]byte(`[1,2]`))
	requireNoError(t, err)
	encoded, err := codec.Encode(value)
	requireNoError(t, err)
	again, err := codec.Decode(encoded)
	requireNoError(t, err)
	if !reflect.DeepEqual(again, []int{1, 2}) {
		t.Fatalf("non-object state = %v", again)
	}
}
