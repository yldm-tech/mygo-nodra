// Example mygo demonstrates Nodra driving a native MyGo view.
package main

import (
	"log"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	nodra "github.com/yldm-tech/mygo-nodra"
	mygostore "github.com/yldm-tech/mygo-nodra/integrations/mygo"
)

type appState struct{ Count int }

func main() {
	store := nodra.New(appState{})
	mygo.App.WhenReady(func() {
		window := mygo.NewWindow(mygo.WindowOptions{Title: "Nodra + MyGo", Width: 420, Height: 280, Content: ui.View(func(c *ui.Context) {
			state := store.Get()
			ui.Column(c).Fill().Center().Gap(16).Children(func() {
				ui.Text(c, "Nodra + MyGo").FontSize(22).Bold()
				ui.Textf(c, "Count: %d", state.Count).FontSize(32)
				ui.Row(c).Gap(8).Children(func() {
					if ui.Button(c, "−").Width(48).Clicked() {
						_ = store.Update(func(value *appState) { value.Count-- })
					}
					if ui.Button(c, "Reset").Clicked() {
						_ = store.Set(appState{})
					}
					if ui.PrimaryButton(c, "+").Width(48).Clicked() {
						_ = store.Update(func(value *appState) { value.Count++ })
					}
				})
			})
		})})
		stop := mygostore.SubscribeInvalidation(store, window)
		window.OnClosed(func() { stop() })
	})
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}
