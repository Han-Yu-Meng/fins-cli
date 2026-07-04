package commands

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"fins-cli/internal/utils"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"gopkg.in/yaml.v3"
)

type WorkspaceConfig struct {
	Name string `mapstructure:"name" yaml:"name"`
	Path string `mapstructure:"path" yaml:"path"`
}

type RepoConfig struct {
	URL     string `yaml:"url"`
	Version string `yaml:"version"`
	Type    string `yaml:"type,omitempty"`
}

type WorkspacePullConfig struct {
	Name  string                `yaml:"name"`
	Repos map[string]RepoConfig `yaml:"repos"`
}

var workspaceCmd = &cobra.Command{
	Use:   "workspace",
	Short: "Manage local build workspaces",
}

var workspaceAddCmd = &cobra.Command{
	Use:   "add [name]",
	Short: "Add current directory as a workspace",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		name := args[0]
		absPath, _ := filepath.Abs(".")

		var workspaces []WorkspaceConfig
		if err := viper.UnmarshalKey("local_packages", &workspaces); err != nil {
			utils.LogError(os.Stdout, "Failed to parse workspaces: %v", err)
			return
		}

		for _, ws := range workspaces {
			if ws.Name == name {
				utils.LogError(os.Stdout, "Workspace with name '%s' already exists (Path: %s)", name, ws.Path)
				return
			}
			if ws.Path == absPath {
				utils.LogError(os.Stdout, "This directory is already added as workspace '%s'", ws.Name)
				return
			}
		}

		workspaces = append(workspaces, WorkspaceConfig{Name: name, Path: absPath})
		viper.Set("local_packages", workspaces)

		if err := viper.WriteConfig(); err != nil {
			utils.LogError(os.Stdout, "Failed to save config: %v", err)
			return
		}

		utils.LogSuccess(os.Stdout, "Workspace '%s' added at %s", name, absPath)

		url := fmt.Sprintf("%s/api/scan", DaemonURL)
		resp, err := http.Post(url, "application/json", nil)
		if err != nil {
			utils.LogWarning(os.Stdout, "Added workspace, but failed to notify daemon for rescan: %v", err)
		} else {
			defer resp.Body.Close()
			if resp.StatusCode == 200 {
				utils.LogSuccess(os.Stdout, "Daemon triggered automatic scan.")
			} else {
				utils.LogWarning(os.Stdout, "Daemon returned %d on scan request.", resp.StatusCode)
			}
		}
	},
}

var workspaceListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all registered workspaces",
	Run: func(cmd *cobra.Command, args []string) {
		var workspaces []WorkspaceConfig
		if err := viper.UnmarshalKey("local_packages", &workspaces); err != nil {
			utils.LogError(os.Stdout, "Failed to parse workspaces: %v", err)
			return
		}

		if len(workspaces) == 0 {
			utils.LogWarning(os.Stdout, "No workspaces registered.")
			return
		}

		fmt.Println("Registered Workspaces:")
		fmt.Printf("%-20s %-50s\n", "Name", "Path")
		fmt.Println(strings.Repeat("-", 75))
		for _, ws := range workspaces {
			fmt.Printf("%-20s %-50s\n", ws.Name, ws.Path)
		}
	},
}

var workspaceRemoveCmd = &cobra.Command{
	Use:   "remove [name]",
	Short: "Remove a workspace from registration",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		name := args[0]
		var workspaces []WorkspaceConfig
		if err := viper.UnmarshalKey("local_packages", &workspaces); err != nil {
			utils.LogError(os.Stdout, "Failed to parse workspaces: %v", err)
			return
		}

		var newWorkspaces []WorkspaceConfig
		found := false
		for _, ws := range workspaces {
			if ws.Name == name {
				found = true
				continue
			}
			newWorkspaces = append(newWorkspaces, ws)
		}

		if !found {
			utils.LogError(os.Stdout, "Workspace '%s' not found.", name)
			return
		}

		viper.Set("local_packages", newWorkspaces)
		if err := viper.WriteConfig(); err != nil {
			utils.LogError(os.Stdout, "Failed to save config: %v", err)
			return
		}

		utils.LogSuccess(os.Stdout, "Workspace '%s' removed.", name)
	},
}

