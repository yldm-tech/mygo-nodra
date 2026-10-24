package nodra

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/gofrs/flock"
)

func checkedStorage(t *testing.T, file FileStorage) *CheckedFileStorage {
	t.Helper()
	storage, err := file.WithConflictDetection(context.Background())
	requireNoError(t, err)
	return storage
}

func TestCheckedFileStorageConflictReloadAndExternalEdit(t *testing.T) {
	ctx := context.Background()
	file := FileStorage{Path: filepath.Join(t.TempDir(), "state.json"), Backup: true}
	first, second := checkedStorage(t, file), checkedStorage(t, file)
	requireNoError(t, first.Save(ctx, []byte("one")))
	for range 2 {
		var conflict *FileConflictError
		err := second.Save(ctx, []byte("two"))
		if !errors.Is(err, ErrFileConflict) || !errors.As(err, &conflict) || conflict.Expected.Exists || !conflict.Actual.Exists || conflict.Actual.SHA256 != sha256.Sum256([]byte("one")) {
			t.Fatalf("conflict: %+v, %v", conflict, err)
		}
	}
	for _, path := range []string{file.Path, file.Path + ".bak"} {
		raw, err := os.ReadFile(path)
		requireNoError(t, err)
		if string(raw) != "one" {
			t.Fatal("rejected save changed primary or backup")
		}
	}
	raw, err := second.Load(ctx)
	requireNoError(t, err)
	if string(raw) != "one" {
		t.Fatal("explicit load missed external content")
	}
	requireNoError(t, second.Save(ctx, []byte("two")))
	if !errors.Is(first.Save(ctx, []byte("old")), ErrFileConflict) {
		t.Fatal("first writer silently overwrote second")
	}
	requireNoError(t, os.WriteFile(file.Path, []byte("external"), 0600))
	if !errors.Is(second.Save(ctx, []byte("three")), ErrFileConflict) {
		t.Fatal("external edit was not detected")
	}
	raw, err = second.LoadBackup(ctx)
	requireNoError(t, err)
	if string(raw) != "two" || !errors.Is(second.Save(ctx, []byte("three")), ErrFileConflict) {
		t.Fatal("backup load acknowledged an external primary change")
	}
}

func TestCheckedFileStorageMissingEmptyAndRemovedFiles(t *testing.T) {
	ctx := context.Background()
	file := FileStorage{Path: filepath.Join(t.TempDir(), "nested", "state.json")}
	storage := checkedStorage(t, file)
	_, err := storage.Load(ctx)
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing load: %v", err)
	}
	requireNoError(t, file.Save(ctx, nil))
	if !errors.Is(storage.Save(ctx, []byte("new")), ErrFileConflict) {
		t.Fatal("empty file was confused with missing file")
	}
	_, err = storage.Load(ctx)
	requireNoError(t, err)
	requireNoError(t, os.Remove(file.Path))
	if !errors.Is(storage.Save(ctx, []byte("new")), ErrFileConflict) {
		t.Fatal("external deletion was not detected")
	}
	_, err = storage.Load(ctx)
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	requireNoError(t, storage.Save(ctx, []byte("new")))
}

func TestCheckedFileStorageBackupFailureAcknowledgesPrimary(t *testing.T) {
	ctx := context.Background()
	file := FileStorage{Path: filepath.Join(t.TempDir(), "state.json"), Backup: true}
	storage := checkedStorage(t, file)
	requireNoError(t, os.Mkdir(file.Path+".bak", 0700))
	if err := storage.Save(ctx, []byte("one")); err == nil || errors.Is(err, ErrFileConflict) {
		t.Fatalf("backup failure: %v", err)
	}
	raw, err := os.ReadFile(file.Path)
	requireNoError(t, err)
	if string(raw) != "one" {
		t.Fatal("primary not saved")
	}
	requireNoError(t, os.Remove(file.Path+".bak"))
	requireNoError(t, storage.Save(ctx, []byte("two")))
	raw, err = storage.LoadBackup(ctx)
	requireNoError(t, err)
	if string(raw) != "two" {
		t.Fatal("retry backup not saved")
	}
}

