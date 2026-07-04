package monitor

import (
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"fins-cli/internal/core"
	"fins-cli/internal/types"

	"github.com/fsnotify/fsnotify"
	"github.com/spf13/viper"
)

type PackageWatcher struct {
	watcher *fsnotify.Watcher
	cache   map[string]*types.Package
	mutex   sync.RWMutex
}

func NewWatcher() (*PackageWatcher, error) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}

	return &PackageWatcher{
		watcher: w,
		cache:   make(map[string]*types.Package),
	}, nil
}

func (pw *PackageWatcher) Start() {
	pw.setupWatchers()

	go func() {
		for {
			select {
			case event, ok := <-pw.watcher.Events:
				if !ok {
					return
				}
				pw.handleEvent(event)
			case err, ok := <-pw.watcher.Errors:
				if !ok {
					return
				}
				log.Println("Watcher error:", err)
			}
		}
	}()
}

func (pw *PackageWatcher) setupWatchers() {
	pw.mutex.Lock()
	pkgs, _ := core.ScanPackages()
	pw.cache = pkgs
	pw.mutex.Unlock()

	type LocalSource struct {
		Name string `mapstructure:"name"`
		Path string `mapstructure:"path"`
	}
	var localSources []LocalSource
	_ = viper.UnmarshalKey("local_packages", &localSources)

	for _, src := range localSources {
		root := src.Path
		_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				if strings.HasPrefix(d.Name(), ".") && d.Name() != "." {
					return filepath.SkipDir
				}

				rel, _ := filepath.Rel(root, path)
				if rel != "." {
					parts := strings.Split(rel, string(filepath.Separator))
					if len(parts) > 1 {
						return filepath.SkipDir
					}
				}

				_ = pw.watcher.Add(path)
			}
			return nil
		})
	}
}

func (pw *PackageWatcher) handleEvent(event fsnotify.Event) {
	if event.Op&fsnotify.Create == fsnotify.Create {
		info, err := os.Stat(event.Name)
		if err == nil && info.IsDir() {
			isRootChild := false
			var localSources []struct {
				Name string `mapstructure:"name"`
				Path string `mapstructure:"path"`
			}
			_ = viper.UnmarshalKey("local_packages", &localSources)
			for _, src := range localSources {
				parent := filepath.Dir(event.Name)
				if parent == src.Path {
					isRootChild = true
					break
				}
			}

			if isRootChild {
				log.Println("New package directory found, watching:", event.Name)
				pw.watcher.Add(event.Name)
				pw.reloadPackage(event.Name, "directory created")
			}
			return
		}
	}

	if event.Op&fsnotify.Rename == fsnotify.Rename || event.Op&fsnotify.Remove == fsnotify.Remove {
		pw.mutex.RLock()
		isPackageRoot := false
		for _, pkg := range pw.cache {
			if pkg.Path == event.Name {
				isPackageRoot = true
				break
			}
		}
		pw.mutex.RUnlock()

		if isPackageRoot {
			pw.reloadPackage(event.Name, "package directory removed/renamed")
			return
		}
	}

	if strings.HasSuffix(event.Name, "package.yaml") || strings.HasSuffix(event.Name, "package.xml") {
		if event.Op&fsnotify.Write == fsnotify.Write ||
			event.Op&fsnotify.Create == fsnotify.Create ||
			event.Op&fsnotify.Rename == fsnotify.Rename ||
			event.Op&fsnotify.Remove == fsnotify.Remove {
			pw.reloadPackage(filepath.Dir(event.Name), event.Name)
			return
		}
	}

	if event.Op&fsnotify.Write == fsnotify.Write {
		if strings.HasSuffix(event.Name, ".cpp") || strings.HasSuffix(event.Name, ".hpp") {
			log.Println("File modified:", event.Name)
			pw.markStale(event.Name)
		}
	}
}

func (pw *PackageWatcher) reloadPackage(dir, metaPath string) {
	pkgs, err := core.ScanPackages()
	if err == nil {
		pw.mutex.Lock()
		pw.cache = pkgs
		pw.mutex.Unlock()
		log.Printf("Reloaded package configuration from: %s", metaPath)
	}
}

func (pw *PackageWatcher) Rescan() {
	pw.setupWatchers()

	pkgs, err := core.ScanPackages()
	if err == nil {
		pw.mutex.Lock()
		pw.cache = pkgs
		pw.mutex.Unlock()
		log.Println("Manual rescan: Package list updated")
	} else {
		log.Println("Manual rescan failed:", err)
	}
}

func (pw *PackageWatcher) markStale(filePath string) {
	pw.mutex.Lock()
	defer pw.mutex.Unlock()
	for _, pkg := range pw.cache {
		if strings.HasPrefix(filePath, pkg.Path) {
			pkg.Status = types.StatusStale
			log.Printf("Package %s marked as STALE", pkg.Meta.Name)
			break
		}
	}
}

func (pw *PackageWatcher) UpdateStatus(name string, status types.BuildStatus) {
	pw.mutex.Lock()
	defer pw.mutex.Unlock()
	if pkg, ok := pw.cache[name]; ok {
		pkg.Status = status
	}
}

func (pw *PackageWatcher) GetPackage(name string) *types.Package {
	pw.mutex.RLock()
	defer pw.mutex.RUnlock()
	if p, ok := pw.cache[name]; ok {
		return p
	}
	return nil
}

func (pw *PackageWatcher) GetPackagesMap() map[string]*types.Package {
	pw.mutex.RLock()
	defer pw.mutex.RUnlock()
	// Return a copy to avoid race conditions and external map mutation
	res := make(map[string]*types.Package)
	for k, v := range pw.cache {
		res[k] = v
	}
	return res
}

func (pw *PackageWatcher) GetPackages() []types.PackageInfo {
	pw.mutex.RLock()
	defer pw.mutex.RUnlock()
	var list []types.PackageInfo
	for k, p := range pw.cache {
		list = append(list, types.PackageInfo{
			Name:        k, // Use the unique key (short name or src/name)
			Version:     p.Meta.Version,
			Description: p.Meta.Description,
			Status:      p.Status,
			Path:        p.Path,
			Maintainer:  getMaintainer(p),
			Source:      p.Source,
			IconPath:    p.IconPath,
			Type:        p.Type,
		})
	}
	return list
}

func getMaintainer(p *types.Package) string {
	if len(p.Meta.Maintainers) > 0 {
		return p.Meta.Maintainers[0].Name
	}
	return ""
}