var workspacePullCmd = &cobra.Command{
	Use:   "pull",
	Short: "Pull repositories based on workspace.yaml",
	Run: func(cmd *cobra.Command, args []string) {
		yamlPath := "workspace.yaml"
		data, err := os.ReadFile(yamlPath)
		if err != nil {
			utils.LogError(os.Stdout, "Failed to read workspace.yaml: %v", err)
			return
		}

		var config WorkspacePullConfig
		if err := yaml.Unmarshal(data, &config); err != nil {
			utils.LogError(os.Stdout, "Failed to parse workspace.yaml: %v", err)
			return
		}

		if config.Name == "" {
			utils.LogError(os.Stdout, "Workspace name is required in workspace.yaml")
			return
		}

		absPath, _ := filepath.Abs(".")

		// Update .gitignore with repo paths
		gitignorePath := filepath.Join(absPath, ".gitignore")
		existingContent, _ := os.ReadFile(gitignorePath)
		contentStr := string(existingContent)

		f, err := os.OpenFile(gitignorePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			utils.LogError(os.Stdout, "Failed to open/create .gitignore: %v", err)
		} else {
			updated := false
			for repoPath := range config.Repos {
				if !strings.Contains(contentStr, repoPath) {
					if !updated && len(contentStr) > 0 && contentStr[len(contentStr)-1] != '\n' {
						f.WriteString("\n")
					}
					f.WriteString(repoPath + "\n")
					contentStr += repoPath + "\n"
					updated = true
				}
			}
			f.Close()
			if updated {
				utils.LogSuccess(os.Stdout, ".gitignore updated.")
			}
		}

		var workspaces []WorkspaceConfig
		if err := viper.UnmarshalKey("local_packages", &workspaces); err != nil {
			utils.LogError(os.Stdout, "Failed to parse workspaces: %v", err)
			return
		}

		found := false
		for i, ws := range workspaces {
			if ws.Path == absPath {
				workspaces[i].Name = config.Name
				found = true
				break
			}
		}

		if !found {
			// If name already exists at a different path, update its path
			updated := false
			for i, ws := range workspaces {
				if ws.Name == config.Name {
					workspaces[i].Path = absPath
					updated = true
					break
				}
			}
			if !updated {
				workspaces = append(workspaces, WorkspaceConfig{Name: config.Name, Path: absPath})
			}
		}

		viper.Set("local_packages", workspaces)
		if err := viper.WriteConfig(); err != nil {
			utils.LogError(os.Stdout, "Failed to save config: %v", err)
			return
		}

		utils.LogSuccess(os.Stdout, "Workspace '%s' registered/updated at %s", config.Name, absPath)

		// Clone/Pull repos
		for repoPath, repo := range config.Repos {
			fullPath := filepath.Join(absPath, repoPath)
			if _, err := os.Stat(fullPath); os.IsNotExist(err) {
				utils.LogSection(os.Stdout, "Cloning %s (%s)...", repoPath, repo.URL)
				// Ensure parent directories exist for nested paths
				parentDir := filepath.Dir(fullPath)
				if err := os.MkdirAll(parentDir, 0755); err != nil {
					utils.LogError(os.Stdout, "Failed to create directory for %s: %v", repoPath, err)
					continue
				}
				cloneCmd := exec.Command("git", "clone", "--recursive", repo.URL, fullPath)
				cloneCmd.Stdout = os.Stdout
				cloneCmd.Stderr = os.Stderr
				if err := cloneCmd.Run(); err != nil {
					utils.LogError(os.Stdout, "Failed to clone %s: %v", repoPath, err)
					continue
				}
				// Checkout specific version (works for branch, tag, and commit hash)
				if repo.Version != "" {
					checkoutCmd := exec.Command("git", "checkout", repo.Version)
					checkoutCmd.Dir = fullPath
					checkoutCmd.Stdout = os.Stdout
					checkoutCmd.Stderr = os.Stderr
					if err := checkoutCmd.Run(); err != nil {
						utils.LogError(os.Stdout, "Failed to checkout version '%s' for %s: %v", repo.Version, repoPath, err)
						continue
					}
				}
			} else {
				utils.LogSection(os.Stdout, "Updating %s...", repoPath)
				// Fetch latest refs and tags
				fetchCmd := exec.Command("git", "fetch", "--all", "--tags")
				fetchCmd.Dir = fullPath
				fetchCmd.Stdout = os.Stdout
				fetchCmd.Stderr = os.Stderr
				if err := fetchCmd.Run(); err != nil {
					utils.LogError(os.Stdout, "Failed to fetch %s: %v", repoPath, err)
					continue
				}
				// Checkout specific version
				if repo.Version != "" {
					checkoutCmd := exec.Command("git", "checkout", repo.Version)
					checkoutCmd.Dir = fullPath
					checkoutCmd.Stdout = os.Stdout
					checkoutCmd.Stderr = os.Stderr
					if err := checkoutCmd.Run(); err != nil {
						utils.LogError(os.Stdout, "Failed to checkout version '%s' for %s: %v", repo.Version, repoPath, err)
						continue
					}
				}
				// Try to pull (will fail gracefully for tags/commits)
				pullCmd := exec.Command("git", "pull", "--ff-only")
				pullCmd.Dir = fullPath
				pullCmd.Stdout = os.Stdout
				pullCmd.Stderr = os.Stderr
				pullCmd.Run()

				// Update submodules
				submoduleCmd := exec.Command("git", "submodule", "update", "--init", "--recursive")
				submoduleCmd.Dir = fullPath
				submoduleCmd.Stdout = os.Stdout
				submoduleCmd.Stderr = os.Stderr
				if err := submoduleCmd.Run(); err != nil {
					utils.LogWarning(os.Stdout, "Failed to update submodules for %s: %v", repoPath, err)
				}
			}
		}

		// Notify daemon
		url := fmt.Sprintf("%s/api/scan", DaemonURL)
		resp, err := http.Post(url, "application/json", nil)
		if err != nil {
			utils.LogWarning(os.Stdout, "Updated workspace, but failed to notify daemon for rescan: %v", err)
		} else {
			defer resp.Body.Close()
			if resp.StatusCode == 200 {
				utils.LogSuccess(os.Stdout, "Daemon triggered automatic scan.")
			}
		}
	},
}

