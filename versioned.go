package nodra

import (
	"encoding/json"
	"errors"
	"fmt"
)

var (
	ErrUnsupportedVersion = errors.New("nodra: unsupported schema version")
	ErrMissingMigration   = errors.New("nodra: missing schema migration")
)

// JSONMigration converts one schema version to the next. Inputs and outputs
// are state JSON, without the version envelope. Return an error to abort Load.
type JSONMigration func(json.RawMessage) (json.RawMessage, error)

// VersionedJSONCodec stores {"schemaVersion": N, "state": ...}. Migrations[n]
// upgrades version n to n+1, sequentially. AllowUnversioned accepts legacy raw
// JSON as version zero. Version must be positive; future versions are rejected.
// Decode never writes storage: persist a successful upgrade explicitly with Save.
type VersionedJSONCodec[T any] struct {
	Version          int
	Migrations       map[int]JSONMigration
	AllowUnversioned bool
}

type jsonEnvelope struct {
	Version *int            `json:"schemaVersion"`
	State   json.RawMessage `json:"state"`
}

func (c VersionedJSONCodec[T]) Encode(value T) ([]byte, error) {
	if c.Version < 1 {
		return nil, ErrInvalidPersistence
	}
	state, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return json.MarshalIndent(jsonEnvelope{Version: &c.Version, State: state}, "", "  ")
}

func (c VersionedJSONCodec[T]) Decode(data []byte) (T, error) {
	var zero T
	if c.Version < 1 {
		return zero, ErrInvalidPersistence
	}
	// Decode raw first so malformed legacy JSON cannot reach a migration.
	var raw json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return zero, err
	}
	version, state := 0, raw
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(raw, &fields)
	if _, versioned := fields["schemaVersion"]; versioned {
		var envelope jsonEnvelope
		if err := json.Unmarshal(raw, &envelope); err != nil {
			return zero, fmt.Errorf("nodra: decode version envelope: %w", err)
		}
		if envelope.Version == nil || envelope.State == nil {
			return zero, fmt.Errorf("%w: incomplete version envelope", ErrInvalidPersistence)
		}
		version, state = *envelope.Version, envelope.State
	} else if !c.AllowUnversioned {
		return zero, fmt.Errorf("%w: expected version envelope", ErrInvalidPersistence)
	}
	if version < 0 || version > c.Version {
		return zero, fmt.Errorf("%w: got %d, supported through %d", ErrUnsupportedVersion, version, c.Version)
	}
	for version < c.Version {
		migrate := c.Migrations[version]
		if migrate == nil {
			return zero, fmt.Errorf("%w: %d -> %d", ErrMissingMigration, version, version+1)
		}
		next, err := migrate(state)
		if err != nil {
			return zero, fmt.Errorf("nodra: migrate %d -> %d: %w", version, version+1, err)
		}
		if !json.Valid(next) {
			return zero, fmt.Errorf("%w: migration %d -> %d returned invalid JSON", ErrInvalidPersistence, version, version+1)
		}
		state = next
		version++
	}
	var value T
	if err := json.Unmarshal(state, &value); err != nil {
		return zero, err
	}
	return value, nil
}
