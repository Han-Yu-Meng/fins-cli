# FINS CLI

FINS (Framework for Intelligent/Natural Systems) is a CLI tool and daemon service designed for intelligent system management, agent building, and inspection.

## Table of Contents

- [Features](#features)
- [Prerequisites](#prerequisites)
- [Installation](#installation)
- [Getting Started](#getting-started)
- [Uninstallation](#uninstallation)
- [Directory Structure](#directory-structure)

## Features

- **Daemon Service**: `finsd` runs in the background to handle asynchronous tasks.
- **Agent Management**: Build and manage intelligent agents.
- **Code Inspection**: Inspect and analyze your codebase.
- **Easy Installation**: One-click setup script with systemd integration.

## Prerequisites

- **OS**: Linux (tested on Ubuntu 22.04+)
- **Sudo Access**: Required for system-wide installation and service management.
- **Network**: Internet access to download binaries from GitHub and register configuration.

## Installation

To install FINS CLI, run the provided installation script:

```bash
curl -fsSL https://raw.githubusercontent.com/Han-Yu-Meng/fins-cli/main/install.sh | sudo bash
```

### Build Internal Tools

For the first time, you need to build the necessary tools for Agent and Inspect functionality:

```bash
fins agent build
fins inspect build
```

### Help

View all available commands:

```bash
fins --help
```

## Uninstallation

To completely remove FINS CLI from your system:

```bash
curl -fsSL https://raw.githubusercontent.com/Han-Yu-Meng/fins-cli/main/uninstall.sh | sudo bash
```

## Directory Structure

- `cmd/`: CLI and Daemon source code.
- `internal/`: Core logic for agents, server, and utils.
- `default/`: Default configuration files and recipes.
- `~/.fins/`: User-specific configuration and logs (post-installation).