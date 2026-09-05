package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Session SessionConfig `yaml:"session"`
}

type SessionConfig struct {
	Name             string         `yaml:"name"`
	WorkingDirectory string         `yaml:"working-directory,omitempty"`
	Windows          []WindowConfig `yaml:"windows,omitempty"`
}

type WindowConfig struct {
	Name             string       `yaml:"name"`
	WorkingDirectory string       `yaml:"working-directory,omitempty"`
	Panes            []PaneConfig `yaml:"panes,omitempty"`
	Layout           LayoutNode   `yaml:"layout,omitempty"`
}

type PaneConfig struct {
	Name             string   `yaml:"name"`
	WorkingDirectory string   `yaml:"working-directory,omitempty"`
	Command          string   `yaml:"command,omitempty"`
	Commands         []string `yaml:"commands,omitempty"`
}

type LayoutNode struct {
	PaneName string       `yaml:"pane,omitempty"`
	Columns  []LayoutNode `yaml:"columns,omitempty"`
	Rows     []LayoutNode `yaml:"rows,omitempty"`
}

func (n *LayoutNode) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.ScalarNode {
		return value.Decode(&n.PaneName)
	}
	var m map[string][]LayoutNode
	if err := value.Decode(&m); err != nil {
		return err
	}
	if cols, ok := m["columns"]; ok {
		n.Columns = cols
	}
	if rows, ok := m["rows"]; ok {
		n.Rows = rows
	}
	return nil
}

func (n LayoutNode) MarshalYAML() (interface{}, error) {
	if n.PaneName != "" {
		return n.PaneName, nil
	}
	m := make(map[string][]LayoutNode)
	if len(n.Columns) > 0 {
		m["columns"] = n.Columns
	}
	if len(n.Rows) > 0 {
		m["rows"] = n.Rows
	}
	return m, nil
}

type TMUX struct {
	dryRun bool

	// out receives the command trace printed in dry-run mode. Defaults to os.Stdout.
	out io.Writer

	// runner, when set, replaces the actual tmux invocation. Used by tests.
	runner func(args []string) (string, error)

	// dryRunPanes counts the synthetic pane IDs handed out in dry-run mode.
	dryRunPanes int

	// legacySize is set once tmux is found to reject "-l <percentage>%" and to
	// require the deprecated "-p <percentage>" instead.
	legacySize bool
}

func (t *TMUX) writer() io.Writer {
	if t.out != nil {
		return t.out
	}
	return os.Stdout
}

func (t *TMUX) run(args ...string) (string, error) {
	if t.dryRun {
		fmt.Fprintf(t.writer(), "tmux %s\n", strings.Join(args, " "))
		return "", nil
	}
	if t.runner != nil {
		return t.runner(args)
	}
	cmd := exec.Command("tmux", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("tmux %s failed: %v\nOutput: %s", strings.Join(args, " "), err, string(out))
	}
	return string(out), nil
}

// nextDryRunPaneID produces a placeholder pane ID for dry-run mode, where tmux
// never actually runs and therefore never reports real pane IDs.
func (t *TMUX) nextDryRunPaneID() string {
	id := fmt.Sprintf("%%dry%d", t.dryRunPanes)
	t.dryRunPanes++
	return id
}

// firstPaneID returns the stable tmux pane ID (#{pane_id}) of the first pane of
// a window. This is the anchor every layout is built from.
func (t *TMUX) firstPaneID(windowTarget string) (string, error) {
	out, err := t.run("list-panes", "-t", windowTarget, "-F", "#{pane_id}")
	if err != nil {
		return "", fmt.Errorf("window %s: failed to list panes: %w", windowTarget, err)
	}
	if id := firstLine(out); id != "" {
		return id, nil
	}
	if t.dryRun {
		return t.nextDryRunPaneID(), nil
	}
	return "", fmt.Errorf("window %s: list-panes returned no pane id", windowTarget)
}

