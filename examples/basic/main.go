package main

import (
	"fmt"
	"log"

	"github.com/yldm-tech/nodra"
)

type AppState struct {
	Count  int
	Filter string
}

func main() {
	store := nodra.New(AppState{Filter: "all"})
	store.Subscribe(func(snapshot nodra.Snapshot[AppState]) {
		fmt.Printf("version=%d state=%+v\n", snapshot.Version, snapshot.Value)
	})
	if err := store.Update(func(state *AppState) { state.Count++ }); err != nil {
		log.Fatal(err)
	}
	err := store.Batch(func() {
		if err := store.Update(func(state *AppState) { state.Count++ }); err != nil {
			log.Fatal(err)
		}
		if err := store.Update(func(state *AppState) { state.Filter = "active" }); err != nil {
			log.Fatal(err)
		}
	})
	if err != nil {
		log.Fatal(err)
	}
}
