package handlers

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"finsd/internal/agent"
	"finsd/internal/core"

	"github.com/gin-gonic/gin"
)

// StartAgent 启动 agent
func StartAgent(c *gin.Context) {
	var req agent.AgentConfig
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": "Invalid configuration: " + err.Error()})
		return
	}

	if req.AgentName == "" {
		c.JSON(400, gin.H{"error": "Agent name is required"})
		return
	}

	if err := agent.GlobalManager.Start(req, false, nil); err != nil {
		c.JSON(500, gin.H{"error": "Failed to start agent: " + err.Error()})
		return
	}

	c.JSON(200, gin.H{"message": fmt.Sprintf("Agent '%s' started successfully", req.AgentName)})
}

// RunAgent 运行 agent 并流显式输出
func RunAgent(c *gin.Context) {
	var req agent.AgentConfig
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": "Invalid configuration: " + err.Error()})
		return
	}

	// 劫持连接以支持流式输出
	c.Writer.Header().Set("Content-Type", "text/plain")
	c.Writer.Header().Set("Transfer-Encoding", "chunked")
	c.Writer.WriteHeader(200)
	flusher, _ := c.Writer.(http.Flusher)

	// 使用管道将输出重定向到 http 响应
	pr, pw, _ := os.Pipe()
	defer pr.Close()

	if err := agent.GlobalManager.Start(req, false, pw); err != nil {
		pw.Close()
		fmt.Fprintf(c.Writer, "Error: %v\n", err)
		flusher.Flush()
		return
	}

	// 将管道输出复制到客户端
	go func() {
		buf := make([]byte, 1024)
		for {
			n, err := pr.Read(buf)
			if n > 0 {
				c.Writer.Write(buf[:n])
				flusher.Flush()
			}
			if err != nil {
				break
			}
		}
	}()

	// 等待直到 agent 停止或连接关闭
	notify := c.Request.Context().Done()
	agentDone := make(chan struct{})
	go func() {
		for {
			running, _, _ := agent.GlobalManager.GetStatus(req.AgentName)
			if !running {
				close(agentDone)
				return
			}
			select {
			case <-notify:
				// 客户端断开，停止 agent
				agent.GlobalManager.Stop(req.AgentName)
				close(agentDone)
				return
			default:
				time.Sleep(500 * time.Millisecond)
			}
		}
	}()

	<-agentDone
}

// DebugAgent 调试 agent 并流显式输出
func DebugAgent(c *gin.Context) {
	var req agent.AgentConfig
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": "Invalid configuration: " + err.Error()})
		return
	}

	c.Writer.Header().Set("Content-Type", "text/plain")
	c.Writer.Header().Set("Transfer-Encoding", "chunked")
	c.Writer.WriteHeader(200)
	flusher, _ := c.Writer.(http.Flusher)

	pr, pw, _ := os.Pipe()
	defer pr.Close()

	if err := agent.GlobalManager.Start(req, true, pw); err != nil {
		pw.Close()
		fmt.Fprintf(c.Writer, "Error: %v\n", err)
		flusher.Flush()
		return
	}

	go func() {
		buf := make([]byte, 1024)
		for {
			n, err := pr.Read(buf)
			if n > 0 {
				c.Writer.Write(buf[:n])
				flusher.Flush()
			}
			if err != nil {
				break
			}
		}
	}()

	notify := c.Request.Context().Done()
	agentDone := make(chan struct{})
	go func() {
		for {
			running, _, _ := agent.GlobalManager.GetStatus(req.AgentName)
			if !running {
				close(agentDone)
				return
			}
			select {
			case <-notify:
				agent.GlobalManager.Stop(req.AgentName)
				close(agentDone)
				return
			default:
				time.Sleep(500 * time.Millisecond)
			}
		}
	}()

	<-agentDone
}

// StopAgent 停止 agent
func StopAgent(c *gin.Context) {
	var req struct {
		AgentName string `json:"agent_name"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": "Invalid request: " + err.Error()})
		return
	}
	if req.AgentName == "" {
		c.JSON(400, gin.H{"error": "agent_name is required"})
		return
	}

	if err := agent.GlobalManager.Stop(req.AgentName); err != nil {
		c.JSON(500, gin.H{"error": "Failed to stop agent: " + err.Error()})
		return
	}
	c.JSON(200, gin.H{"message": fmt.Sprintf("Agent '%s' stopped", req.AgentName)})
}

// GetAgentStatus 获取 agent 状态
func GetAgentStatus(c *gin.Context) {
	name := c.Query("name")
	if name != "" {
		running, pid, err := agent.GlobalManager.GetStatus(name)
		if err != nil {
			c.JSON(404, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, gin.H{
			"name":    name,
			"running": running,
			"pid":     pid,
		})
	} else {
		// Return all statuses
		statuses := agent.GlobalManager.GetAllStatus()
		c.JSON(200, statuses)
	}
}

// GetAgentLogs 获取 agent 日志
func GetAgentLogs(c *gin.Context) {
	name := c.Query("name")
	if name == "" {
		c.JSON(400, gin.H{"error": "name query parameter is required"})
		return
	}

	logPath := filepath.Join(core.GetLogDir(), fmt.Sprintf("agent_%s.log", name))
	if _, err := os.Stat(logPath); os.IsNotExist(err) {
		c.String(404, "No logs available for agent: "+name)
		return
	}
	c.File(logPath)
}
