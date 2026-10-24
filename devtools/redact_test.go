package devtools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/yldm-tech/nodra"
)

func TestMetadataRedactionBeforeAllRetentionAndHTTPExports(t *testing.T) {
	var i *Inspector
	i = New(Options{TaskProgressInterval: time.Hour, RedactMetadata: func(_ string, m Metadata) (Metadata, error) {
		// Redaction callbacks may inspect the inspector without taking a lock recursively.
		_ = i.Snapshot()
		m.Name = strings.ReplaceAll(m.Name, "private-name", "masked-name")
		m.Key = strings.ReplaceAll(m.Key, "private-key", "masked-key")
		m.Error = strings.ReplaceAll(m.Error, "private-error", "masked-error")
		return m, nil
	}})
	defer func() { _ = i.Close() }()
	store := nodra.New(0)
	entered, release := make(chan struct{}), make(chan struct{})
	action, err := nodra.NewAsyncActionWithOptions(store, "private-name", func(_ context.Context, _ int, report func(float64)) (int, error) {
		report(.1)
		report(.2)
		close(entered)
		<-release
		return 0, errors.New("private-error")
	}, func(*int, int) error { return nil }, nodra.ActionOptions[int]{Key: func(int) string { return "private-key" }})
	mustUpdate(t, err)
	defer func() { _ = action.Close(context.Background()) }()
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	task, err := action.Run(context.Background(), 0)
	mustUpdate(t, err)
	<-entered
	// Attach during work exercises TaskSnapshot reconciliation, not only new traces.
	mustAttach(t, i, "public-label", store)
	assertMasked := func() {
		t.Helper()
		for _, path := range []string{"/api/snapshot", "/api/changes", "/api/events"} {
			response := request(i, "GET", path, "")
			if response.Code != 200 || strings.Contains(response.Body.String(), "private-") {
				t.Fatalf("metadata escaped through %s: %s", path, response.Body.String())
			}
		}
		for _, node := range i.stores {
			for _, progress := range node.progress {
				raw, err := json.Marshal(progress.pending)
				mustUpdate(t, err)
				if strings.Contains(string(raw), "private-") {
					t.Fatalf("pending metadata retained: %s", raw)
				}
			}
		}
	}
	assertMasked()
	// Exercise actual pending progress and mutation/action metadata on the attached node.
	i.recordTrace(i.stores["public-label"], nodra.ActionTrace{ID: 99, Name: "private-name", Key: "private-key", Phase: nodra.TaskRunning, TaskVersion: 1})
	i.recordTrace(i.stores["public-label"], nodra.ActionTrace{ID: 99, Name: "private-name", Key: "private-key", Phase: nodra.TaskRunning, TaskVersion: 2})
	mustUpdate(t, store.Do("private-name", func(s *int) error { *s = 1; return nil }))
	if err := store.Do("private-name", func(*int) error { return errors.New("private-error") }); err == nil {
		t.Fatal("missing source error")
	}
	assertMasked()
	mustUpdate(t, i.SetCapture(true, 0))
	mustUpdate(t, i.SetCapture(false, 0))
	assertMasked()
	close(release)
	if err := task.Wait(context.Background()); err == nil || err.Error() != "private-error" {
		t.Fatalf("source error was changed: %v", err)
	}
	assertMasked()
	var found bool
	for _, event := range i.Snapshot().Tasks {
		if event.RunID == task.ID && event.ActionID != 99 && event.Phase == nodra.TaskFailed {
			found = event.Name == "masked-name" && event.Key == "masked-key" && event.Error == "masked-error"
		}
	}
	if !found {
		t.Fatal("redacted terminal task absent")
	}
}

type secretEncodingError struct{}

func (secretEncodingError) MarshalJSON() ([]byte, error) { return nil, errors.New("private-error") }

func TestMaskingEncodingErrorsCannotEnableRestoration(t *testing.T) {
	i := New(Options{AllowRestore: true, RedactMetadata: func(_ string, m Metadata) (Metadata, error) {
		m.Error = ""
		return m, nil
	}})
	defer func() { _ = i.Close() }()
	store := nodra.New(secretEncodingError{})
	mustAttach(t, i, "public", store)
	mustUpdate(t, store.Set(secretEncodingError{}))
	if i.Snapshot().Stores[0].Restorable {
		t.Fatal("masked encoding error made invalid state restorable")
	}
	if !errors.Is(i.Apply("public", json.RawMessage(`{}`)), ErrReadOnly) {
		t.Fatal("restore accepted an uninspectable state")
	}
}

func TestMetadataRedactorFailureAndPanicFailClosed(t *testing.T) {
	for _, panicCallback := range []bool{false, true} {
		t.Run(map[bool]string{false: "error", true: "panic"}[panicCallback], func(t *testing.T) {
			i := New(Options{RedactMetadata: func(string, Metadata) (Metadata, error) {
				if panicCallback {
					panic("private-error")
				}
				return Metadata{Name: "private-result"}, errors.New("private-error")
			}})
			defer func() { _ = i.Close() }()
			store := nodra.New(secretEncodingError{})
			mustAttach(t, i, "public", store)
			mustUpdate(t, store.Set(secretEncodingError{}))
			i.recordTrace(i.stores["public"], nodra.ActionTrace{ID: 7, Name: "private-name", Key: "private-key", Phase: nodra.TaskFailed, Err: errors.New("private-error")})
			raw, err := json.Marshal(i.Snapshot())
			mustUpdate(t, err)
			if strings.Contains(string(raw), "private-") || !strings.Contains(string(raw), "metadata redaction failed") {
				t.Fatalf("redactor failure leaked text: %s", raw)
			}
			if i.Snapshot().Tasks[0].ActionID != 7 {
				t.Fatal("redaction changed task correlation")
			}
		})
	}
}
