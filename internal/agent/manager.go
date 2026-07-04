package agent

import (
	"fins-cli/internal/utils"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/viper"
)

type AgentConfig struct {
	LogLevel   int      `json:"log_level"`
	Workspaces []string `json:"workspaces"`
	AgentName  string   `json:"agent_name"`
	AgentIP    string   `json:"agent_ip"`
	AgentPort  int      `json:"agent_port"`
}

type AgentInstance struct {
	Name      string
	Config    AgentConfig
	cmd       *exec.Cmd
	logFile   *os.File
	isRunning bool
	mu        sync.Mutex
	pid       int
	lockFile  *os.File
}

type AgentManager struct {
	agents map[string]*AgentInstance
	mu     sync.RWMutex
}

var GlobalManager = &AgentManager{
	agents: make(map[string]*AgentInstance),
}

func isPortInUse(port int) bool {
	address := fmt.Sprintf(":%d", port)
	ln, err := net.Listen("tcp", address)
	if err != nil {
		return true
	}
	ln.Close()
	return false
}

func (m *AgentManager) Start(cfg AgentConfig, debug bool, stdout *os.File, heaptrack bool) error {
	if cfg.AgentName == "" {
		return fmt.Errorf("agent_name is required")
	}

	if isPortInUse(cfg.AgentPort) {
		return fmt.Errorf("port %d is already in use by another process", cfg.AgentPort)
	}

	lockDir := filepath.Join(utils.GetFinsHome(), "install")
	os.MkdirAll(lockDir, 0755)
	lockPath := filepath.Join(lockDir, fmt.Sprintf("agent_%s.lock", cfg.AgentName))

	f, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE, 0666)
	if err != nil {
		return fmt.Errorf("failed to create lock file: %v", err)
	}

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return fmt.Errorf("agent '%s' is already running in another terminal", cfg.AgentName)
	}

	m.mu.Lock()
	instance, exists := m.agents[cfg.AgentName]
	if !exists {
		instance = &AgentInstance{Name: cfg.AgentName}
		m.agents[cfg.AgentName] = instance
	}
	instance.lockFile = f
	m.mu.Unlock()

	return instance.Start(cfg, debug, stdout, heaptrack)
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

