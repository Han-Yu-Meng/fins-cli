package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"fins-cli/cmd/fins/client"
	"fins-cli/internal/types"
	"fins-cli/internal/utils"

	"github.com/fatih/color"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"gopkg.in/yaml.v3"
)

var (
	buildAll     bool
	buildClear   bool
	targetSource string
)

var buildCmd = &cobra.Command{
	Use:   "build [package]",
	Short: "Request daemon to build a package",
	ValidArgsFunction: func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		if len(args) != 0 {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}

		pkgs, err := client.FetchPackageList(DaemonURL)
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}

		var suggestions []string
		for _, p := range pkgs {
			suggestions = append(suggestions, p.Name)
		}
		return suggestions, cobra.ShellCompDirectiveNoFileComp
	},
	Args: func(cmd *cobra.Command, args []string) error {
		if buildAll {
			if len(args) > 0 {
				return fmt.Errorf("cannot use arguments with --all")
			}
			return nil
		}
		if len(args) == 0 {
			return nil
		}
		if len(args) != 1 {
			return fmt.Errorf("accepts 1 arg(s), received %d", len(args))
		}
		return nil
	},
	Run: func(cmd *cobra.Command, args []string) {
		// --- Phase 1: Resolve scope ---
		var targetPkg string
		var workspacePath string
		if len(args) == 0 || args[0] == "." {
			absPath, _ := filepath.Abs(".")

			// 1. Try to find the package by current directory path directly from daemon
			pkgs, err := client.FetchPackageList(DaemonURL)
			if err == nil {
				for _, p := range pkgs {
					if p.Path == absPath {
						targetPkg = p.Name
						break
					}
				}
			}

			// 2. If not found by path, check for package.yaml
			if targetPkg == "" {
				if _, err := os.Stat("package.yaml"); err == nil {
					data, err := os.ReadFile("package.yaml")
					if err == nil {
						var pkgMeta struct {
							Package struct {
								Name string `yaml:"name"`
							} `yaml:"package"`
							Name string `yaml:"name"`
						}
						if err := yaml.Unmarshal(data, &pkgMeta); err == nil {
							if pkgMeta.Package.Name != "" {
								targetPkg = pkgMeta.Package.Name
							} else if pkgMeta.Name != "" {
								targetPkg = pkgMeta.Name
							}
						}
					}
				}
			}

			// 3. Check for workspace.yaml
			if targetPkg == "" {
				if _, err := os.Stat("workspace.yaml"); err == nil {
					buildAll = true
					workspacePath = absPath
				}
			}

			// 4. Existing logic for registered workspaces
			if targetPkg == "" && !buildAll {
				var workspaces []WorkspaceConfig
				if err := viper.UnmarshalKey("local_packages", &workspaces); err == nil {
					for _, ws := range workspaces {
						if ws.Path == absPath {
							buildAll = true
							workspacePath = absPath
							break
						}
					}
				}
			}

			if !buildAll && targetPkg == "" {
				if len(args) == 0 {
					utils.LogError(os.Stdout, "Package name required or run in a registered workspace.")
					return
				}
				targetPkg = args[0]
			}
		} else {
			targetPkg = args[0]
		}

		// --- Phase 2: Handle --clear based on resolved scope ---
		if buildClear {
			var cleanBody map[string]string
			if buildAll && workspacePath != "" {
				cleanBody = map[string]string{"workspace": workspacePath}
			} else if targetPkg != "" {
				cleanBody = map[string]string{"target": targetPkg}
			}

			var reqBody io.Reader
			if cleanBody != nil {
				data, _ := json.Marshal(cleanBody)
				reqBody = bytes.NewBuffer(data)
			}

			url := fmt.Sprintf("%s/api/clean", DaemonURL)
			resp, err := http.Post(url, "application/json", reqBody)
			if err != nil {
				utils.LogError(os.Stdout, "Failed to connect to finsd: %v", err)
				return
			}
			defer resp.Body.Close()

			if resp.StatusCode == 200 {
				var msg struct{ Message string `json:"message"` }
				json.NewDecoder(resp.Body).Decode(&msg)
				utils.LogSuccess(os.Stdout, "%s", msg.Message)
			} else {
				respBody, _ := io.ReadAll(resp.Body)
				utils.LogError(os.Stdout, "Failed to clean cache: %s", string(respBody))
				return
			}

			if !buildAll && targetPkg == "" {
				return
			}
		}

		// --- Phase 3: Execute build ---
		if buildAll {
			pkgs, err := client.FetchPackageList(DaemonURL)
			if err != nil {
				utils.LogError(os.Stdout, "Failed to connect to daemon: %v", err)
				return
			}

			if workspacePath != "" {
				var filtered []types.PackageInfo
				for _, p := range pkgs {
					rel, err := filepath.Rel(workspacePath, p.Path)
					if err == nil && !strings.HasPrefix(rel, "..") {
						filtered = append(filtered, p)
					}
				}
				pkgs = filtered
			}

			if len(pkgs) == 0 {
				utils.LogWarning(os.Stdout, "No packages found to build.")
				return
			}

			// Check if this is a mixed workspace (contains ROS2 packages)
			hasROS2 := false
			for _, p := range pkgs {
				if p.Type == "ros2" {
					hasROS2 = true
					break
				}
			}

			// For mixed workspaces, use the workspace build endpoint
			// which orchestrates fins + colcon builds
			if hasROS2 && workspacePath != "" {
				body := map[string]string{"workspace": workspacePath}
				data, _ := json.Marshal(body)
				url := fmt.Sprintf("%s/api/build-workspace", DaemonURL)
				resp, err := http.Post(url, "application/json", bytes.NewBuffer(data))
				if err != nil {
					utils.LogError(os.Stdout, "Failed to connect to daemon: %v", err)
					return
				}
				defer resp.Body.Close()
				client.StreamResponse(resp.Body)
				return
			}

			n := len(pkgs)
			type taskState struct {
				status    string
				startTime time.Time
				endTime   time.Time
			}
			states := make([]taskState, n)
			for i := range states {
				states[i].status = "Pending ⏳"
			}

			var mu sync.Mutex
			updateStatus := func(idx int, status string) {
				mu.Lock()
				states[idx].status = status
				if strings.Contains(status, "Building") {
					states[idx].startTime = time.Now()
				} else if strings.Contains(status, "Success") || strings.Contains(status, "Failed") {
					states[idx].endTime = time.Now()
				}
				mu.Unlock()
			}

			done := make(chan struct{})

			sigChan := make(chan os.Signal, 1)
			signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
			ctx, cancel := context.WithCancel(context.Background())

			go func() {
				select {
				case <-done:
					return
				case <-sigChan:
					cancel()
					return
				}
			}()

			var wg sync.WaitGroup
			sem := make(chan struct{}, MaxConcurrentBuilds)

			var errMu sync.Mutex
			var errorLogs []string

			for i, p := range pkgs {
				wg.Add(1)
				go func(idx int, pkgName string) {
					defer wg.Done()

					select {
					case sem <- struct{}{}:
					case <-ctx.Done():
						updateStatus(idx, color.YellowString("Cancelled ⚠"))
						fmt.Printf("[%s] - %s\n", pkgName, color.YellowString("Cancelled"))
						return
					}

					updateStatus(idx, color.CyanString("Building 🚀"))
					fmt.Printf("[%s] - %s\n", pkgName, color.CyanString("Building..."))

					url := fmt.Sprintf("%s/api/build/%s", DaemonURL, pkgName)

					req, _ := http.NewRequestWithContext(ctx, "POST", url, nil)
					resp, err := http.DefaultClient.Do(req)

					defer func() { <-sem }()

					if err != nil {
						if ctx.Err() != nil {
							updateStatus(idx, color.YellowString("Cancelled ⚠"))
						} else {
							updateStatus(idx, color.RedString("Request Failed ✘"))
							fmt.Printf("[%s] - %s\n", pkgName, color.RedString("Request Failed"))
							errMu.Lock()
							errorLogs = append(errorLogs, fmt.Sprintf(">>> Package: %s (Request Failed)\n%v\n", pkgName, err))
							errMu.Unlock()
						}
						return
					}
					defer resp.Body.Close()

					output, _ := io.ReadAll(resp.Body)
					outStr := string(output)

					mu.Lock()
					s := states[idx]
					elapsed := ""
					if !s.startTime.IsZero() {
						d := time.Since(s.startTime)
						elapsed = fmt.Sprintf("%.1fs", d.Seconds())
					}
					mu.Unlock()

					if strings.Contains(outStr, "[ERROR]") {
						updateStatus(idx, color.RedString("Failed ✘"))
						fmt.Printf("[%s] - %s (%s)\n", pkgName, color.RedString("Failed"), elapsed)
						errMu.Lock()
						errorLogs = append(errorLogs, fmt.Sprintf(">>> Package: %s (Build Failed)\n%s\n", pkgName, outStr))
						errMu.Unlock()
					} else {
						updateStatus(idx, color.GreenString("Success ✔"))
						fmt.Printf("[%s] - %s (%s)\n", pkgName, color.GreenString("Success"), elapsed)
					}
				}(i, p.Name)
			}

			wg.Wait()
			close(done)

			if len(errorLogs) > 0 {
				utils.LogError(os.Stdout, "Errors Encountered:")
				for _, log := range errorLogs {
					fmt.Println(log)
					fmt.Println(strings.Repeat("-", 40))
				}
				utils.LogError(os.Stdout, "Tasks completed with errors")
			} else {
				if ctx.Err() != nil {
					utils.LogWarning(os.Stdout, "Build interrupted by user")
				} else {
					utils.LogSuccess(os.Stdout, "All tasks completed successfully")
				}
			}
			return
		}

		finalPkg, err := client.ResolvePackageIdentity(DaemonURL, targetPkg, targetSource)
		if err != nil {
			utils.LogError(os.Stdout, "%v", err)
			return
		}

		url := fmt.Sprintf("%s/api/build/%s", DaemonURL, finalPkg)

		resp, err := http.Post(url, "application/json", nil)
		if err != nil {
			utils.LogError(os.Stdout, "Error connecting to finsd: %v", err)
			return
		}
		defer resp.Body.Close()

		client.StreamResponse(resp.Body)
	},
}

func init() {
	buildCmd.Flags().BoolVar(&buildAll, "all", false, "Build all packages in parallel")
	buildCmd.Flags().BoolVar(&buildClear, "clear", false, "Clear build caches before building (respects build scope)")
	buildCmd.Flags().StringVar(&targetSource, "source", "", "Specify package source to resolve ambiguity")
	RootCmd.AddCommand(buildCmd)
}
