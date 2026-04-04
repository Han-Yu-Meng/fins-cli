// finsd/cmd/fins/commands/agent.go

package commands

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"

	"finsd/cmd/fins/client"
	"finsd/internal/agent"
	"finsd/internal/utils"

	"github.com/spf13/cobra"
)

var (
	agentName string
	agentIP   string
	agentPort int
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
		url := fmt.Sprintf("%s/api/agent/build", DaemonURL)

		utils.LogSection(os.Stdout, "Requesting agent build")
		resp, err := http.Post(url, "application/json", nil)
		if err != nil {
			utils.LogError(os.Stdout, "Error connecting to finsd: %v", err)
			return
		}
		defer resp.Body.Close()

		client.StreamResponse(resp.Body)
	},
}

var agentRunCmd = &cobra.Command{
	Use:   "run",
	Short: "Run the fins agent",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		runAgent(false)
	},
}

var agentDebugCmd = &cobra.Command{
	Use:   "debug",
	Short: "Debug the fins agent with GDB",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		runAgent(true)
	},
}

func runAgent(debug bool) {
	url := fmt.Sprintf("%s/api/agent/run", DaemonURL)
	if debug {
		url = fmt.Sprintf("%s/api/agent/debug", DaemonURL)
	}

	cfg := agent.AgentConfig{
		AgentName:     agentName,
		AgentIP:       agentIP,
		AgentPort:     agentPort,
		UrgentThreads: 4,
		HighThreads:   4,
		MediumThreads: 4,
		LowThreads:    4,
		LogLevel:      1,
	}

	body, _ := json.Marshal(cfg)
	resp, err := http.Post(url, "application/json", bytes.NewBuffer(body))
	if err != nil {
		utils.LogError(os.Stdout, "Error connecting to finsd: %v", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var errResp map[string]string
		json.NewDecoder(resp.Body).Decode(&errResp)
		utils.LogError(os.Stdout, "Failed to start agent: %s", errResp["error"])
		return
	}

	client.StreamResponse(resp.Body)
}

func init() {
	agentRunCmd.Flags().StringVarP(&agentName, "name", "n", "agent", "Set agent name")
	agentRunCmd.Flags().StringVarP(&agentIP, "ip", "I", "0.0.0.0", "Set agent IP binding")
	agentRunCmd.Flags().IntVarP(&agentPort, "port", "P", 9090, "Set agent listening port")

	agentDebugCmd.Flags().StringVarP(&agentName, "name", "n", "agent", "Set agent name")
	agentDebugCmd.Flags().StringVarP(&agentIP, "ip", "I", "0.0.0.0", "Set agent IP binding")
	agentDebugCmd.Flags().IntVarP(&agentPort, "port", "P", 9090, "Set agent listening port")

	agentCmd.AddCommand(agentBuildCmd)
	agentCmd.AddCommand(agentRunCmd)
	agentCmd.AddCommand(agentDebugCmd)
	RootCmd.AddCommand(agentCmd)
}
