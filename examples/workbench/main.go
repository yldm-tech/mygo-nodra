// Command workbench searches real local file names and edits persisted settings. Drafts remain UI-local; search and save actions run outside Store locks.
package main

import (
	"context"
	"errors"
	"flag"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/yldm-tech/nodra"
)

type settings struct {
	Root          string `json:"root"`
	IncludeHidden bool   `json:"includeHidden"`
}
type searchInput struct {
	Settings settings
	Query    string
}
type searchResult struct {
	Root      string
	Query     string
	Files     []string
	Visited   int
	Truncated bool
}

type workbench struct {
	settings  *nodra.Store[settings]
	results   *nodra.Store[searchResult]
	search    *nodra.AsyncAction[searchResult, searchInput, searchResult]
	save      *nodra.AsyncAction[settings, settings, settings]
	draft     settings
	query     string
	notice    string
	searchRun *nodra.Task
	saveRun   *nodra.Task
}

func newWorkbench(root, path string) (*workbench, error) {
	prefs := nodra.New(settings{Root: root})
	persistence := nodra.NewPersistence(prefs, nodra.FileStorage{Path: path}, nodra.JSONCodec[settings]{})
	notice := ""
	if err := persistence.Load(context.Background()); err != nil && !errors.Is(err, os.ErrNotExist) {
		notice = "Could not load settings: " + err.Error()
	}
	app := &workbench{settings: prefs, draft: prefs.Get(), notice: notice, results: nodra.NewWithClone(searchResult{}, func(v searchResult) searchResult { v.Files = slices.Clone(v.Files); return v })}
	var err error
	app.search, err = nodra.NewAsyncAction(app.results, "search files", searchFiles, func(s *searchResult, result searchResult) error { *s = result; return nil })
	if err != nil {
		return nil, err
	}
	app.save, err = nodra.NewAsyncAction(prefs, "save settings", func(ctx context.Context, value settings, _ func(float64)) (settings, error) {
		info, err := os.Stat(value.Root)
		if err != nil {
			return value, err
		}
		if !info.IsDir() {
			return value, errors.New("choose a folder, not a file")
		}
		raw, err := (nodra.JSONCodec[settings]{}).Encode(value)
		if err != nil {
			return value, err
		}
		return value, (nodra.FileStorage{Path: path}).Save(ctx, raw)
	}, func(s *settings, value settings) error { *s = value; return nil })
	if err != nil {
		_ = app.search.Close(context.Background())
		return nil, err
	}
	return app, nil
}