func splitArgs(direction string, percentage int, target, workDir string, legacySize bool) []string {
	size := []string{"-l", fmt.Sprintf("%d%%", percentage)}
	if legacySize {
		size = []string{"-p", strconv.Itoa(percentage)}
	}
	args := append([]string{"split-window", direction}, size...)
	args = append(args, "-t", target, "-P", "-F", "#{pane_id}")
	if workDir != "" {
		args = append(args, "-c", workDir)
	}
	return args
}

// splitPane splits target and returns the stable pane ID of the newly created
// pane, as reported by tmux itself.
func (t *TMUX) splitPane(direction string, percentage int, target, workDir, windowTarget, nodePath string) (string, error) {
	out, err := t.run(splitArgs(direction, percentage, target, workDir, t.legacySize)...)
	if err != nil && !t.legacySize {
		// tmux before 3.1 does not accept a percentage for -l and needs the
		// deprecated -p flag instead. Retry once and remember the answer.
		if legacyOut, legacyErr := t.run(splitArgs(direction, percentage, target, workDir, true)...); legacyErr == nil {
			t.legacySize = true
			out, err = legacyOut, nil
		}
	}
	if err != nil {
		return "", fmt.Errorf("window %s: %s: failed to split pane %s: %w", windowTarget, nodePath, target, err)
	}
	if id := firstLine(out); id != "" {
		return id, nil
	}
	if t.dryRun {
		return t.nextDryRunPaneID(), nil
	}
	return "", fmt.Errorf("window %s: %s: split-window on %s returned no pane id", windowTarget, nodePath, target)
}

func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func parseConfig(data []byte) (*Config, error) {
	var config Config
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, err
	}
	return &config, nil
}

