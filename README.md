# Gridlock

Gridlock is a powerful TMUX session manager and automator that allows you to define complex TMUX environments using a simple YAML configuration.

## Features

- **Declarative Configuration**: Define sessions, windows, and panes in a single YAML file.
- **Complex Layouts**: Supports nested rows and columns for precise pane placement.
- **Automatic Setup**: Automatically runs commands in specific panes upon session creation.
- **Working Directory Management**: Set working directories at the session, window, or individual pane level.
- **Smart Attachment**: Attach to new sessions, switch from within existing TMUX sessions, or create them in detached mode.

You can install Gridlock instantly using the following commands:

### Linux & macOS
```bash
curl -sSL https://raw.githubusercontent.com/esaiaswestberg/gridlock/main/install.sh | bash
```

### Windows (PowerShell)
```powershell
irm https://raw.githubusercontent.com/esaiaswestberg/gridlock/main/install.ps1 | iex
```

### Arch Linux (AUR)
```bash
# Using an AUR helper
yay -S gridlock
paru -S gridlock
```

## Manual Installation

You can download the pre-built binaries for your platform from the [Releases](https://github.com/esaiaswestberg/gridlock/releases) page.

Alternatively, if you have Go installed:

```bash
go install github.com/esaiaswestberg/gridlock@latest
```

## Usage

Create a configuration file (default is `.gridlock.yaml`) and run:

```bash
gridlock
```

### Initialization

Initialize a new configuration file:

```bash
gridlock init
```

To capture your current TMUX session into a configuration file:

```bash
gridlock init --save-current
```

### Options

- `-config, -f`: Path to the configuration file (default: `.gridlock.yaml`)
- `-detached, -d`: Create the session without attaching to it.
- `-recreate`: Kill any existing session with the same name before creating.
- `-dry-run`: Print the TMUX commands that would be executed without running them.

## Configuration

Gridlock uses a YAML structure to define your workspace. See the [.gridlock.example.yaml](.gridlock.example.yaml) for a complete example of how to structure your sessions, including nested layouts.

### Example Fragment

```yaml
session:
  name: "my-project"
  windows:
    - name: "dev"
      panes:
        - name: "editor"
          command: "nvim"
        - name: "server"
          commands:
             - "echo 'Starting server...'"
             - "npm run dev"
      layout:
        columns:
          - "editor"
          - "server"
```

## License

MIT
