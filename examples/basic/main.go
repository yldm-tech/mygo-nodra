package main

import (
	"fmt"
	"github.com/yldm-tech/mygo-nodra"
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
	store.Update(func(state *AppState) { state.Count++ })
	store.Batch(func() {
		store.Update(func(state *AppState) { state.Count++ })
		store.Update(func(state *AppState) { state.Filter = "active" })
	})
}