func main() {
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage of %s:\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "  --config, -f string\n        Path to the configuration file (default \".gridlock.yaml\")\n")
		fmt.Fprintf(os.Stderr, "  --detached, -d\n        Do not attach to the session\n")
		fmt.Fprintf(os.Stderr, "  --current, -c\n        Create windows from the configuration in the current TMUX session instead of a new one\n")
		fmt.Fprintf(os.Stderr, "  --recreate\n        Recreate the session. If run from within the target session, it cleans and rebuilds it without exiting\n")
		fmt.Fprintf(os.Stderr, "  --dry-run\n        Print commands without executing them\n")
	}
	configFile := flag.String("config", ".gridlock.yaml", "Path to the configuration file")
	flag.String("f", ".gridlock.yaml", "Path to the configuration file (shorthand)")
	detached := flag.Bool("detached", false, "Do not attach to the session")
	flag.Bool("d", false, "Do not attach to the session (shorthand)")
	current := flag.Bool("current", false, "Create windows from the configuration in the current TMUX session instead of a new one")
	flag.Bool("c", false, "Create windows in the current TMUX session (shorthand)")
	recreate := flag.Bool("recreate", false, "Recreate the session. If run from within the target session, it cleans and rebuilds it without exiting")
	dryRun := flag.Bool("dry-run", false, "Print commands without executing them")
	flag.Parse()

	if flag.Arg(0) == "init" {
		initCmd := flag.NewFlagSet("init", flag.ExitOnError)
		saveCurrent := initCmd.Bool("save-current", false, "Save the current TMUX session to the config file")
		initCmd.Parse(flag.Args()[1:])

		wd, err := os.Getwd()
		if err != nil {
			log.Fatalf("failed to get working directory: %v", err)
		}

		var config *Config
		var sessionName string

		if *saveCurrent {
			// Check if we are in tmux or have a session attached
			// We can try to guess the session name from TMUX env var if set, or just capture the attached session.
			// Actually, if we run `tmux display-message -p '#S'`, it returns the current session if attached/inside.

			t := &TMUX{dryRun: false}
			out, err := t.run("display-message", "-p", "#S")
			if err != nil {
				log.Fatalf("Failed to get current session: %v. Are you inside or attached to a TMUX session?", err)
			}
			currentSession := strings.TrimSpace(out)

			fmt.Printf("Capturing session: %s\n", currentSession)
			config, err = captureCurrentSession(currentSession)
			if err != nil {
				log.Fatalf("Failed to capture session: %v", err)
			}
			sessionName = currentSession
		} else {
			sessionName = filepath.Base(wd)
			config = &Config{
				Session: SessionConfig{
					Name: sessionName,
					Windows: []WindowConfig{
						{
							Name: "main",
							Panes: []PaneConfig{
								{
									Name:    "bash",
									Command: "echo Gridlock",
								},
							},
							Layout: LayoutNode{
								Columns: []LayoutNode{
									{PaneName: "bash"},
								},
							},
						},
					},
				},
			}
		}

		var buf strings.Builder
		enc := yaml.NewEncoder(&buf)
		enc.SetIndent(2)
		if err := enc.Encode(config); err != nil {
			log.Fatalf("failed to marshal yaml: %v", err)
		}
		data := []byte(buf.String())

		if _, err := os.Stat(".gridlock.yaml"); err == nil {
			log.Fatalf(".gridlock.yaml already exists")
		}

		if err := os.WriteFile(".gridlock.yaml", data, 0644); err != nil {
			log.Fatalf("failed to write config: %v", err)
		}

		fmt.Printf("Initialized .gridlock.yaml with session name: %s\n", sessionName)
		return
	}

	// Handle shorthands manually because flag package is limited
	for i, arg := range os.Args {
		if arg == "-f" && i+1 < len(os.Args) {
			*configFile = os.Args[i+1]
		}
		if arg == "-d" {
			*detached = true
		}
		if arg == "-c" {
			*current = true
		}
	}

	data, err := os.ReadFile(*configFile)
	if err != nil {
		log.Fatalf("failed to read config: %v", err)
	}

	cfg, err := parseConfig(data)
	if err != nil {
		log.Fatalf("failed to parse yaml: %v", err)
	}
	config := *cfg

	t := &TMUX{dryRun: *dryRun}
	sessionName := config.Session.Name

	inTMUX := os.Getenv("TMUX") != ""
	currentSession := ""
	if inTMUX {
		out, err := t.run("display-message", "-p", "#S")
		if err == nil {
			currentSession = strings.TrimSpace(out)
		}
	}

	useCurrent := *current
	if useCurrent {
		if !inTMUX {
			log.Fatalf("Not inside a TMUX session. Cannot use --current")
		}
		sessionName = currentSession
	}

	// layoutErrors collects every failure that happened while building windows and
	// panes, so a partially created session is never reported as a success.
	var layoutErrors []error

	sessionExists := false
	survivorWindowID := ""
	if !useCurrent {
		_, err = t.run("has-session", "-t", sessionName)
		if err == nil && !*dryRun {
			if *recreate {
				if inTMUX && currentSession == sessionName {
					fmt.Printf("Inside target session, cleaning instead of killing: %s\n", sessionName)
					survivorWindowID = cleanSession(t)
				} else {
					fmt.Printf("Killing existing session: %s\n", sessionName)
					t.run("kill-session", "-t", sessionName)
				}
			} else {
				sessionExists = true
			}
		}
	}

	if !sessionExists || useCurrent {
		if !useCurrent && survivorWindowID == "" {
			// 1. We always create the session in the background.
			fmt.Printf("Creating session: %s\n", sessionName)
			newSessionArgs := []string{"new-session", "-d", "-s", sessionName}
			if config.Session.WorkingDirectory != "" {
				newSessionArgs = append(newSessionArgs, "-c", expandPath(config.Session.WorkingDirectory))
			}
			if len(config.Session.Windows) > 0 {
				newSessionArgs = append(newSessionArgs, "-n", config.Session.Windows[0].Name)
			}
			if _, err := t.run(newSessionArgs...); err != nil {
				log.Fatalf("Failed to create session: %v", err)
			}
		}

		if !useCurrent && survivorWindowID != "" {
			// Inside target session and recreating: session already exists but is empty (except for survivor window)
			fmt.Printf("Recreating windows in current session: %s\n", sessionName)
		} else if useCurrent {
			fmt.Printf("Adding windows to current session: %s\n", sessionName)
		}

		var firstWindowName string
		for i := range config.Session.Windows {
			window := &config.Session.Windows[i]
			uniqueName := window.Name
			if i > 0 || useCurrent || survivorWindowID != "" {
				uniqueName = t.getUniqueWindowName(sessionName, window.Name)
				fmt.Printf("Creating window: %s\n", uniqueName)
				windowArgs := []string{"new-window", "-d", "-t", sessionName + ":", "-n", uniqueName}
				if window.WorkingDirectory != "" {
					windowArgs = append(windowArgs, "-c", expandPath(window.WorkingDirectory))
				} else if config.Session.WorkingDirectory != "" {
					windowArgs = append(windowArgs, "-c", expandPath(config.Session.WorkingDirectory))
				}
				if _, err := t.run(windowArgs...); err != nil {
					err = fmt.Errorf("failed to create window %s: %w", uniqueName, err)
					log.Printf("Error: %v", err)
					layoutErrors = append(layoutErrors, err)
					continue
				}
			}
			if i == 0 {
				firstWindowName = uniqueName
			}

			windowTarget := fmt.Sprintf("%s:%s", sessionName, uniqueName)
			// Apply layout recursively, driven by real tmux pane IDs.
			if err := t.applyLayout(windowTarget, window.Layout, window, config.Session.WorkingDirectory); err != nil {
				log.Printf("Error: %v", err)
				layoutErrors = append(layoutErrors, err)
			}
		}

		// Switch to the first window if not detached
		if !*detached && firstWindowName != "" {
			fmt.Printf("Switching to window: %s\n", firstWindowName)
			t.run("select-window", "-t", fmt.Sprintf("%s:%s", sessionName, firstWindowName))
		}

		if survivorWindowID != "" {
			t.run("kill-window", "-t", survivorWindowID)
		}
	}

	if len(layoutErrors) > 0 {
		fmt.Fprintf(os.Stderr, "\nGridlock finished with %d error(s); the session may be incomplete:\n", len(layoutErrors))
		for _, err := range layoutErrors {
			fmt.Fprintf(os.Stderr, "  - %v\n", err)
		}
	}

	// 4. If we are currently in a TMUX session, we detach from the current one and attach to the new one, unless created detached.
	if !*detached {
		if inTMUX {
			if currentSession != sessionName {
				fmt.Printf("Switching to session: %s\n", sessionName)
				t.run("switch-client", "-t", sessionName)
			}
		} else {
			fmt.Printf("Attaching to session: %s\n", sessionName)
			// attach-session usually takes over the terminal, so we use exec.Command to replace the process if not dryRun
			if !*dryRun {
				cmd := exec.Command("tmux", "attach-session", "-t", sessionName)
				cmd.Stdin = os.Stdin
				cmd.Stdout = os.Stdout
				cmd.Stderr = os.Stderr
				if err := cmd.Run(); err != nil {
					log.Fatalf("failed to attach to session: %v", err)
				}
			} else {
				t.run("attach-session", "-t", sessionName)
			}
		}
	}

	if len(layoutErrors) > 0 {
		os.Exit(1)
	}
}

