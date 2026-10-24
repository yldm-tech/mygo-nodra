// Command persistent demonstrates cloned state, schema migration and write-behind
// persistence. Run with -file pointing to a new file or a legacy JSON word map.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"maps"
	"time"

	"github.com/yldm-tech/nodra"
)

type wordState struct {
	Words map[string]int `json:"words"`
}

func run(path string) (result error) {
	store := nodra.NewWithClone(wordState{Words: map[string]int{}}, func(v wordState) wordState {
		v.Words = maps.Clone(v.Words)
		return v
	})
	defer func() { _ = store.Close() }()
	codec := nodra.VersionedJSONCodec[wordState]{
		Version: 1, AllowUnversioned: true,
		Migrations: map[int]nodra.JSONMigration{
			0: func(raw json.RawMessage) (json.RawMessage, error) {
				var words map[string]int
				if err := json.Unmarshal(raw, &words); err != nil {
					return nil, err
				}
				return json.Marshal(wordState{Words: words})
			},
		},
	}
	persistence := nodra.NewPersistence(store, nodra.FileStorage{Path: path}, codec)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := persistence.Load(ctx); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	writer, err := persistence.AutoSaveAsync(ctx, nodra.AsyncOptions{Debounce: 25 * time.Millisecond})
	if err != nil {
		return err
	}
	// Drain before canceling the parent or closing the store. Preserve any
	// shutdown write error so the caller can surface it to the user.
	defer func() { result = errors.Join(result, writer.Close(ctx)) }()
	if err := store.Update(func(v *wordState) {
		if v.Words == nil {
			v.Words = map[string]int{}
		}
		v.Words["hello"]++
	}); err != nil {
		return err
	}
	if err := writer.Flush(ctx); err != nil {
		return err
	}
	fmt.Printf("saved version %d: hello=%d\n", store.Version(), store.Get().Words["hello"])
	return nil
}

func main() {
	path := flag.String("file", "state.json", "path to the state file")
	flag.Parse()
	if err := run(*path); err != nil {
		log.Fatal(err)
	}
}
