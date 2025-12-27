package main

import (
	"flag"
	"fmt"
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
}

func (t *TMUX) run(args ...string) (string, error) {
	if t.dryRun {
		fmt.Printf("tmux %s\n", strings.Join(args, " "))
		return "", nil
	}
	cmd := exec.Command("tmux", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("tmux %s failed: %v\nOutput: %s", strings.Join(args, " "), err, string(out))
	}
	return string(out), nil
}

func main() {
	configFile := flag.String("config", ".gridlock.yaml", "Path to the configuration file")
	flag.String("f", ".gridlock.yaml", "Path to the configuration file (shorthand)")
	detached := flag.Bool("detached", false, "Do not attach to the session")
	flag.Bool("d", false, "Do not attach to the session (shorthand)")
	recreate := flag.Bool("recreate", false, "Kill existing session with the same name")
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
	}

	data, err := os.ReadFile(*configFile)
	if err != nil {
		log.Fatalf("failed to read config: %v", err)
	}

	var config Config
	if err := yaml.Unmarshal(data, &config); err != nil {
		log.Fatalf("failed to parse yaml: %v", err)
	}

	t := &TMUX{dryRun: *dryRun}
	sessionName := config.Session.Name

	sessionExists := false
	_, err = t.run("has-session", "-t", sessionName)
	if err == nil && !*dryRun {
		if *recreate {
			fmt.Printf("Killing existing session: %s\n", sessionName)
			t.run("kill-session", "-t", sessionName)
		} else {
			sessionExists = true
		}
	}

	if !sessionExists {
		// 1. We always create the session in the background.
		fmt.Printf("Creating session: %s\n", sessionName)
		newSessionArgs := []string{"new-session", "-d", "-s", sessionName}
		if config.Session.WorkingDirectory != "" {
			newSessionArgs = append(newSessionArgs, "-c", expandPath(config.Session.WorkingDirectory))
		}
		if len(config.Session.Windows) > 0 {
			newSessionArgs = append(newSessionArgs, "-n", config.Session.Windows[0].Name)
		}
		t.run(newSessionArgs...)

		for i := range config.Session.Windows {
			window := &config.Session.Windows[i]
			if i > 0 {
				windowArgs := []string{"new-window", "-d", "-t", sessionName, "-n", window.Name}
				if window.WorkingDirectory != "" {
					windowArgs = append(windowArgs, "-c", expandPath(window.WorkingDirectory))
				} else if config.Session.WorkingDirectory != "" {
					windowArgs = append(windowArgs, "-c", expandPath(config.Session.WorkingDirectory))
				}
				t.run(windowArgs...)
			}

			windowTarget := fmt.Sprintf("%s:%s", sessionName, window.Name)
			// Apply layout recursively
			t.applyLayout(windowTarget, 0, window.Layout, window, config.Session.WorkingDirectory)
		}
	}

	// 4. If we are currently in a TMUX session, we detach from the current one and attach to the new one, unless created detached.
	if !*detached {
		inTMUX := os.Getenv("TMUX") != ""
		if inTMUX {
			fmt.Printf("Switching to session: %s\n", sessionName)
			t.run("switch-client", "-t", sessionName)
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
}

func (t *TMUX) applyLayout(windowTarget string, paneTarget int, node LayoutNode, window *WindowConfig, sessionWorkDir string) int {
	if node.PaneName != "" {
		paneConfig := findPane(window, node.PaneName)
		if paneConfig != nil {
			if paneConfig.Command != "" {
				t.run("send-keys", "-t", fmt.Sprintf("%s.%d", windowTarget, paneTarget), paneConfig.Command, "C-m")
			}
			if len(paneConfig.Commands) > 0 {
				for _, cmd := range paneConfig.Commands {
					t.run("send-keys", "-t", fmt.Sprintf("%s.%d", windowTarget, paneTarget), cmd, "C-m")
				}
			}
		}
		return paneTarget + 1
	}

	if len(node.Columns) > 0 {
		n := len(node.Columns)
		for i := 0; i < n-1; i++ {
			percentage := 100 * (n - 1 - i) / (n - i)
			splitArgs := []string{"split-window", "-h", "-p", fmt.Sprintf("%d", percentage), "-t", fmt.Sprintf("%s.%d", windowTarget, paneTarget+i)}
			workDir := getWorkDirForNode(&node.Columns[i+1], window, sessionWorkDir)
			if workDir != "" {
				splitArgs = append(splitArgs, "-c", workDir)
			}
			t.run(splitArgs...)
		}

		currentPane := paneTarget
		for _, col := range node.Columns {
			currentPane = t.applyLayout(windowTarget, currentPane, col, window, sessionWorkDir)
		}
		return currentPane
	} else if len(node.Rows) > 0 {
		n := len(node.Rows)
		for i := 0; i < n-1; i++ {
			percentage := 100 * (n - 1 - i) / (n - i)
			splitArgs := []string{"split-window", "-v", "-p", fmt.Sprintf("%d", percentage), "-t", fmt.Sprintf("%s.%d", windowTarget, paneTarget+i)}
			workDir := getWorkDirForNode(&node.Rows[i+1], window, sessionWorkDir)
			if workDir != "" {
				splitArgs = append(splitArgs, "-c", workDir)
			}
			t.run(splitArgs...)
		}

		currentPane := paneTarget
		for _, row := range node.Rows {
			currentPane = t.applyLayout(windowTarget, currentPane, row, window, sessionWorkDir)
		}
		return currentPane
	}
	return paneTarget + 1
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
			Name:    winName,
			Panes:   panes,
			Layout:  layoutNode,
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

