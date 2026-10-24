package main

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/yldm-tech/nodra"
)

func TestPreferencesAndCounterCommitTheirOwnFields(t *testing.T) {
	app := &counter{store: nodra.New(appState{Count: 7})}
	defer func() { _ = app.store.Close() }()
	preferences := ui.NewTester(app.preferencesView, 420, 200)
	if err := preferences.Click("Increment by two"); err != nil {
		t.Fatal(err)
	}
	if got := app.store.Get(); got != (appState{Count: 7, DoubleStep: true}) {
		t.Fatalf("preference edit replaced unrelated state: %+v", got)
	}
	view := ui.NewTester(app.view, 420, 360)
	for _, step := range []struct {
		button string
		count  int
	}{{"+", 9}, {"Reset", 0}, {"−", -2}} {
		if err := view.Click(step.button); err != nil {
			t.Fatal(err)
		}
		if got := app.store.Get(); got.Count != step.count || !got.DoubleStep {
			t.Fatalf("after %s: %+v, want count %d and preserved preference", step.button, got, step.count)
		}
	}
}

func TestBackgroundButtonUpdatesSharedState(t *testing.T) {
	app := &counter{store: nodra.New(appState{})}
	defer func() { _ = app.store.Close() }()
	changed := make(chan struct{}, 1)
	stop := app.store.Subscribe(func(s nodra.Snapshot[appState]) {
		if s.Value.Count == 1 {
			changed <- struct{}{}
		}
	})
	defer stop()
	view := ui.NewTester(app.view, 420, 360)
	if err := view.Click("Increment in background"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-changed:
	case <-time.After(5 * time.Second):
		t.Fatal("background action did not publish")
	}
	view.Frame()
	if !view.HasText("Count: 1") {
		t.Fatalf("view did not read the new snapshot: %v", view.Texts())
	}
}