func cleanSession(t *TMUX) string {
	// Returns the ID of the window that survived
	out, err := t.run("display-message", "-p", "#{window_id}")
	if err != nil {
		return ""
	}
	currentWindowID := strings.TrimSpace(out)

	// Rename it so it doesn't conflict with config names
	t.run("rename-window", "-t", currentWindowID, ".gridlock-survivor")

	// Kill all other windows
	t.run("kill-window", "-a", "-t", currentWindowID)

	return currentWindowID
}

// applyLayout builds the pane layout of a window. It resolves the window's
// initial pane ID from tmux and then drives every split from stable pane IDs
// (#{pane_id}) reported by tmux, never from predicted numeric pane indexes.
func (t *TMUX) applyLayout(windowTarget string, node LayoutNode, window *WindowConfig, sessionWorkDir string) error {
	rootPane, err := t.firstPaneID(windowTarget)
	if err != nil {
		return err
	}
	return t.buildLayout(windowTarget, rootPane, node, window, sessionWorkDir, "layout")
}

// buildLayout splits paneID according to node and recurses into the children.
// nodePath is a human readable location of node inside the window's layout,
// used to make errors identifiable.
func (t *TMUX) buildLayout(windowTarget, paneID string, node LayoutNode, window *WindowConfig, sessionWorkDir, nodePath string) error {
	if node.PaneName != "" {
		return t.sendPaneCommands(windowTarget, paneID, node.PaneName, window, nodePath)
	}

	// Columns take precedence over rows when both are present, as before.
	children := node.Rows
	direction := "-v"
	kind := "rows"
	if len(node.Columns) > 0 {
		children = node.Columns
		direction = "-h"
		kind = "columns"
	}
	if len(children) == 0 {
		return nil
	}

	// The node's own pane becomes the first child; every further child is a new
	// pane created by splitting the most recently created one. The shrinking
	// percentages keep the resulting panes approximately equal in size.
	n := len(children)
	paneIDs := make([]string, n)
	paneIDs[0] = paneID
	current := paneID
	for i := 0; i < n-1; i++ {
		percentage := 100 * (n - 1 - i) / (n - i)
		workDir := getWorkDirForNode(&children[i+1], window, sessionWorkDir)
		childPath := fmt.Sprintf("%s.%s[%d]", nodePath, kind, i+1)
		newPane, err := t.splitPane(direction, percentage, current, workDir, windowTarget, childPath)
		if err != nil {
			return err
		}
		paneIDs[i+1] = newPane
		current = newPane
	}

	for i := range children {
		childPath := fmt.Sprintf("%s.%s[%d]", nodePath, kind, i)
		if err := t.buildLayout(windowTarget, paneIDs[i], children[i], window, sessionWorkDir, childPath); err != nil {
			return err
		}
	}
	return nil
}