func TestCheckedFileStorageConcurrentInstancesAndOneInstance(t *testing.T) {
	ctx := context.Background()
	file := FileStorage{Path: filepath.Join(t.TempDir(), "state.json")}
	first, second := checkedStorage(t, file), checkedStorage(t, file)
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, storage := range []*CheckedFileStorage{first, second} {
		go func() { <-start; results <- storage.Save(ctx, []byte("first write")) }()
	}
	close(start)
	successes, conflicts := 0, 0
	for range 2 {
		switch err := <-results; {
		case err == nil:
			successes++
		case errors.Is(err, ErrFileConflict):
			conflicts++
		default:
			t.Fatal(err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("successes=%d conflicts=%d", successes, conflicts)
	}
	_, err := first.Load(ctx)
	requireNoError(t, err)
	var wg sync.WaitGroup
	for n := range 8 {
		wg.Go(func() {
			if err := first.Save(ctx, fmt.Appendf(nil, "write %d", n)); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
}

func TestCheckedFileStorageAsyncConflictStaysPendingUntilReconciled(t *testing.T) {
	ctx := context.Background()
	file := FileStorage{Path: filepath.Join(t.TempDir(), "state.json")}
	requireNoError(t, file.Save(ctx, []byte("1")))
	storage := checkedStorage(t, file)
	store := New(2)
	p := NewPersistence(store, storage, JSONCodec[int]{})
	writer, err := p.AutoSaveAsync(ctx, AsyncOptions{Debounce: time.Hour})
	requireNoError(t, err)
	defer func() { _ = writer.Close(ctx) }()
	requireNoError(t, file.Save(ctx, []byte("3")))
	if !errors.Is(writer.Flush(ctx), ErrFileConflict) {
		t.Fatal("async save overwrote an external edit")
	}
	// The error stream is sent after status publication, so this also synchronizes the status assertion.
	if !errors.Is(<-writer.Errors(), ErrFileConflict) {
		t.Fatal("missing background conflict")
	}
	if state := writer.Status().Get(); !state.Pending || state.HasSaved || state.Error == "" {
		t.Fatalf("conflict status: %+v", state)
	}
	// The application explicitly accepts current primary content, then applies its merge.
	requireNoError(t, p.Load(ctx))
	if store.Get() != 3 {
		t.Fatal("conflicting content not loaded")
	}
	requireNoError(t, store.Set(4))
	requireNoError(t, writer.Flush(ctx))
	requireNoError(t, writer.Close(ctx))
	raw, err := file.Load(ctx)
	requireNoError(t, err)
	if string(raw) != "4" || writer.Status().Get().Pending {
		t.Fatal("reconciled state not acknowledged")
	}
}

func TestCheckedFileStorageRejectsInvalidPathsAndCanceledContexts(t *testing.T) {
	ctx := context.Background()
	if _, err := (FileStorage{}).WithConflictDetection(ctx); !errors.Is(err, ErrInvalidPersistence) {
		t.Fatal(err)
	}
	var zero CheckedFileStorage
	if err := zero.Save(ctx, nil); !errors.Is(err, ErrInvalidPersistence) {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "not-created", "state.json")
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := (FileStorage{Path: path}).WithConflictDetection(canceled); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Dir(path)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("canceled constructor created a directory")
	}
	directory := t.TempDir()
	if _, err := (FileStorage{Path: directory}).WithConflictDetection(ctx); !errors.Is(err, ErrInvalidPersistence) {
		t.Fatalf("directory accepted: %v", err)
	}
	// Windows environments may not grant the privilege required to create a symlink.
	t.Run("symlink", func(t *testing.T) {
		dir := t.TempDir()
		target, link := filepath.Join(dir, "target"), filepath.Join(dir, "link")
		requireNoError(t, os.WriteFile(target, []byte("target"), 0600))
		if err := os.Symlink(target, link); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if _, err := (FileStorage{Path: link}).WithConflictDetection(ctx); !errors.Is(err, ErrInvalidPersistence) {
			t.Fatalf("symlink accepted: %v", err)
		}
		storage := checkedStorage(t, FileStorage{Path: filepath.Join(dir, "state")})
		requireNoError(t, os.Symlink(target, storage.file.Path))
		if err := storage.Save(ctx, []byte("new")); !errors.Is(err, ErrInvalidPersistence) {
			t.Fatalf("later symlink accepted: %v", err)
		}
	})
}

// Run as a subprocess to verify real OS lock coordination, not only shared Go memory.
func TestCheckedFileStorageProcessHelper(t *testing.T) {
	mode := os.Getenv("NODRA_FILE_CONFLICT_HELPER")
	if mode == "" {
		return
	}
	path := os.Getenv("NODRA_FILE_CONFLICT_PATH")
	storage, err := (FileStorage{Path: path}).WithConflictDetection(context.Background())
	requireNoError(t, err)
	if mode == "hold" {
		lock := flock.New(storage.file.Path + ".lock")
		requireNoError(t, lock.Lock())
		defer func() { _ = lock.Close() }()
		fmt.Println("ready")
		_, err := bufio.NewReader(os.Stdin).ReadString('\n')
		requireNoError(t, err)
		return
	}
	fmt.Println("ready")
	_, err = bufio.NewReader(os.Stdin).ReadString('\n')
	requireNoError(t, err)
	err = storage.Save(context.Background(), bytes.Repeat([]byte(mode), 8192))
	switch {
	case err == nil:
		fmt.Println("saved")
	case errors.Is(err, ErrFileConflict):
		fmt.Println("conflict")
	default:
		t.Fatal(err)
	}
}

func startConflictProcess(t *testing.T, path, mode string) (*exec.Cmd, io.WriteCloser, *bufio.Scanner) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCheckedFileStorageProcessHelper$", "-test.timeout=15s")
	command.Env = append(os.Environ(), "NODRA_FILE_CONFLICT_HELPER="+mode, "NODRA_FILE_CONFLICT_PATH="+path)
	input, err := command.StdinPipe()
	requireNoError(t, err)
	output, err := command.StdoutPipe()
	requireNoError(t, err)
	command.Stderr = os.Stderr
	requireNoError(t, command.Start())
	t.Cleanup(func() {
		_ = input.Close()
		if command.ProcessState == nil {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	})
	lines := bufio.NewScanner(output)
	if !lines.Scan() || lines.Text() != "ready" {
		t.Fatalf("child not ready: %q, %v", lines.Text(), lines.Err())
	}
	return command, input, lines
}

func TestCheckedFileStorageCrossProcessConflicts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	first, firstInput, firstOutput := startConflictProcess(t, path, "one")
	second, secondInput, secondOutput := startConflictProcess(t, path, "two")
	_, err := io.WriteString(firstInput, "save\n")
	requireNoError(t, err)
	_, err = io.WriteString(secondInput, "save\n")
	requireNoError(t, err)
	if !firstOutput.Scan() || !secondOutput.Scan() {
		t.Fatal("child missing outcome")
	}
	a, b := firstOutput.Text(), secondOutput.Text()
	switch {
	case a == "saved" && b == "conflict", a == "conflict" && b == "saved":
	default:
		t.Fatalf("cross-process outcomes: %s, %s", a, b)
	}
	requireNoError(t, first.Wait())
	requireNoError(t, second.Wait())
	raw, err := os.ReadFile(path)
	requireNoError(t, err)
	if !bytes.Equal(raw, bytes.Repeat([]byte("one"), 8192)) && !bytes.Equal(raw, bytes.Repeat([]byte("two"), 8192)) {
		t.Fatal("partial or unrecognized primary file")
	}
}

func TestCheckedFileStorageCrossProcessLockWaitHonorsContext(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.json")
	storage := checkedStorage(t, FileStorage{Path: path})
	child, input, _ := startConflictProcess(t, path, "hold")
	deadline, cancel := context.WithTimeout(ctx, 40*time.Millisecond)
	defer cancel()
	if err := storage.Save(deadline, []byte("blocked")); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("lock wait: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("blocked save modified primary")
	}
	_, err := io.WriteString(input, "release\n")
	requireNoError(t, err)
	requireNoError(t, child.Wait())
	requireNoError(t, storage.Save(ctx, []byte("after release")))
}
