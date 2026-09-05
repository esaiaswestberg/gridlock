package main

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

// fakeTMUX records the tmux invocations made by the layout code and hands out
// pane IDs the way tmux would.
type fakeTMUX struct {
	commands [][]string
	nextID   int
	// failOn, when non-nil and returning an error, makes that invocation fail.
	failOn func(args []string) error
}

func newFakeTMUX() (*TMUX, *fakeTMUX) {
	f := &fakeTMUX{}
	t := &TMUX{runner: f.run}
	return t, f
}

func (f *fakeTMUX) run(args []string) (string, error) {
	f.commands = append(f.commands, args)
	if f.failOn != nil {
		if err := f.failOn(args); err != nil {
			return "", err
		}
	}
	switch args[0] {
	case "list-panes":
		return "%0\n", nil
	case "split-window":
		f.nextID++
		return fmt.Sprintf("%%%d\n", f.nextID), nil
	}
	return "", nil
}

func (f *fakeTMUX) strings() []string {
	var out []string
	for _, c := range f.commands {
		out = append(out, strings.Join(c, " "))
	}
	return out
}

// sends maps each dispatched command to the pane ID it was sent to.
func (f *fakeTMUX) sends() map[string]string {
	out := make(map[string]string)
	for _, c := range f.commands {
		if c[0] == "send-keys" {
			out[c[3]] = c[2]
		}
	}
	return out
}

func window(layout LayoutNode, panes ...PaneConfig) *WindowConfig {
	return &WindowConfig{Name: "authentication", Panes: panes, Layout: layout}
}

func flatPanes() []PaneConfig {
	return []PaneConfig{
		{Name: "backend", Commands: []string{"bun run dev:auth:backend"}},
		{Name: "frontend", Commands: []string{"bun run dev:auth:frontend"}},
		{Name: "shell"},
	}
}

func assertCommands(t *testing.T, f *fakeTMUX, want []string) {
	t.Helper()
	got := f.strings()
	if len(got) != len(want) {
		t.Fatalf("got %d commands, want %d:\ngot:  %v\nwant: %v", len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("command %d:\n got: %s\nwant: %s", i, got[i], want[i])
		}
	}
}

func TestApplyLayoutFlatRows(t *testing.T) {
	tm, f := newFakeTMUX()
	w := window(LayoutNode{Rows: []LayoutNode{{PaneName: "backend"}, {PaneName: "frontend"}, {PaneName: "shell"}}}, flatPanes()...)

	if err := tm.applyLayout("commonroom:authentication", w.Layout, w, ""); err != nil {
		t.Fatalf("applyLayout: %v", err)
	}

	assertCommands(t, f, []string{
		"list-panes -t commonroom:authentication -F #{pane_id}",
		"split-window -v -l 66% -t %0 -P -F #{pane_id}",
		"split-window -v -l 50% -t %1 -P -F #{pane_id}",
		"send-keys -t %0 bun run dev:auth:backend C-m",
		"send-keys -t %1 bun run dev:auth:frontend C-m",
	})
}

func TestApplyLayoutFlatColumns(t *testing.T) {
	tm, f := newFakeTMUX()
	w := window(LayoutNode{Columns: []LayoutNode{{PaneName: "backend"}, {PaneName: "frontend"}, {PaneName: "shell"}}}, flatPanes()...)

	if err := tm.applyLayout("commonroom:authentication", w.Layout, w, ""); err != nil {
		t.Fatalf("applyLayout: %v", err)
	}

	assertCommands(t, f, []string{
		"list-panes -t commonroom:authentication -F #{pane_id}",
		"split-window -h -l 66% -t %0 -P -F #{pane_id}",
		"split-window -h -l 50% -t %1 -P -F #{pane_id}",
		"send-keys -t %0 bun run dev:auth:backend C-m",
		"send-keys -t %1 bun run dev:auth:frontend C-m",
	})
}

func TestApplyLayoutNested(t *testing.T) {
	// rows: [ editor, columns: [ server, logs ] ]
	layout := LayoutNode{Rows: []LayoutNode{
		{PaneName: "editor"},
		{Columns: []LayoutNode{{PaneName: "server"}, {PaneName: "logs"}}},
	}}
	w := window(layout,
		PaneConfig{Name: "editor", Command: "nvim"},
		PaneConfig{Name: "server", Command: "npm start"},
		PaneConfig{Name: "logs", Command: "tail -f log"},
	)

	tm, f := newFakeTMUX()
	if err := tm.applyLayout("commonroom:authentication", w.Layout, w, ""); err != nil {
		t.Fatalf("applyLayout: %v", err)
	}

	assertCommands(t, f, []string{
		"list-panes -t commonroom:authentication -F #{pane_id}",
		"split-window -v -l 50% -t %0 -P -F #{pane_id}",
		"send-keys -t %0 nvim C-m",
		"split-window -h -l 50% -t %1 -P -F #{pane_id}",
		"send-keys -t %1 npm start C-m",
		"send-keys -t %2 tail -f log C-m",
	})
}

