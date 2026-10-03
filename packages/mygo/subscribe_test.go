package mygo

import (
	"testing"

	"github.com/yldm-tech/mygo-nodra"
)

func TestSubscribeInvalidationHandlesNilInputs(t *testing.T) {
	var store *nodra.Store[int]
	if stop := SubscribeInvalidation(store, nil); stop == nil {
		t.Fatal("nil store returned nil unsubscribe")
	}
}
