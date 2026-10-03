package nodra

import (
	"reflect"
	"testing"
)

type apiState struct {
	Count int
	Name  string
}

func TestCreatePatchResetAndStateAliases(t *testing.T) {
	store := Create(apiState{Count: 1, Name: "initial"})
	if store.State().Count != 1 {
		t.Fatal("initial state")
	}
	if err := store.Patch(func(state *apiState) { state.Count++; state.Name = "patched" }); err != nil {
		t.Fatal(err)
	}
	if store.State().Count != 2 || store.State().Name != "patched" {
		t.Fatalf("state=%+v", store.State())
	}
	if err := store.Reset(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(store.State(), apiState{Count: 1, Name: "initial"}) {
		t.Fatalf("reset=%+v", store.State())
	}
}

func TestDerivedGetterCachesAndSubscribes(t *testing.T) {
	store := Create(apiState{Count: 2})
	calls := 0
	derived := Derive(store, func(state apiState) int { calls++; return state.Count * 2 }, func(a, b int) bool { return a == b })
	if derived.Get() != 4 || derived.Get() != 4 || calls != 1 {
		t.Fatalf("value=%d calls=%d", derived.Get(), calls)
	}
	var values []int
	stop := derived.Subscribe(func(value int) { values = append(values, value) })
	_ = store.Patch(func(state *apiState) { state.Name = "same getter" })
	_ = store.Patch(func(state *apiState) { state.Count = 3 })
	stop()
	if !reflect.DeepEqual(values, []int{6}) {
		t.Fatalf("values=%v", values)
	}
}

func TestSelectorWithPrevious(t *testing.T) {
	store := Create(apiState{Count: 1})
	var got [][2]int
	stop := SubscribeSelectorWith(store, func(state apiState) int { return state.Count }, func(current, previous int) { got = append(got, [2]int{current, previous}) }, SelectorOptions[int]{Immediate: true})
	_ = store.Set(apiState{Count: 2})
	stop()
	if !reflect.DeepEqual(got, [][2]int{{1, 0}, {2, 1}}) {
		t.Fatalf("got=%v", got)
	}
}

func TestActionsAndMutationMetadata(t *testing.T) {
	store := Create(apiState{})
	var actionNames []string
	stopActions := store.SubscribeActions(func(event ActionEvent) { actionNames = append(actionNames, event.Name) })
	var mutations []Mutation[apiState]
	stopMutations := store.SubscribeMutations(func(event Mutation[apiState]) { mutations = append(mutations, event) })
	if err := store.Do("increment", func(state *apiState) error { state.Count++; return nil }); err != nil {
		t.Fatal(err)
	}
	stopActions()
	stopMutations()
	if !reflect.DeepEqual(actionNames, []string{"increment"}) {
		t.Fatalf("actions=%v", actionNames)
	}
	if len(mutations) != 1 || mutations[0].Type != MutationAction || mutations[0].Name != "increment" || mutations[0].Previous.Count != 0 || mutations[0].Value.Count != 1 {
		t.Fatalf("mutations=%+v", mutations)
	}
}

func TestNamedActionFactory(t *testing.T) {
	store := Create(0)
	increment := store.Action("increment", func(value *int) error { *value++; return nil })
	if err := increment(); err != nil || store.Get() != 1 {
		t.Fatalf("value=%d err=%v", store.Get(), err)
	}
}