// sendPaneCommands dispatches a leaf's configured commands to the real pane ID
// that ended up representing it.
func (t *TMUX) sendPaneCommands(windowTarget, paneID, paneName string, window *WindowConfig, nodePath string) error {
	paneConfig := findPane(window, paneName)
	if paneConfig == nil {
		return nil
	}

	commands := make([]string, 0, len(paneConfig.Commands)+1)
	if paneConfig.Command != "" {
		commands = append(commands, paneConfig.Command)
	}
	commands = append(commands, paneConfig.Commands...)

	for _, command := range commands {
		if _, err := t.run("send-keys", "-t", paneID, command, "C-m"); err != nil {
			return fmt.Errorf("window %s: %s: pane %q (tmux target %s): failed to send command %q: %w",
				windowTarget, nodePath, paneName, paneID, command, err)
		}
	}
	return nil
}

func getWorkDirForNode(node *LayoutNode, window *WindowConfig, sessionWorkDir string) string {
	if node.PaneName != "" {
		p := findPane(window, node.PaneName)
		if p != nil && p.WorkingDirectory != "" {
			return expandPath(p.WorkingDirectory)
		}
		if window.WorkingDirectory != "" {
			return expandPath(window.WorkingDirectory)
		}
		return expandPath(sessionWorkDir)
	}
	if len(node.Columns) > 0 {
		return getWorkDirForNode(&node.Columns[0], window, sessionWorkDir)
	}
	if len(node.Rows) > 0 {
		return getWorkDirForNode(&node.Rows[0], window, sessionWorkDir)
	}
	return expandPath(sessionWorkDir)
}

func expandPath(path string) string {
	if strings.HasPrefix(path, "~/") || path == "~" {
		home, err := os.UserHomeDir()
		if err != nil {
			return path
		}
		if path == "~" {
			return home
		}
		return filepath.Join(home, path[2:])
	}
	return path
}

func findPane(window *WindowConfig, name string) *PaneConfig {
	for i := range window.Panes {
		p := &window.Panes[i]
		if p.Name == name {
			return p
		}
		// Try suffix match of the "-pane-XXX" part
		pSuffix := p.Name
		if idx := strings.LastIndex(p.Name, "-pane-"); idx != -1 {
			pSuffix = p.Name[idx:]
		}
		if strings.HasSuffix(name, pSuffix) {
			return p
		}
	}
	return nil
}