var workspaceScanCmd = &cobra.Command{
	Use:   "scan",
	Short: "Manually trigger a scan of all registered workspaces",
	Run: func(cmd *cobra.Command, args []string) {
		url := fmt.Sprintf("%s/api/scan", DaemonURL)
		resp, err := http.Post(url, "application/json", nil)
		if err != nil {
			utils.LogError(os.Stdout, "Failed to connect to finsd: %v", err)
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode == 200 {
			utils.LogSuccess(os.Stdout, "Package scan triggered successfully.")
		} else {
			utils.LogError(os.Stdout, "Failed to trigger scan: Daemon returned %d", resp.StatusCode)
		}
	},
}

var captureUseHash bool

var workspaceCaptureCmd = &cobra.Command{
	Use:   "capture",
	Short: "Capture current workspace state into workspace.yaml",
	Long:  "Scan all git repos with valid fins package config and sync them to workspace.yaml",
	Run: func(cmd *cobra.Command, args []string) {
		absPath, _ := filepath.Abs(".")

		// Try to read existing workspace name from workspace.yaml
		workspaceName := filepath.Base(absPath)
		yamlPath := "workspace.yaml"
		if data, err := os.ReadFile(yamlPath); err == nil {
			var existing WorkspacePullConfig
			if yaml.Unmarshal(data, &existing) == nil && existing.Name != "" {
				workspaceName = existing.Name
			}
		}

		repos := make(map[string]RepoConfig)

		// Walk workspace to find package.yaml (same logic as scanner.go)
		filepath.WalkDir(absPath, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}

			// Skip hidden directories
			if d.IsDir() && strings.HasPrefix(d.Name(), ".") && d.Name() != "." {
				return filepath.SkipDir
			}

			// Skip build output directories
			if d.IsDir() && (d.Name() == "build" || d.Name() == "devel" || d.Name() == "install") {
				return filepath.SkipDir
			}

			// Found package.yaml or package.xml - this is a valid package
			if !d.IsDir() && (d.Name() == "package.yaml" || d.Name() == "package.xml") {
				repoDir := filepath.Dir(path)

				// Check if it's a git repo
				gitDir := filepath.Join(repoDir, ".git")
				if _, err := os.Stat(gitDir); os.IsNotExist(err) {
					return filepath.SkipDir
				}

				// Get relative path from workspace root
				relPath, err := filepath.Rel(absPath, repoDir)
				if err != nil || relPath == "." {
					return filepath.SkipDir
				}

				// Get remote URL
				urlCmd := exec.Command("git", "remote", "get-url", "origin")
				urlCmd.Dir = repoDir
				urlOutput, err := urlCmd.Output()
				if err != nil {
					utils.LogWarning(os.Stdout, "Failed to get remote URL for %s: %v", relPath, err)
					return filepath.SkipDir
				}
				remoteURL := strings.TrimSpace(string(urlOutput))

				// Get version (branch or hash)
				var version string
				if captureUseHash {
					hashCmd := exec.Command("git", "rev-parse", "HEAD")
					hashCmd.Dir = repoDir
					hashOutput, err := hashCmd.Output()
					if err != nil {
						utils.LogWarning(os.Stdout, "Failed to get commit hash for %s: %v", relPath, err)
						return filepath.SkipDir
					}
					version = strings.TrimSpace(string(hashOutput))
				} else {
					branchCmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
					branchCmd.Dir = repoDir
					branchOutput, err := branchCmd.Output()
					if err != nil {
						utils.LogWarning(os.Stdout, "Failed to get branch name for %s: %v", relPath, err)
						return filepath.SkipDir
					}
					version = strings.TrimSpace(string(branchOutput))
				}

				repos[relPath] = RepoConfig{
					URL:     remoteURL,
					Version: version,
				}

				utils.LogSuccess(os.Stdout, "Captured %s (%s @ %s)", relPath, remoteURL, version)
				return filepath.SkipDir
			}

			return nil
		})

		if len(repos) == 0 {
			utils.LogWarning(os.Stdout, "No valid packages found in workspace.")
			return
		}

		// Build workspace.yaml content
		config := WorkspacePullConfig{
			Name:  workspaceName,
			Repos: repos,
		}

		// Marshal to YAML
		yamlData, err := yaml.Marshal(config)
		if err != nil {
			utils.LogError(os.Stdout, "Failed to marshal workspace config: %v", err)
			return
		}

		// Write to file
		if err := os.WriteFile(yamlPath, yamlData, 0644); err != nil {
			utils.LogError(os.Stdout, "Failed to write workspace.yaml: %v", err)
			return
		}

		utils.LogSuccess(os.Stdout, "workspace.yaml updated with %d repositories.", len(repos))
	},
}

func init() {
	workspaceCaptureCmd.Flags().BoolVar(&captureUseHash, "hash", false, "Use commit hash instead of branch name")
	workspaceCmd.AddCommand(workspaceAddCmd, workspaceListCmd, workspaceRemoveCmd, workspaceScanCmd, workspacePullCmd, workspaceCaptureCmd)
	RootCmd.AddCommand(workspaceCmd)
}
