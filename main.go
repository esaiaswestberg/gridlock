package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Session SessionConfig `yaml:"session"`
}

type SessionConfig struct {
	Name             string         `yaml:"name"`
	WorkingDirectory string         `yaml:"working-directory"`
	Windows          []WindowConfig `yaml:"windows"`
}

type WindowConfig struct {
	Name             string       `yaml:"name"`
	WorkingDirectory string       `yaml:"working-directory"`
	Panes            []PaneConfig `yaml:"panes"`
	Layout           LayoutNode   `yaml:"layout"`
}

type PaneConfig struct {
	Name             string `yaml:"name"`
	WorkingDirectory string `yaml:"working-directory"`
	Command          string `yaml:"command"`
}

type LayoutNode struct {
	PaneName string
	Columns  []LayoutNode
	Rows     []LayoutNode
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
			newSessionArgs = append(newSessionArgs, "-c", config.Session.WorkingDirectory)
		}
		if len(config.Session.Windows) > 0 {
			newSessionArgs = append(newSessionArgs, "-n", config.Session.Windows[0].Name)
		}
		t.run(newSessionArgs...)

		for i := range config.Session.Windows {
			window := &config.Session.Windows[i]
			if i > 0 {
				windowArgs := []string{"new-window", "-t", sessionName, "-n", window.Name}
				if window.WorkingDirectory != "" {
					windowArgs = append(windowArgs, "-c", window.WorkingDirectory)
				} else if config.Session.WorkingDirectory != "" {
					windowArgs = append(windowArgs, "-c", config.Session.WorkingDirectory)
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
			return p.WorkingDirectory
		}
		if window.WorkingDirectory != "" {
			return window.WorkingDirectory
		}
		return sessionWorkDir
	}
	if len(node.Columns) > 0 {
		return getWorkDirForNode(&node.Columns[0], window, sessionWorkDir)
	}
	if len(node.Rows) > 0 {
		return getWorkDirForNode(&node.Rows[0], window, sessionWorkDir)
	}
	return sessionWorkDir
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
