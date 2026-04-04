package agent

import (
	"finsd/internal/utils"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/viper"
)

type AgentConfig struct {
	UrgentThreads int         `json:"urgent_threads"`
	HighThreads   int         `json:"high_threads"`
	MediumThreads int         `json:"medium_threads"`
	LowThreads    int         `json:"low_threads"`
	LogLevel      int         `json:"log_level"`
	Plugins       []PluginReq `json:"plugins"`
	AgentName     string      `json:"agent_name"`
	AgentIP       string      `json:"agent_ip"`
	AgentPort     int         `json:"agent_port"`
}

type PluginReq struct {
	Name   string `json:"name"`
	Source string `json:"source"`
}

type AgentInstance struct {
	Name      string
	Config    AgentConfig
	cmd       *exec.Cmd
	logFile   *os.File
	isRunning bool
	mu        sync.Mutex
	pid       int
}

type AgentManager struct {
	agents map[string]*AgentInstance
	mu     sync.RWMutex
}

var GlobalManager = &AgentManager{
	agents: make(map[string]*AgentInstance),
}

func (m *AgentManager) Start(cfg AgentConfig, debug bool, stdout *os.File) error {
	if cfg.AgentName == "" {
		return fmt.Errorf("agent_name is required")
	}

	m.mu.Lock()

	for name, inst := range m.agents {
		inst.mu.Lock()
		isRunning := inst.isRunning
		usedPort := inst.Config.AgentPort
		inst.mu.Unlock()

		if isRunning {
			if name == cfg.AgentName {
				m.mu.Unlock()
				return fmt.Errorf("agent '%s' is already running", name)
			}
			if usedPort == cfg.AgentPort {
				m.mu.Unlock()
				return fmt.Errorf("port %d is already in use by running agent '%s'", usedPort, name)
			}
		}
	}

	instance, exists := m.agents[cfg.AgentName]
	if !exists {
		instance = &AgentInstance{Name: cfg.AgentName}
		m.agents[cfg.AgentName] = instance
	}
	m.mu.Unlock()

	return instance.Start(cfg, debug, stdout)
}

func (m *AgentManager) Stop(name string) error {
	m.mu.RLock()
	instance, exists := m.agents[name]
	m.mu.RUnlock()

	if !exists {
		return fmt.Errorf("agent '%s' not found", name)
	}

	return instance.Stop()
}

func (m *AgentManager) GetStatus(name string) (bool, int, error) {
	m.mu.RLock()
	instance, exists := m.agents[name]
	m.mu.RUnlock()

	if !exists {
		return false, 0, fmt.Errorf("agent '%s' not found", name)
	}
	return instance.isRunning, instance.pid, nil
}

func (m *AgentManager) GetAllStatus() map[string]struct {
	Running bool
	Pid     int
} {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make(map[string]struct {
		Running bool
		Pid     int
	})

	for name, instance := range m.agents {
		instance.mu.Lock()
		result[name] = struct {
			Running bool
			Pid     int
		}{
			Running: instance.isRunning,
			Pid:     instance.pid,
		}
		instance.mu.Unlock()
	}
	return result
}

func (ag *AgentInstance) Start(cfg AgentConfig, debug bool, stdout *os.File) error {
	ag.mu.Lock()
	defer ag.mu.Unlock()

	if ag.isRunning {
		if ag.cmd != nil && ag.cmd.Process != nil {
			if err := ag.cmd.Process.Signal(syscall.Signal(0)); err == nil {
				return fmt.Errorf("agent '%s' is already running (PID: %d)", ag.Name, ag.pid)
			}
		}
		ag.isRunning = false
	}

	binDir := viper.GetString("build.defaults.build_output")
	agentBin := utils.ExpandPath(filepath.Join(binDir, "agent"))
	if _, err := os.Stat(agentBin); os.IsNotExist(err) {
		return fmt.Errorf("agent binary not found at %s. Please compile it first", agentBin)
	}

	args := []string{
		"--threads-urgent", fmt.Sprintf("%d", cfg.UrgentThreads),
		"--threads-high", fmt.Sprintf("%d", cfg.HighThreads),
		"--threads-medium", fmt.Sprintf("%d", cfg.MediumThreads),
		"--threads-low", fmt.Sprintf("%d", cfg.LowThreads),
		"--log-level", fmt.Sprintf("%d", cfg.LogLevel),
		"--name", cfg.AgentName,
		"--ip", cfg.AgentIP,
		"--port", fmt.Sprintf("%d", cfg.AgentPort),
		"--load-all",
	}

	webUrl := viper.GetString("webui.host")
	if webUrl == "" {
		webUrl = "http://localhost:8080"
	}
	args = append(args, "--webui", webUrl)

	for _, p := range cfg.Plugins {
		soName := fmt.Sprintf("lib%s_%s.so", p.Source, p.Name)
		soPath := utils.ExpandPath(filepath.Join(binDir, soName))
		if _, err := os.Stat(soPath); err == nil {
			args = append(args, "--plugin", soPath)
		} else {
			utils.LogWarning(os.Stdout, "Plugin %s/%s not found at %s", p.Source, p.Name, soPath)
		}
	}

	var cmd *exec.Cmd
	if debug {
		gdbArgs := append([]string{"-ex", "run", "--args", agentBin}, args...)
		cmd = exec.Command("gdb", gdbArgs...)
	} else {
		cmd = exec.Command(agentBin, args...)
	}

	if stdout != nil {
		ag.logFile = nil
		cmd.Stdout = stdout
		cmd.Stderr = stdout
	} else {
		logPath := filepath.Join(utils.GetLogDir(), fmt.Sprintf("agent_%s.log", ag.Name))
		f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
		if err != nil {
			return fmt.Errorf("failed to open log file: %v", err)
		}
		ag.logFile = f
		cmd.Stdout = f
		cmd.Stderr = f
	}

	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	utils.LogSection(os.Stdout, "[%s] Starting agent (debug=%v): %v", ag.Name, debug, cmd.Args)
	if err := cmd.Start(); err != nil {
		if ag.logFile != nil {
			ag.logFile.Close()
		}
		return err
	}

	ag.cmd = cmd
	ag.isRunning = true
	ag.pid = cmd.Process.Pid
	ag.Config = cfg

	go func() {
		state, _ := cmd.Process.Wait()

		ag.mu.Lock()
		defer ag.mu.Unlock()

		ag.isRunning = false
		ag.pid = 0

		timestamp := time.Now().Format(time.RFC3339)
		if ag.logFile != nil {
			if state != nil {
				ag.logFile.WriteString(fmt.Sprintf("\n[%s] Agent exited with code %d\n", timestamp, state.ExitCode()))
			} else {
				ag.logFile.WriteString(fmt.Sprintf("\n[%s] Agent exited unknown state\n", timestamp))
			}
			ag.logFile.Close()
			ag.logFile = nil
		}
	}()

	return nil
}

func (ag *AgentInstance) Stop() error {
	ag.mu.Lock()
	if !ag.isRunning || ag.cmd == nil || ag.cmd.Process == nil {
		ag.mu.Unlock()
		return fmt.Errorf("agent is not running")
	}

	proc := ag.cmd.Process
	ag.mu.Unlock()

	utils.LogWarning(os.Stdout, "[%s] Force killing agent process group (PGID: %d)", ag.Name, proc.Pid)
	if err := syscall.Kill(-proc.Pid, syscall.SIGKILL); err != nil {
		return proc.Kill()
	}

	return nil
}
