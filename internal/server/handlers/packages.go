package handlers

import (
	"compress/gzip"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"finsd/internal/core"
	"finsd/internal/types"

	"github.com/gin-gonic/gin"
	"github.com/spf13/viper"
)

// GetPackages 获取所有包列表
func GetPackages(c *gin.Context) {
	data := PackageWatcher.GetPackages()
	if strings.Contains(c.Request.Header.Get("Accept-Encoding"), "gzip") {
		c.Header("Content-Encoding", "gzip")
		c.Header("Vary", "Accept-Encoding")
		gz := gzip.NewWriter(c.Writer)
		defer gz.Close()
		c.Header("Content-Type", "application/json; charset=utf-8")
		json.NewEncoder(gz).Encode(data)
	} else {
		c.JSON(200, data)
	}
}

// GetPackageDetail 获取包详情
func GetPackageDetail(c *gin.Context) {
	name := c.Param("name")
	if len(name) > 1 && name[0] == '/' {
		name = name[1:]
	}

	p := PackageWatcher.GetPackage(name)

	// If not found by short name, try combining with source query param
	if p == nil {
		source := c.Query("source")
		if source != "" && source != "Unknown" {
			p = PackageWatcher.GetPackage(source + "/" + name)
		}
	}

	// Fallback: search for suffix if short name is unique enough
	if p == nil {
		pkgs := PackageWatcher.GetPackages()
		for _, pkg := range pkgs {
			if strings.HasSuffix(pkg.Name, "/"+name) {
				// If we find a match, double check if source matches if provided
				source := c.Query("source")
				if source != "" && source != "Unknown" && pkg.Source != source {
					continue
				}
				realPkg := PackageWatcher.GetPackage(pkg.Name)
				if realPkg != nil {
					p = realPkg
					break
				}
			}
		}
	}

	if p == nil {
		c.JSON(404, gin.H{"error": "Package not found"})
		return
	}

	details := types.PackageDetails{
		Package:       *p,
		ReadmeContent: "",
	}

	if p.ReadmePath != "" {
		if content, err := os.ReadFile(p.ReadmePath); err == nil {
			details.ReadmeContent = string(content)
		}
	}

	c.JSON(200, details)
}

// GetPackageAsset 获取包资源文件
func GetPackageAsset(c *gin.Context) {
	fullPath := c.Param("path")
	if len(fullPath) > 1 && fullPath[0] == '/' {
		fullPath = fullPath[1:]
	}

	var matchedPkg *types.Package
	var relPath string

	pkgs := PackageWatcher.GetPackages()

	var pkgNames []string
	for _, p := range pkgs {
		pkgNames = append(pkgNames, p.Name)
	}
	sort.Slice(pkgNames, func(i, j int) bool { return len(pkgNames[i]) > len(pkgNames[j]) })

	for _, name := range pkgNames {
		if strings.HasPrefix(fullPath, name+"/") {
			matchedPkg = PackageWatcher.GetPackage(name)
			relPath = strings.TrimPrefix(fullPath, name+"/")
			break
		}
	}

	if matchedPkg == nil {
		parts := strings.SplitN(fullPath, "/", 2)
		if len(parts) >= 2 {
			potentialShortName := parts[0]
			relPathCandidate := parts[1]

			for _, p := range pkgs {
				if p.Name == potentialShortName || strings.HasSuffix(p.Name, "/"+potentialShortName) {
					matchedPkg = PackageWatcher.GetPackage(p.Name)
					relPath = relPathCandidate
					break
				}
			}
		}
	}

	if matchedPkg != nil {
		if strings.Contains(relPath, "..") {
			c.Status(403)
			return
		}

		file := filepath.Join(matchedPkg.Path, relPath)
		if _, err := os.Stat(file); err == nil {
			c.File(file)
		} else {
			c.Status(404)
		}
	} else {
		c.Status(404)
	}
}

// GetPackageLog 获取包日志
func GetPackageLog(c *gin.Context) {
	name := c.Param("name")
	if len(name) > 1 && name[0] == '/' {
		name = name[1:]
	}

	safeName := strings.ReplaceAll(name, "/", "_")
	logPath := filepath.Join(core.GetLogDir(), safeName+".log")

	if content, err := os.ReadFile(logPath); err == nil {
		c.String(200, string(content))
	} else {
		c.String(200, "")
	}
}

// TriggerScan 触发包扫描
func TriggerScan(c *gin.Context) {
	// 重新加载配置
	if err := viper.ReadInConfig(); err != nil {
		log.Printf("Failed to reload config during scan: %v", err)
	}
	PackageWatcher.Rescan()
	c.JSON(200, gin.H{"message": "Package scan triggered"})
}