func TestCommandsGoToCorrectPaneID(t *testing.T) {
	layout := LayoutNode{Columns: []LayoutNode{
		{Rows: []LayoutNode{{PaneName: "a"}, {PaneName: "b"}}},
		{Rows: []LayoutNode{{PaneName: "c"}, {PaneName: "d"}}},
	}}
	w := window(layout,
		PaneConfig{Name: "a", Command: "cmd-a"},
		PaneConfig{Name: "b", Command: "cmd-b"},
		PaneConfig{Name: "c", Command: "cmd-c"},
		PaneConfig{Name: "d", Commands: []string{"cmd-d1", "cmd-d2"}},
	)

	tm, f := newFakeTMUX()
	if err := tm.applyLayout("s:w", w.Layout, w, ""); err != nil {
		t.Fatalf("applyLayout: %v", err)
	}

	// The outer columns split %0 -> %1. The left column then splits %0 -> %2 and
	// the right column splits %1 -> %3.
	want := map[string]string{"cmd-a": "%0", "cmd-b": "%2", "cmd-c": "%1", "cmd-d1": "%3", "cmd-d2": "%3"}
	got := f.sends()
	if len(got) != len(want) {
		t.Fatalf("dispatched %v, want %v", got, want)
	}
	for cmd, pane := range want {
		if got[cmd] != pane {
			t.Errorf("command %q sent to pane %q, want %q", cmd, got[cmd], pane)
		}
	}
}

func TestWorkingDirectoryPropagation(t *testing.T) {
	layout := LayoutNode{Rows: []LayoutNode{{PaneName: "a"}, {PaneName: "b"}, {PaneName: "c"}}}
	w := &WindowConfig{
		Name:             "win",
		WorkingDirectory: "/window",
		Panes: []PaneConfig{
			{Name: "a"},
			{Name: "b", WorkingDirectory: "/pane-b"},
			{Name: "c"},
		},
		Layout: layout,
	}

	tm, f := newFakeTMUX()
	if err := tm.applyLayout("s:win", w.Layout, w, "/session"); err != nil {
		t.Fatalf("applyLayout: %v", err)
	}

	cmds := f.strings()
	if !strings.Contains(cmds[1], "-c /pane-b") {
		t.Errorf("expected pane working directory for second row, got %q", cmds[1])
	}
	// Pane c has no working directory, so it inherits the window's.
	if !strings.Contains(cmds[2], "-c /window") {
		t.Errorf("expected window working directory for third row, got %q", cmds[2])
	}
}

