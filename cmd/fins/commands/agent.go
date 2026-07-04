package commands

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"fins-cli/internal/agent"
	"fins-cli/internal/core"
	"fins-cli/internal/utils"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var (
	agentName      string
	agentIP        string
	agentPort      int
	agentHeaptrack bool
)

var agentCmd = &cobra.Command{
	Use:   "agent",
	Short: "Manage the fins agent",
}

var agentBuildCmd = &cobra.Command{
	Use:   "build",
	Short: "Build the fins agent binary",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		err := core.CompileAgent(context.Background(), os.Stdout)
		if err != nil {
			utils.LogError(os.Stdout, "Build failed: %v", err)
			return
		}
	},
}

var agentRunCmd = &cobra.Command{
	Use:   "run",
	Short: "Run the fins agent in foreground",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		runAgentLocal(false)
	},
}

var agentDebugCmd = &cobra.Command{
	Use:   "debug",
	Short: "Debug the fins agent with GDB in foreground",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		runAgentLocal(true)
	},
}

func runAgentLocal(debug bool) {
	// Read workspace paths from config
	type LocalSource struct {
		Name string `mapstructure:"name"`
		Path string `mapstructure:"path"`
	}
	var localSources []LocalSource
	viper.UnmarshalKey("local_packages", &localSources)

	var workspacePaths []string
	for _, src := range localSources {
		workspacePaths = append(workspacePaths, src.Path)
	}

	cfg := agent.AgentConfig{
		AgentName:  agentName,
		AgentIP:    agentIP,
		AgentPort:  agentPort,
		LogLevel:   1,
		Workspaces: workspacePaths,
	}

	err := agent.GlobalManager.Start(cfg, debug, os.Stdout, agentHeaptrack)
	if err != nil {
		utils.LogError(os.Stdout, "Failed to start agent: %v", err)
		return
	}

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)

	<-sigChan

	fmt.Printf("\n")
	utils.LogWarning(os.Stdout, "Interrupt received. Attempting graceful shutdown (timeout 5s)...")

	running, pid, _ := agent.GlobalManager.GetStatus(cfg.AgentName)
	if running && pid > 0 {
		syscall.Kill(-pid, syscall.SIGTERM)
	}

	done := make(chan bool)
	go func() {
		for {
			isStillRunning, _, _ := agent.GlobalManager.GetStatus(cfg.AgentName)
			if !isStillRunning {
				done <- true
				return
			}
			time.Sleep(200 * time.Millisecond)
		}
	}()

	select {
	case <-done:
		utils.LogSuccess(os.Stdout, "Agent stopped gracefully.")
	case <-time.After(5 * time.Second):
		utils.LogWarning(os.Stdout, "Shutdown timed out. Performing force stop...")
		if err := agent.GlobalManager.Stop(cfg.AgentName); err != nil {
			utils.LogError(os.Stdout, "Error force stopping agent: %v", err)
		}
		utils.LogSuccess(os.Stdout, "Agent force stopped.")
	}

	utils.LogSuccess(os.Stdout, "Exit.")
}

func init() {
	agentRunCmd.Flags().StringVarP(&agentName, "name", "n", "agent", "Set agent name")
	agentRunCmd.Flags().StringVarP(&agentIP, "ip", "I", "0.0.0.0", "Set agent IP binding")
	agentRunCmd.Flags().IntVarP(&agentPort, "port", "P", 9090, "Set agent listening port")
	agentRunCmd.Flags().BoolVarP(&agentHeaptrack, "heaptrack", "", false, "Enable heaptrack memory profiling")

	agentDebugCmd.Flags().StringVarP(&agentName, "name", "n", "agent", "Set agent name")
	agentDebugCmd.Flags().StringVarP(&agentIP, "ip", "I", "0.0.0.0", "Set agent IP binding")
	agentDebugCmd.Flags().IntVarP(&agentPort, "port", "P", 9090, "Set agent listening port")

	agentCmd.AddCommand(agentBuildCmd)
	agentCmd.AddCommand(agentRunCmd)
	agentCmd.AddCommand(agentDebugCmd)
	RootCmd.AddCommand(agentCmd)
}
