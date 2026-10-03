package nodra_test

import (
	"fmt"

	"github.com/yldm-tech/mygo-nodra"
)

func ExampleStore_Update() {
	store := nodra.New(0)
	store.Subscribe(func(snapshot nodra.Snapshot[int]) {
		fmt.Println(snapshot.Value, snapshot.Version)
	})
	store.Update(func(value *int) { *value = 42 })
	// Output: 42 1
}
