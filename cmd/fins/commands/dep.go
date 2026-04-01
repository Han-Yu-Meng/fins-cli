// cmd/fins/commands/dep.go

package commands

import (
	"bytes"
	"encoding/json"
	"finsd/cmd/fins/client"
	"finsd/internal/types"
	"finsd/internal/utils"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"
)

var depClearCache bool

var depCmd = &cobra.Command{
	Use:   "dep",
	Short: "Manage package dependencies",
	Long:  `Build and solve dependencies for FINS packages.`,
}

var depBuildCmd = &cobra.Command{
	Use:   "build [Library]=[Version]",
	Short: "Manually trigger a dependency build on the daemon",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		parts := strings.Split(args[0], "=")
		if len(parts) != 2 {
			utils.LogError(os.Stdout, "Invalid format. Use Library=Version")
			return
		}

		reqBody := map[string]interface{}{
			"library": parts[0],
			"version": parts[1],
			"clear":   depClearCache,
		}
		jsonBody, _ := json.Marshal(reqBody)

		url := fmt.Sprintf("%s/api/dep/build", DaemonURL)
		resp, err := http.Post(url, "application/json", bytes.NewBuffer(jsonBody))
		if err != nil {
			utils.LogError(os.Stdout, "Error connecting to finsd: %v", err)
			return
		}
		defer resp.Body.Close()

		client.StreamResponse(resp.Body)
	},
}

var depSolveCmd = &cobra.Command{
	Use:   "solve [PackageName]",
	Short: "Resolve and build all dependencies for a specific package",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		pkgName, err := client.ResolvePackageIdentity(DaemonURL, args[0], targetSource)
		if err != nil {
			utils.LogError(os.Stdout, "%v", err)
			return
		}

		urlMeta := fmt.Sprintf("%s/api/package/detail/%s", DaemonURL, pkgName)
		respMeta, err := http.Get(urlMeta)
		if err != nil {
			utils.LogError(os.Stdout, "Error connecting to finsd: %v", err)
			return
		}
		defer respMeta.Body.Close()

		if respMeta.StatusCode != 200 {
			utils.LogError(os.Stdout, "Package '%s' not found on daemon.", pkgName)
			return
		}

		var details types.PackageDetails
		if err := json.NewDecoder(respMeta.Body).Decode(&details); err != nil {
			utils.LogError(os.Stdout, "Failed to decode package details: %v", err)
			return
		}

		deps := details.Meta.Depends
		localRecipes := details.Meta.Recipes
		var systemDeps []string

		for lib, ver := range deps {
			isSystemMarked := (ver == "system")

			sysPkgName := resolveSystemPackage(lib, localRecipes)

			if sysPkgName != "" {
				pkgs := strings.Fields(sysPkgName)
				systemDeps = append(systemDeps, pkgs...)
			} else if isSystemMarked {
				utils.LogWarning(os.Stdout, "Dependency '%s' is marked as system but no system_pkg mapping found.", lib)
			}
		}

		if len(systemDeps) > 0 {
			if os.Geteuid() != 0 {
				utils.LogWarning(os.Stdout, "System dependencies detected: %v", systemDeps)
				utils.LogError(os.Stdout, "You must run this command with 'sudo' to install system packages.")
				return
			}

			utils.LogSection(os.Stdout, "Installing system packages: %v", systemDeps)
			aptArgs := append([]string{"install", "-y"}, systemDeps...)

			cmd := exec.Command("apt-get", aptArgs...)
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			cmd.Env = append(os.Environ(), "DEBIAN_FRONTEND=noninteractive")

			if err := cmd.Run(); err != nil {
				utils.LogError(os.Stdout, "Failed to install system packages: %v", err)
				return
			}
			utils.LogSuccess(os.Stdout, "System dependencies installed")
		}

		url := fmt.Sprintf("%s/api/dep/solve/%s?clear=%t", DaemonURL, pkgName, depClearCache)
		utils.LogSection(os.Stdout, "Requesting dependency resolution for %s (Clear Cache: %v)", pkgName, depClearCache)
		resp, err := http.Post(url, "application/json", nil)
		if err != nil {
			utils.LogError(os.Stdout, "Error connecting to finsd: %v", err)
			return
		}
		defer resp.Body.Close()
		client.StreamResponse(resp.Body)
	},
}

func resolveSystemPackage(libName string, localRecipes map[string]types.DependencyRecipe) string {
	var pkgName string

	if recipe, ok := localRecipes[libName]; ok {
		if recipe.SystemPackage != "" {
			pkgName = recipe.SystemPackage
		}
	}

	if pkgName == "" {
		pkgName = fetchGlobalSystemPackageName(libName)
	}

	if pkgName != "" && strings.Contains(pkgName, "${ROS_DISTRO}") {
		rosDistro := os.Getenv("ROS_DISTRO")
		if rosDistro == "" {
			if _, err := os.Stat("/opt/ros/jazzy"); err == nil {
				rosDistro = "jazzy"
			} else if _, err := os.Stat("/opt/ros/humble"); err == nil {
				rosDistro = "humble"
			} else {
				rosDistro = "jazzy"
			}
		}
		pkgName = strings.ReplaceAll(pkgName, "${ROS_DISTRO}", rosDistro)
	}

	return pkgName
}

func fetchGlobalSystemPackageName(libName string) string {
	resp, err := http.Get(fmt.Sprintf("%s/api/recipe/%s", DaemonURL, libName))
	if err != nil || resp.StatusCode != 200 {
		return ""
	}
	defer resp.Body.Close()

	var recipe types.DependencyRecipe
	if err := json.NewDecoder(resp.Body).Decode(&recipe); err != nil {
		return ""
	}

	return recipe.SystemPackage
}

func init() {
	depBuildCmd.Flags().BoolVar(&depClearCache, "clear", false, "Clear build cache before building")
	depSolveCmd.Flags().BoolVar(&depClearCache, "clear", false, "Clear build cache for all dependencies")
	depSolveCmd.Flags().StringVar(&targetSource, "source", "", "Specify package source to resolve ambiguity")

	depCmd.AddCommand(depBuildCmd, depSolveCmd)
	RootCmd.AddCommand(depCmd)
}
