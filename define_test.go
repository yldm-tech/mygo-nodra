package nodra

import (
	"errors"
	"testing"
)

type profileState struct{ Name string }

func TestDefineCreatesNamedIndependentStores(t *testing.T) {
	definition := Define("profile", func() profileState { return profileState{Name: "new"} })
	first := definition.New()
	second := definition.New()
	if definition.ID() != "profile" || first.ID() != "profile" || second.ID() != "profile" {
		t.Fatalf("ids=%q,%q,%q", definition.ID(), first.ID(), second.ID())
	}
	_ = first.Update(func(state *profileState) { state.Name = "first" })
	if second.Get().Name != "new" {
		t.Fatalf("second=%+v", second.Get())
	}
}

func TestActionHooksSeeSuccessAndError(t *testing.T) {
	store := Define("counter", func() int { return 0 }).New()
	var before, after, failed int
	stop := store.SubscribeActionHooks(ActionHooks{Before: func(ActionEvent) { before++ }, After: func(ActionEvent) { after++ }, Error: func(ActionEvent) { failed++ }})
	_ = store.Do("increment", func(value *int) error { *value++; return nil })
	want := errors.New("bad")
	if err := store.Do("fail", func(*int) error { return want }); !errors.Is(err, want) {
		t.Fatal(err)
	}
	stop()
	if before != 2 || after != 1 || failed != 1 {
		t.Fatalf("hooks=%d,%d,%d", before, after, failed)
	}
}