func (t *TMUX) getUniqueWindowName(sessionName string, baseName string) string {
	out, err := t.run("list-windows", "-t", sessionName, "-F", "#{window_name}")
	if err != nil {
		// If session is new or list-windows fails, assume baseName is okay
		return baseName
	}

	existing := make(map[string]bool)
	lines := strings.Split(strings.TrimSpace(out), "\n")
	for _, name := range lines {
		name = strings.TrimSpace(name)
		if name != "" {
			existing[name] = true
		}
	}

	if !existing[baseName] {
		return baseName
	}

	for i := 1; i < 100; i++ {
		newName := fmt.Sprintf("%s-%02d", baseName, i)
		if !existing[newName] {
			return newName
		}
	}
	return baseName
}

func captureCurrentSession(sessionName string) (*Config, error) {
	t := &TMUX{dryRun: false}

	// Verify session exists
	_, err := t.run("has-session", "-t", sessionName)
	if err != nil {
		return nil, fmt.Errorf("session %s not found", sessionName)
	}

	// Get Windows
	out, err := t.run("list-windows", "-t", sessionName, "-F", "#{window_id} #{window_name} #{window_layout}")
	if err != nil {
		return nil, fmt.Errorf("failed to list windows: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(out), "\n")
	var windows []WindowConfig

	// Get Session CWD (from first pane of first window usually, or just assume user home for now,
	// but let's try to infer from common prefix later? No, let's just leave it empty and set per-window/pane)
	// Actually, tmux has a session working directory but it's not easily exposed unless we look at the session creation time or just ignore it.
	// We will rely on window/pane working directories.

	for _, line := range lines {
		parts := strings.SplitN(line, " ", 3)
		if len(parts) < 3 {
			continue
		}
		winID := parts[0]
		winName := parts[1]
		layoutStr := parts[2]

		// Get Panes for this window
		paneOut, err := t.run("list-panes", "-t", winID, "-F", "#{pane_id} #{pane_current_path} #{pane_current_command}")
		if err != nil {
			return nil, fmt.Errorf("failed to list panes for window %s: %v", winName, err)
		}

		paneLines := strings.Split(strings.TrimSpace(paneOut), "\n")
		var panes []PaneConfig
		paneIDMap := make(map[int]string)

		for i, pLine := range paneLines {
			pParts := strings.SplitN(pLine, " ", 3)
			if len(pParts) < 3 {
				continue
			}
			pIDStr := pParts[0]
			pPath := pParts[1]
			pCmd := pParts[2]

			// Generate a name
			pName := fmt.Sprintf("%s-pane-%d", winName, i)

			// Try to simplify path
			home, _ := os.UserHomeDir()
			if strings.HasPrefix(pPath, home) {
				pPath = "~" + strings.TrimPrefix(pPath, home)
			}

			// Clean up command (if it's just a shell, maybe ignore it? No, keep it.)
			// If it's bash/zsh/sh, it might be the default shell, but explicit is okay.

			panes = append(panes, PaneConfig{
				Name:             pName,
				WorkingDirectory: pPath,
				Command:          pCmd,
			})

			// Map ID (remove %) to name
			idVal, _ := strconv.Atoi(strings.TrimPrefix(pIDStr, "%"))
			paneIDMap[idVal] = pName
		}

		// Parse Layout
		layoutNode, err := parseTmuxLayout(layoutStr, paneIDMap)
		if err != nil {
			// Fallback: just columns
			log.Printf("Warning: failed to parse layout for window %s: %v. Using simple column layout.", winName, err)
			var cols []LayoutNode
			for _, p := range panes {
				cols = append(cols, LayoutNode{PaneName: p.Name})
			}
			layoutNode = LayoutNode{Columns: cols}
		}

		windows = append(windows, WindowConfig{
			Name:   winName,
			Panes:  panes,
			Layout: layoutNode,
		})
	}

	return &Config{
		Session: SessionConfig{
			Name:    sessionName,
			Windows: windows,
		},
	}, nil
}

func parseTmuxLayout(layout string, paneMap map[int]string) (LayoutNode, error) {
	// Format: checksum,WxH,X,Y{...} or ...[...] or ...,ID
	// 1. Remove checksum if present (hex followed by comma) at start
	if idx := strings.Index(layout, ","); idx != -1 {
		// Check if prefix is hex checksum (approx check)
		prefix := layout[:idx]
		if matched, _ := regexp.MatchString(`^[0-9a-f]{4}$`, prefix); matched {
			layout = layout[idx+1:]
		}
	}

	// Regex to match WxH,X,Y
	// We just need to find where the geometry ends.
	// It ends at `{`, `[`, or `,`.
	// Actually, leaf node format: WxH,X,Y,ID
	// Container: WxH,X,Y{...} or WxH,X,Y[...]

	re := regexp.MustCompile(`^\d+x\d+,\d+,\d+`)
	loc := re.FindStringIndex(layout)
	if loc == nil {
		return LayoutNode{}, fmt.Errorf("invalid layout format: %s", layout)
	}

	rest := layout[loc[1]:]
	if len(rest) == 0 {
		return LayoutNode{}, fmt.Errorf("unexpected end of layout string")
	}

	firstChar := rest[0]
	content := rest[1:] // remove first char

	if firstChar == ',' {
		// Leaf node: ,ID
		idStr := content
		id, err := strconv.Atoi(idStr)
		if err != nil {
			return LayoutNode{}, fmt.Errorf("invalid pane ID: %s", idStr)
		}
		name, ok := paneMap[id]
		if !ok {
			// Maybe pane is not in the list? (e.g. dead pane?)
			// Or we parsed ID wrong.
			return LayoutNode{PaneName: fmt.Sprintf("unknown-pane-%d", id)}, nil
		}
		return LayoutNode{PaneName: name}, nil
	} else if firstChar == '{' {
		// Horizontal split (Columns)
		// Remove trailing }
		if content[len(content)-1] != '}' {
			return LayoutNode{}, fmt.Errorf("mismatched braces in layout")
		}
		content = content[:len(content)-1]
		childrenStr := splitLayoutChildren(content)
		var columns []LayoutNode
		for _, child := range childrenStr {
			node, err := parseTmuxLayout(child, paneMap)
			if err != nil {
				return LayoutNode{}, err
			}
			columns = append(columns, node)
		}
		return LayoutNode{Columns: columns}, nil

	} else if firstChar == '[' {
		// Vertical split (Rows)
		// Remove trailing ]
		if content[len(content)-1] != ']' {
			return LayoutNode{}, fmt.Errorf("mismatched brackets in layout")
		}
		content = content[:len(content)-1]
		childrenStr := splitLayoutChildren(content)
		var rows []LayoutNode
		for _, child := range childrenStr {
			node, err := parseTmuxLayout(child, paneMap)
			if err != nil {
				return LayoutNode{}, err
			}
			rows = append(rows, node)
		}
		return LayoutNode{Rows: rows}, nil
	}

	return LayoutNode{}, fmt.Errorf("unexpected character after geometry: %c", firstChar)
}

func splitLayoutChildren(s string) []string {
	var children []string
	re := regexp.MustCompile(`^\d+x\d+,\d+,\d+`)

	for len(s) > 0 {
		// Find end of current node
		// A node starts with WxH,X,Y
		loc := re.FindStringIndex(s)
		if loc == nil {
			// Should not happen if valid layout
			break
		}

		cursor := loc[1]
		if cursor >= len(s) {
			children = append(children, s)
			break
		}

		char := s[cursor]
		if char == ',' {
			// Leaf: ,ID
			cursor++
			// Consume digits
			for cursor < len(s) && s[cursor] >= '0' && s[cursor] <= '9' {
				cursor++
			}
		} else if char == '{' || char == '[' {
			// Container
			openChar := char
			closeChar := '}'
			if openChar == '[' {
				closeChar = ']'
			}
			cursor++
			depth := 1
			for cursor < len(s) && depth > 0 {
				if s[cursor] == openChar {
					depth++
				}
				if s[cursor] == byte(closeChar) {
					depth--
				}
				cursor++
			}
		}

		// Now cursor is at end of node
		children = append(children, s[:cursor])

		// If there is a comma separator, skip it for the next iteration
		if cursor < len(s) && s[cursor] == ',' {
			cursor++
		}
		s = s[cursor:]
	}
	return children
}
