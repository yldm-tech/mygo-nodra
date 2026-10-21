package nodra_test

import (
	"fmt"

	"github.com/yldm-tech/nodra"
)

func ExampleStore_Update() {
	store := nodra.New(0)
	store.Subscribe(func(snapshot nodra.Snapshot[int]) {
		fmt.Println(snapshot.Value, snapshot.Version)
	})
	if err := store.Update(func(value *int) { *value = 42 }); err != nil {
		fmt.Println(err)
	}
	// Output: 42 1
}