func searchFiles(ctx context.Context, input searchInput, report func(float64)) (searchResult, error) {
	result := searchResult{Root: input.Settings.Root, Query: input.Query}
	query := strings.ToLower(strings.TrimSpace(input.Query))
	if query == "" {
		return result, nil
	}
	err := filepath.WalkDir(input.Settings.Root, func(path string, entry fs.DirEntry, err error) error {
		if cause := ctx.Err(); cause != nil {
			return cause
		}
		if err != nil {
			return err
		}
		if path != input.Settings.Root && !input.Settings.IncludeHidden && strings.HasPrefix(entry.Name(), ".") {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		result.Visited++
		if strings.Contains(strings.ToLower(entry.Name()), query) {
			relative, err := filepath.Rel(input.Settings.Root, path)
			if err != nil {
				return err
			}
			result.Files = append(result.Files, relative)
		}
		if result.Visited%128 == 0 {
			report(float64(result.Visited) / 50000)
		}
		if len(result.Files) >= 200 || result.Visited >= 50000 {
			result.Truncated = true
			return fs.SkipAll
		}
		return nil
	})
	return result, err
}

func (app *workbench) startSearch() {
	task, err := app.search.Run(context.Background(), searchInput{Settings: app.settings.Get(), Query: app.query})
	if err != nil {
		app.notice = err.Error()
		return
	}
	app.searchRun = task
}
func (app *workbench) saveDraft() {
	if app.save.Status().Get().Running {
		return
	}
	app.notice = ""
	if strings.TrimSpace(app.draft.Root) == "" {
		app.notice = "Choose a folder before saving."
		return
	}
	root, err := filepath.Abs(strings.TrimSpace(app.draft.Root))
	if err != nil {
		app.notice = err.Error()
		return
	}
	app.draft.Root = root
	task, err := app.save.Run(context.Background(), app.draft)
	if err != nil {
		app.notice = err.Error()
		return
	}
	app.saveRun = task
}
func (app *workbench) discardDraft() { app.draft = app.settings.Get(); app.notice = "" }

func (app *workbench) view(c *ui.Context) {
	theme := c.Theme()
	search, save := app.search.Status().Get(), app.save.Status().Get()
	// Copy only the result fields displayed by this frame, rather than cloning unrelated state.
	result := nodra.Select(app.results, func(s searchResult) searchResult { s.Files = slices.Clone(s.Files); return s }).Value
	dirty := app.draft != app.settings.Get()
	ui.Column(c).Fill().Padding(24).Gap(20).Children(func() {
		ui.Row(c).FillWidth().Gap(12).Children(func() {
			ui.Text(c, "Workspace search").FontSize(24).Bold()
			ui.Spacer(c).Grow(1)
			ui.Text(c, "Nodra + MyGo").TextColor(theme.TextMuted)
		})
		ui.Text(c, "Find local files. Keep drafts separate from saved preferences.").TextColor(theme.TextMuted)
		ui.Row(c).FillWidth().Grow(1).Gap(24).AlignItems(ui.Stretch).Children(func() {
			ui.Column(c).Width(280).Padding(18).Gap(14).Background(theme.Surface).Radius(12).Children(func() {
				ui.Text(c, "Search preferences").FontSize(17).Bold()
				ui.Text(c, "Folder").Bold()
				ui.TextInput(c, &app.draft.Root).Label("Folder path").Placeholder("Folder path").FillWidth().ReadOnly(save.Running)
				ui.Checkbox(c, &app.draft.IncludeHidden, "Include hidden files").Disabled(save.Running)
				ui.Text(c, "Changes apply after a successful save.").FontSize(12).TextColor(theme.TextMuted)
				ui.Row(c).Gap(8).Children(func() {
					ui.PrimaryButton(c, "Save settings").Disabled(!dirty || save.Running).OnClick(app.saveDraft)
					ui.Button(c, "Cancel edits").Disabled(!dirty || save.Running).OnClick(app.discardDraft)
				})
				if save.Running {
					ui.Text(c, "Saving settings…")
					ui.Progress(c, -1).FillWidth()
				} else if dirty && save.Error != "" {
					ui.Text(c, "Save failed: "+save.Error).TextColor(theme.Danger).Wrap()
				} else if !dirty {
					ui.Text(c, "Settings saved").TextColor(theme.Success)
				}
				if app.notice != "" {
					ui.Text(c, app.notice).TextColor(theme.Danger).Wrap()
				}
			})
			ui.Column(c).Grow(1).Gap(14).Children(func() {
				ui.Row(c).FillWidth().Gap(8).Children(func() {
					ui.TextInput(c, &app.query).Label("Search file names").Placeholder("Search file names…").Grow(1).OnChange(app.startSearch)
					ui.Button(c, "Search").OnClick(app.startSearch)
					ui.Button(c, "Cancel search").Disabled(!search.Running).OnClick(func() {
						if app.searchRun != nil {
							app.searchRun.Cancel()
						}
					})
				})
				if search.Running {
					ui.Text(c, "Searching…")
					ui.Progress(c, -1).FillWidth()
				} else if search.Error != "" {
					ui.Text(c, search.Error).TextColor(theme.TextMuted).Wrap()
				} else {
					ui.Textf(c, "%d matches · %d files checked", len(result.Files), result.Visited).TextColor(theme.TextMuted)
				}
				if result.Truncated {
					ui.Text(c, "Showing a bounded result set. Narrow the query or folder.").TextColor(theme.Warning).Wrap()
				}
				if result.Query != "" {
					ui.Textf(c, "Results for %q", result.Query).FontSize(12).TextColor(theme.TextMuted)
				}
				ui.Scroll(c).FillWidth().Grow(1).Children(func() {
					ui.Column(c).FillWidth().Gap(8).Children(func() {
						if len(result.Files) == 0 {
							ui.Text(c, "Type a file name to begin.").TextColor(theme.TextMuted)
						}
						for _, path := range result.Files {
							ui.Text(c.Key(path), path).FillWidth().Wrap()
						}
					})
				})
			})
		})
	})
}

func (app *workbench) close() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := app.search.Close(ctx); err != nil {
		log.Print(err)
	}
	if err := app.save.Close(ctx); err != nil {
		log.Print(err)
	}
	_ = app.results.Close()
	_ = app.settings.Close()
}

func main() {
	root, err := os.Getwd()
	if err != nil {
		log.Fatal(err)
	}
	config, err := os.UserConfigDir()
	if err != nil {
		log.Fatal(err)
	}
	path := flag.String("settings", filepath.Join(config, "nodra-workbench", "settings.json"), "settings file")
	flag.Parse()
	app, err := newWorkbench(root, *path)
	if err != nil {
		log.Fatal(err)
	}
	defer app.close()
	mygo.App.WhenReady(func() {
		window := mygo.NewWindow(mygo.WindowOptions{Title: "Nodra workspace search", Width: 960, Height: 640, Content: ui.View(app.view)})
		nodra.Invalidate(app.settings, window)
		nodra.Invalidate(app.results, window)
		nodra.Invalidate(app.search.Status(), window)
		nodra.Invalidate(app.save.Status(), window)
	})
	if err := mygo.App.Run(); err != nil {
		log.Print(err)
	}
}