func TestSplitFailurePropagates(t *testing.T) {
	tm, f := newFakeTMUX()
	// Every attempt to split the second pane fails, including the legacy-flag retry.
	f.failOn = func(args []string) error {
		if args[0] == "split-window" && strings.Contains(strings.Join(args, " "), "-t %1") {
			return fmt.Errorf("no space for new pane")
		}
		return nil
	}

	w := window(LayoutNode{Rows: []LayoutNode{{PaneName: "backend"}, {PaneName: "frontend"}, {PaneName: "shell"}}}, flatPanes()...)
	err := tm.applyLayout("commonroom:authentication", w.Layout, w, "")
	if err == nil {
		t.Fatal("expected an error when split-window fails")
	}
	for _, want := range []string{"commonroom:authentication", "layout.rows[2]", "%1", "no space for new pane"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
	for _, c := range f.commands {
		if c[0] == "send-keys" {
			t.Errorf("no commands should be dispatched after a failed split, got %v", c)
		}
	}
}

func TestSendKeysFailurePropagates(t *testing.T) {
	tm, f := newFakeTMUX()
	f.failOn = func(args []string) error {
		if args[0] == "send-keys" {
			return fmt.Errorf("pane not found")
		}
		return nil
	}

	w := window(LayoutNode{Rows: []LayoutNode{{PaneName: "backend"}, {PaneName: "frontend"}}}, flatPanes()...)
	err := tm.applyLayout("commonroom:authentication", w.Layout, w, "")
	if err == nil {
		t.Fatal("expected an error when send-keys fails")
	}
	for _, want := range []string{"commonroom:authentication", "backend", "%0", "bun run dev:auth:backend"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestPaneLookupFailurePropagates(t *testing.T) {
	tm, f := newFakeTMUX()
	f.failOn = func(args []string) error {
		if args[0] == "list-panes" {
			return fmt.Errorf("can't find window")
		}
		return nil
	}

	w := window(LayoutNode{Rows: []LayoutNode{{PaneName: "backend"}}}, flatPanes()...)
	err := tm.applyLayout("commonroom:missing", w.Layout, w, "")
	if err == nil {
		t.Fatal("expected an error when the pane lookup fails")
	}
	if !strings.Contains(err.Error(), "commonroom:missing") || !strings.Contains(err.Error(), "can't find window") {
		t.Errorf("unexpected error: %v", err)
	}
	if len(f.commands) != 1 {
		t.Errorf("expected to stop after the failed lookup, got %v", f.strings())
	}
}

func TestDryRunPrintsCommands(t *testing.T) {
	var buf bytes.Buffer
	tm := &TMUX{dryRun: true, out: &buf}

	w := window(LayoutNode{Rows: []LayoutNode{{PaneName: "backend"}, {PaneName: "frontend"}, {PaneName: "shell"}}}, flatPanes()...)
	if err := tm.applyLayout("commonroom:authentication", w.Layout, w, ""); err != nil {
		t.Fatalf("dry run should not fail: %v", err)
	}

	want := []string{
		"tmux list-panes -t commonroom:authentication -F #{pane_id}",
		"tmux split-window -v -l 66% -t %dry0 -P -F #{pane_id}",
		"tmux split-window -v -l 50% -t %dry1 -P -F #{pane_id}",
		"tmux send-keys -t %dry0 bun run dev:auth:backend C-m",
		"tmux send-keys -t %dry1 bun run dev:auth:frontend C-m",
	}
	got := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(got) != len(want) {
		t.Fatalf("got %d lines, want %d:\n%s", len(got), len(want), buf.String())
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line %d:\n got: %s\nwant: %s", i, got[i], want[i])
		}
	}
}

// The YAML shape documented in the README must keep parsing and producing the
// same layout.
func TestConfigParsingUnchanged(t *testing.T) {
	const src = `
session:
  name: commonroom
  windows:
    - name: authentication
      panes:
        - name: backend
          commands:
            - bun run dev:auth:backend
        - name: frontend
          commands:
            - bun run dev:auth:frontend
        - name: shell
      layout:
        rows:
          - backend
          - frontend
          - shell
`
	cfg, err := parseConfig([]byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cfg.Session.Name != "commonroom" || len(cfg.Session.Windows) != 1 {
		t.Fatalf("unexpected config: %+v", cfg)
	}
	w := &cfg.Session.Windows[0]
	if len(w.Layout.Rows) != 3 || w.Layout.Rows[0].PaneName != "backend" {
		t.Fatalf("unexpected layout: %+v", w.Layout)
	}

	tm, f := newFakeTMUX()
	if err := tm.applyLayout("commonroom:authentication", w.Layout, w, cfg.Session.WorkingDirectory); err != nil {
		t.Fatalf("applyLayout: %v", err)
	}
	assertCommands(t, f, []string{
		"list-panes -t commonroom:authentication -F #{pane_id}",
		"split-window -v -l 66% -t %0 -P -F #{pane_id}",
		"split-window -v -l 50% -t %1 -P -F #{pane_id}",
		"send-keys -t %0 bun run dev:auth:backend C-m",
		"send-keys -t %1 bun run dev:auth:frontend C-m",
	})
}

// tmux builds that reject "-l <percentage>%" must transparently fall back to the
// deprecated "-p <percentage>" flag.
func TestSplitFallsBackToLegacyPercentage(t *testing.T) {
	tm, f := newFakeTMUX()
	f.failOn = func(args []string) error {
		for i, a := range args {
			if a == "-l" && i+1 < len(args) && strings.HasSuffix(args[i+1], "%") {
				return fmt.Errorf("size missing")
			}
		}
		return nil
	}

	w := window(LayoutNode{Rows: []LayoutNode{{PaneName: "backend"}, {PaneName: "frontend"}, {PaneName: "shell"}}}, flatPanes()...)
	if err := tm.applyLayout("commonroom:authentication", w.Layout, w, ""); err != nil {
		t.Fatalf("applyLayout: %v", err)
	}

	assertCommands(t, f, []string{
		"list-panes -t commonroom:authentication -F #{pane_id}",
		"split-window -v -l 66% -t %0 -P -F #{pane_id}",
		"split-window -v -p 66 -t %0 -P -F #{pane_id}",
		// The fallback is remembered, so the second split does not retry.
		"split-window -v -p 50 -t %1 -P -F #{pane_id}",
		"send-keys -t %0 bun run dev:auth:backend C-m",
		"send-keys -t %1 bun run dev:auth:frontend C-m",
	})
}
