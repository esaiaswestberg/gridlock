package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
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
		wd, err := os.Getwd()
		if err != nil {
			log.Fatalf("failed to get working directory: %v", err)
		}
		sessionName := filepath.Base(wd)
		config := Config{
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

		var buf strings.Builder
		enc := yaml.NewEncoder(&buf)
		enc.SetIndent(2)
		if err := enc.Encode(&config); err != nil {
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