func (ag *AgentInstance) Start(cfg AgentConfig, debug bool, stdout *os.File, heaptrack bool) error {
	ag.mu.Lock()
	defer ag.mu.Unlock()

	binDir := viper.GetString("build.defaults.build_output")
	agentBin := utils.ExpandPath(filepath.Join(binDir, "agent"))
	if _, err := os.Stat(agentBin); os.IsNotExist(err) {
		return fmt.Errorf("agent binary not found at %s. Please compile it first", agentBin)
	}

	args := []string{
		"--log-level", fmt.Sprintf("%d", cfg.LogLevel),
		"--name", cfg.AgentName,
		"--ip", cfg.AgentIP,
		"--port", fmt.Sprintf("%d", cfg.AgentPort),
		"--terminal-log", "false",
		"--perf",
	}

	webUrl := viper.GetString("webui.host")
	if webUrl == "" {
		webUrl = "http://localhost:8080"
	}
	args = append(args, "--webui", webUrl)

	binDir = utils.ExpandPath(binDir)
	env := os.Environ()

	found := false
	for i, e := range env {
		if strings.HasPrefix(e, "LD_LIBRARY_PATH=") {
			oldVal := strings.TrimPrefix(e, "LD_LIBRARY_PATH=")
			if oldVal == "" {
				env[i] = "LD_LIBRARY_PATH=" + binDir
			} else if !strings.Contains(oldVal, binDir) {
				env[i] = "LD_LIBRARY_PATH=" + binDir + ":" + oldVal
			}
			found = true
			break
		}
	}
	if !found {
		env = append(env, "LD_LIBRARY_PATH="+binDir)
	}

	// Add workspace paths from config
	for _, ws := range cfg.Workspaces {
		args = append(args, "--workspace", ws)
	}

	fullCmd := agentBin + " " + strings.Join(args, " ")
	if debug {
		fullCmd = "gdb -ex run --args " + fullCmd
	}
	utils.LogSection(os.Stdout, "[%s] Starting agent (debug=%v) Command: %s", ag.Name, debug, fullCmd)

	var cmd *exec.Cmd
	if debug {
		gdbArgs := append([]string{"-ex", "run", "--args", agentBin}, args...)
		cmd = exec.Command("gdb", gdbArgs...)
	} else {
		cmd = exec.Command(agentBin, args...)
	}

	cmd.Env = env

	if heaptrack {
		perfDir := filepath.Join(utils.GetFinsHome(), "performance")
		os.MkdirAll(perfDir, 0755)
		heaptrackOutput := filepath.Join(perfDir, fmt.Sprintf("heaptrack.%s.gz", ag.Name))

		heaptrackArgs := []string{"-o", heaptrackOutput, agentBin}
		heaptrackArgs = append(heaptrackArgs, args...)

		heaptrackCmd := exec.Command("heaptrack", heaptrackArgs...)
		heaptrackCmd.Env = env

		if stdout != nil {
			heaptrackCmd.Stdout = stdout
			heaptrackCmd.Stderr = stdout
		} else {
			logPath := filepath.Join(utils.GetLogDir(), fmt.Sprintf("agent_%s.log", ag.Name))
			f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
			if err != nil {
				return fmt.Errorf("failed to open log file: %v", err)
			}
			ag.logFile = f
			heaptrackCmd.Stdout = f
			heaptrackCmd.Stderr = f
		}

		heaptrackCmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

		if err := heaptrackCmd.Start(); err != nil {
			if ag.logFile != nil {
				ag.logFile.Close()
			}
			return fmt.Errorf("failed to start heaptrack: %v", err)
		}

		ag.cmd = heaptrackCmd
		ag.isRunning = true
		ag.pid = heaptrackCmd.Process.Pid
		ag.Config = cfg

		utils.LogSuccess(os.Stdout, "heaptrack started, output: %s", heaptrackOutput)
		utils.LogSection(os.Stdout, "[%s] Agent started with PID %d under heaptrack, Press Ctrl+C to stop\n", ag.Name, ag.pid)
	} else {
		if stdout != nil {
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

		utils.LogSection(os.Stdout, "[%s] Agent started with PID %d, Press Ctrl+C to stop\n", ag.Name, ag.pid)
	}

	if ag.lockFile != nil {
		ag.lockFile.Truncate(0)
		ag.lockFile.Seek(0, 0)
		fmt.Fprintf(ag.lockFile, "%d", ag.pid)
	}

	go func() {
		state, _ := ag.cmd.Process.Wait()
		ag.mu.Lock()
		defer ag.mu.Unlock()

		ag.isRunning = false
		ag.pid = 0

		if ag.logFile != nil {
			if state != nil {
				fmt.Fprintf(ag.logFile, "\n[%s] Agent exited with code %d\n", time.Now().Format(time.RFC3339), state.ExitCode())
			}
			ag.logFile.Close()
			ag.logFile = nil
		}

		if ag.lockFile != nil {
			syscall.Flock(int(ag.lockFile.Fd()), syscall.LOCK_UN)
			ag.lockFile.Close()
			os.Remove(filepath.Join(utils.GetFinsHome(), "install", fmt.Sprintf("agent_%s.lock", ag.Name)))
			ag.lockFile = nil
		}
	}()

	return nil
}

func (ag *AgentInstance) Stop() error {
	ag.mu.Lock()
	defer ag.mu.Unlock()

	if !ag.isRunning || ag.cmd == nil || ag.cmd.Process == nil {
		return fmt.Errorf("agent is not running")
	}

	err := syscall.Kill(-ag.pid, syscall.SIGTERM)
	if err != nil {
		err = syscall.Kill(-ag.pid, syscall.SIGKILL)
		if err != nil {
			return ag.cmd.Process.Kill()
		}
	}

	return nil
}
