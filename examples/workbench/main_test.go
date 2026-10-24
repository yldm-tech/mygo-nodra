package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/yldm-tech/nodra"
)

func wait(t *testing.T, task *nodra.Task) error {
	t.Helper()
	if task == nil {
		t.Fatal("action was not started")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return task.Wait(ctx)
}

func makeFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("content"), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestSearchFilesUsesRealDirectoryAndCancellation(t *testing.T) {
	root := t.TempDir()
	makeFile(t, filepath.Join(root, "Guide.md"))
	makeFile(t, filepath.Join(root, "sub", "guide.txt"))
	makeFile(t, filepath.Join(root, ".hidden", "guide.md"))
	makeFile(t, filepath.Join(root, "other.txt"))
	result, err := searchFiles(context.Background(), searchInput{Settings: settings{Root: root}, Query: "GUIDE"}, func(float64) {})
	if err != nil || !slices.Equal(result.Files, []string{"Guide.md", filepath.Join("sub", "guide.txt")}) {
		t.Fatalf("search: %+v, %v", result, err)
	}
	result, err = searchFiles(context.Background(), searchInput{Settings: settings{Root: root, IncludeHidden: true}, Query: "guide"}, func(float64) {})
	if err != nil || len(result.Files) != 3 {
		t.Fatalf("hidden search: %+v, %v", result, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := searchFiles(ctx, searchInput{Settings: settings{Root: root}, Query: "guide"}, func(float64) {}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
}

func TestWorkbenchFormValidationCancelSaveAndReload(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(t.TempDir(), "settings.json")
	app, err := newWorkbench(root, path)
	if err != nil {
		t.Fatal(err)
	}
	defer app.close()
	app.draft.Root = ""
	tester := ui.NewTester(app.view, 960, 640)
	if err := tester.Click("Save settings"); err != nil {
		t.Fatal(err)
	}
	if app.notice == "" || app.settings.Get().Root != root {
		t.Fatal("empty folder bypassed validation")
	}
	if err := tester.Click("Cancel edits"); err != nil {
		t.Fatal(err)
	}
	if app.draft != app.settings.Get() || app.notice != "" {
		t.Fatal("cancel did not restore draft")
	}
	app.draft.Root = filepath.Join(root, "missing")
	tester.Frame()
	if err := tester.Click("Save settings"); err != nil {
		t.Fatal(err)
	}
	if err := wait(t, app.saveRun); err == nil {
		t.Fatal("invalid folder saved")
	}
	tester.Frame()
	if app.settings.Get().Root != root || app.draft.Root == root || app.save.Status().Get().Error == "" {
		t.Fatal("failed save lost draft or changed preferences")
	}
	app.draft = settings{Root: root, IncludeHidden: true}
	tester.Frame()
	if err := tester.Click("Save settings"); err != nil {
		t.Fatal(err)
	}
	if err := wait(t, app.saveRun); err != nil {
		t.Fatal(err)
	}
	reloaded, err := newWorkbench("unused", path)
	if err != nil {
		t.Fatal(err)
	}
	defer reloaded.close()
	if reloaded.settings.Get() != app.draft {
		t.Fatal("saved preferences did not reload")
	}
}

func TestWorkbenchStorageFailureAndSearchButton(t *testing.T) {
	root := t.TempDir()
	makeFile(t, filepath.Join(root, "guide.md"))
	// A regular file in place of the parent folder produces a real storage failure on every platform.
	blocked := filepath.Join(t.TempDir(), "blocked")
	makeFile(t, blocked)
	app, err := newWorkbench(root, filepath.Join(blocked, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer app.close()
	app.draft.IncludeHidden = true
	tester := ui.NewTester(app.view, 960, 640)
	if err := tester.Click("Save settings"); err != nil {
		t.Fatal(err)
	}
	if err := wait(t, app.saveRun); err == nil {
		t.Fatal("storage failure was hidden")
	}
	if app.settings.Get().IncludeHidden {
		t.Fatal("failed save changed preferences")
	}
	if err := tester.Click("Cancel edits"); err != nil {
		t.Fatal(err)
	}
	app.query = "guide"
	tester.Frame()
	if err := tester.Click("Search"); err != nil {
		t.Fatal(err)
	}
	if err := wait(t, app.searchRun); err != nil {
		t.Fatal(err)
	}
	tester.Frame()
	if !tester.HasText("guide.md") {
		t.Fatalf("results missing: %v", tester.Texts())
	}
}
