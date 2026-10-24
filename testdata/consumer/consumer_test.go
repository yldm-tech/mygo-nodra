package consumer_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/yldm-tech/nodra"
	"github.com/yldm-tech/nodra/devtools"
)

// Compile generic inference and exercise exported contracts without access to internal symbols or the repository's module/workspace settings.
func TestPublicAPI(t *testing.T) {
	source := nodra.New(1)
	defer source.Close()
	var view nodra.Reader[int] = source.ReadOnly()
	var observable nodra.Observable[int] = view
	derived := nodra.Derive(observable, func(v int) int { return v * 2 }, func(a, b int) bool { return a == b })
	defer derived.Close()
	combined := nodra.Combine(observable, derived, func(a, b int) int { return a + b }, func(a, b int) bool { return a == b })
	defer combined.Close()
	if combined.Get() != 3 {
		t.Fatal("combined public observable")
	}
	action, err := nodra.NewAsyncAction(source, "consumer", func(context.Context, int, func(float64)) (int, error) { return 2, nil }, func(s *int, v int) error { *s = v; return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer action.Close(context.Background())
	task, err := action.Run(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := task.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	if task.Status().Get().Phase != nodra.TaskSucceeded {
		t.Fatal("task status")
	}
	storage, err := (nodra.FileStorage{Path: filepath.Join(t.TempDir(), "state.json")}).WithConflictDetection(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	p := nodra.NewPersistence(source, storage, nodra.JSONCodec[int]{})
	writer, err := p.AutoSaveAsync(context.Background(), nodra.AsyncOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if state := writer.Status().Get(); !state.Closed || state.Pending || !state.HasSaved {
		t.Fatalf("persistence status: %+v", state)
	}
	var _ nodra.TaskMetrics = task.Status().Get().Metrics
	inspector := devtools.New(devtools.Options{RedactMetadata: func(_ string, m devtools.Metadata) (devtools.Metadata, error) { return m, nil }})
	defer inspector.Close()
	stop, err := devtools.Attach(inspector, "consumer", view)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	if len(inspector.Snapshot().Stores) != 1 || source.Get() != 2 {
		t.Fatal("public store/inspector")
	}
}
