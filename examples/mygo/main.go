// Example mygo demonstrates two native windows sharing business state with independent redraw subscriptions.
package main

import (
	"log"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	nodra "github.com/yldm-tech/nodra"
)

type appState struct {
	Count      int
	DoubleStep bool
}

type counter struct {
	store   *nodra.Store[appState]
	options *mygo.Window
}

func main() {
	store := nodra.New(appState{})
	defer func() { _ = store.Close() }()
	app := &counter{store: store}
	mygo.App.WhenReady(func() {
		window := mygo.NewWindow(mygo.WindowOptions{Title: "Nodra + MyGo", Width: 420, Height: 360, Content: ui.View(app.view)})
		// Preferences can change without rebuilding the counter. Button actions read the current preference inside Update.
		nodra.InvalidateSelector(store, window, func(s appState) int { return s.Count })
	})
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}

func (app *counter) view(c *ui.Context) {
	count := app.store.Get().Count
	ui.Column(c).Fill().Center().Padding(20).Gap(16).Children(func() {
		ui.Text(c, "Nodra + MyGo").FontSize(22).Bold()
		ui.Textf(c, "Count: %d", count).FontSize(32)
		ui.Row(c).Gap(8).Children(func() {
			ui.Button(c, "−").Width(48).OnClick(func() { app.change(-1) })
			ui.Button(c, "Reset").OnClick(func() { app.update(func(s *appState) { s.Count = 0 }) })
			ui.PrimaryButton(c, "+").Width(48).OnClick(func() { app.change(1) })
		})
		ui.Button(c, "Increment in background").OnClick(func() { go app.change(1) })
		ui.Button(c, "Preferences…").OnClick(app.openPreferences)
	})
}

func (app *counter) change(direction int) {
	app.update(func(s *appState) {
		step := 1
		if s.DoubleStep {
			step = 2
		}
		s.Count += direction * step
	})
}

func (app *counter) update(fn func(*appState)) {
	if err := app.store.Update(fn); err != nil {
		log.Print(err)
	}
}

func (app *counter) openPreferences() {
	if app.options != nil && !app.options.IsDestroyed() {
		app.options.Show()
		return
	}
	app.options = mygo.NewWindow(mygo.WindowOptions{Title: "Counter preferences", Width: 420, Height: 200, Content: ui.View(app.preferencesView)})
	nodra.InvalidateSelector(app.store, app.options, func(s appState) bool { return s.DoubleStep })
}

func (app *counter) preferencesView(c *ui.Context) {
	// Commit only the edited field against the latest state; keep concurrent count changes.
	doubleStep, err := nodra.BindField(app.store, func(s appState) bool { return s.DoubleStep }, func(s *appState, value bool) error { s.DoubleStep = value; return nil })
	if err != nil {
		log.Print(err)
		return
	}
	ui.Column(c).Fill().Center().Padding(20).Gap(16).Children(func() {
		ui.Text(c, "Counter preferences").FontSize(20).Bold()
		if err := doubleStep.Edit(func(value *bool) bool { return ui.Checkbox(c, value, "Increment by two").Changed() }); err != nil {
			log.Print(err)
		}
	})
}
