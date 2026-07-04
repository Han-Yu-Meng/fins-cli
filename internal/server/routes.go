package server

import (
	"fins-cli/internal/server/handlers"

	"github.com/gin-gonic/gin"
)

func SetupRoutes(r *gin.Engine) {
	// 包管理 API
	r.GET("/api/packages", handlers.GetPackages)
	r.GET("/api/package/detail/*name", handlers.GetPackageDetail)
	r.GET("/api/package/asset/*path", handlers.GetPackageAsset)
	r.GET("/api/package/log/*name", handlers.GetPackageLog)
	r.POST("/api/scan", handlers.TriggerScan)
	r.POST("/api/install", handlers.InstallPlugin)

	// 编译 API
	r.POST("/api/build-workspace", handlers.CompileWorkspace)
	r.POST("/api/build/*name", handlers.CompilePackage)
	r.POST("/api/clean", handlers.CleanBuilds)

	// 配置 API
	r.GET("/api/presets", handlers.GetPresets)
	r.POST("/api/preset", handlers.SetPreset)

	// Agent 管理 API
	r.POST("/api/agent/start", handlers.StartAgent)
	r.POST("/api/agent/run", handlers.RunAgent)
	r.POST("/api/agent/debug", handlers.DebugAgent)
	r.POST("/api/agent/stop", handlers.StopAgent)
	r.GET("/api/agent/status", handlers.GetAgentStatus)
	r.GET("/api/agent/logs", handlers.GetAgentLogs)
	r.POST("/api/agent/build", handlers.CompileAgent)

	// Inspect API
	r.POST("/api/inspect/build", handlers.CompileInspect)
	r.GET("/api/inspect/analyze/*name", handlers.AnalyzePackage)
	r.GET("/api/inspect/file", handlers.AnalyzeFile)

	// 依赖管理 API
	r.POST("/api/dep/build", handlers.BuildDependency)
	r.POST("/api/dep/solve/*name", handlers.SolveDependencies)
	r.GET("/api/recipe/:name", handlers.GetRecipe)
}
