package core

import (
	"encoding/xml"
	"fins-cli/internal/types"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/viper"
	"gopkg.in/yaml.v3"
)

type ros2Maintainer struct {
	Name  string `xml:",chardata"`
	Email string `xml:"email,attr"`
}

type ros2PackageXML struct {
	XMLName      xml.Name         `xml:"package"`
	Format       string           `xml:"format,attr"`
	Name         string           `xml:"name"`
	Version      string           `xml:"version"`
	Description  string           `xml:"description"`
	Maintainer   []ros2Maintainer `xml:"maintainer"`
	License      []string         `xml:"license"`
	Depend       []string         `xml:"depend"`
	BuildDep     []string         `xml:"build_depend"`
	BuildToolDep []string         `xml:"buildtool_depend"`
	ExecDep      []string         `xml:"exec_depend"`
	TestDep      []string         `xml:"test_depend"`
	Export       struct {
		BuildType string `xml:"build_type"`
	} `xml:"export"`
}

func ScanPackages() (map[string]*types.Package, error) {
	pkgs := make(map[string]*types.Package)

	type LocalSource struct {
		Name string `mapstructure:"name"`
		Path string `mapstructure:"path"`
	}
	var localSources []LocalSource
	if err := viper.UnmarshalKey("local_packages", &localSources); err != nil {
		return nil, fmt.Errorf("failed to unmarshal local_packages: %v", err)
	}

	if len(localSources) == 0 {
		if raw := viper.Get("local_packages"); raw != nil {
			if list, ok := raw.([]interface{}); ok {
				for _, item := range list {
					if m, ok := item.(map[string]interface{}); ok {
						name, _ := m["name"].(string)
						path, _ := m["path"].(string)
						if name == "" {
							name, _ = m["Name"].(string)
						}
						if path == "" {
							path, _ = m["Path"].(string)
						}

						if name != "" && path != "" {
							localSources = append(localSources, LocalSource{Name: name, Path: path})
						}
					}
				}
			}
		}
	}

	rawPkgs := make(map[string][]*types.Package)

	for _, src := range localSources {
		root := src.Path
		sourceName := src.Name

		filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}

			if d.IsDir() && strings.HasPrefix(d.Name(), ".") && d.Name() != "." {
				return filepath.SkipDir
			}

			if d.IsDir() && (d.Name() == "build" || d.Name() == "devel" || d.Name() == "install") {
				return filepath.SkipDir
			}

			if !d.IsDir() && d.Name() == "package.yaml" {
				pkgPath := filepath.Dir(path)
				pkg := LoadFinsPackage(pkgPath, path)
				if pkg != nil {
					pkg.Source = sourceName
					pkg.Type = types.PackageTypeFins
					rawPkgs[pkg.Meta.Name] = append(rawPkgs[pkg.Meta.Name], pkg)
				}
				return filepath.SkipDir
			}

			if !d.IsDir() && d.Name() == "package.xml" {
				pkgPath := filepath.Dir(path)
				pkg := LoadROS2Package(pkgPath, path)
				if pkg != nil {
					pkg.Source = sourceName
					pkg.Type = types.PackageTypeROS2
					rawPkgs[pkg.Meta.Name] = append(rawPkgs[pkg.Meta.Name], pkg)
				}
				return filepath.SkipDir
			}
			return nil
		})
	}

	for name, entryList := range rawPkgs {
		for _, p := range entryList {
			fullName := fmt.Sprintf("%s/%s", p.Source, name)
			pkgs[fullName] = p
		}
	}

	return pkgs, nil
}

func LoadFinsPackage(path, metaPath string) *types.Package {
	var config struct {
		Package types.PackageMetadata `yaml:"package"`
	}

	data, _ := os.ReadFile(metaPath)
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil
	}

	if config.Package.Name == "" {
		_ = yaml.Unmarshal(data, &config.Package)
	}

	if config.Package.Name == "" {
		return nil
	}

	p := &types.Package{
		Path: path,
		Meta: config.Package,
		Type: types.PackageTypeFins,
	}

	if _, err := os.Stat(filepath.Join(path, "README.md")); err == nil {
		p.ReadmePath = filepath.Join(path, "README.md")
	}

	if _, err := os.Stat(filepath.Join(path, "assets", "logo.png")); err == nil {
		p.IconPath = "assets/logo.png"
	} else if _, err := os.Stat(filepath.Join(path, "assets", "logo.jpg")); err == nil {
		p.IconPath = "assets/logo.jpg"
	}

	p.Status = checkBuildStatus(p)
	return p
}

func LoadROS2Package(path, metaPath string) *types.Package {
	data, err := os.ReadFile(metaPath)
	if err != nil {
		return nil
	}

	var xmlPkg ros2PackageXML
	if err := xml.Unmarshal(data, &xmlPkg); err != nil {
		return nil
	}

	if xmlPkg.Name == "" {
		return nil
	}

	meta := types.PackageMetadata{
		Name:        xmlPkg.Name,
		Version:     xmlPkg.Version,
		Description: xmlPkg.Description,
		Licenses:    xmlPkg.License,
		Depends:     make(map[string]string),
	}

	for _, m := range xmlPkg.Maintainer {
		meta.Maintainers = append(meta.Maintainers, struct {
			Name  string `yaml:"name" json:"name"`
			Email string `yaml:"email" json:"email"`
		}{
			Name:  strings.TrimSpace(m.Name),
			Email: m.Email,
		})
	}

	// Collect all dependencies as system-level (they are ROS2/system packages)
	for _, dep := range xmlPkg.Depend {
		meta.Depends[dep] = "system"
	}
	for _, dep := range xmlPkg.BuildDep {
		meta.Depends[dep] = "system"
	}
	for _, dep := range xmlPkg.BuildToolDep {
		meta.Depends[dep] = "system"
	}
	for _, dep := range xmlPkg.ExecDep {
		meta.Depends[dep] = "system"
	}

	p := &types.Package{
		Path: path,
		Meta: meta,
		Type: types.PackageTypeROS2,
	}

	if _, err := os.Stat(filepath.Join(path, "README.md")); err == nil {
		p.ReadmePath = filepath.Join(path, "README.md")
	}

	p.Status = checkBuildStatus(p)
	return p
}

// LoadPackage is kept for backward compatibility with existing callers.
func LoadPackage(path, metaPath string) *types.Package {
	return LoadFinsPackage(path, metaPath)
}

func checkBuildStatus(p *types.Package) types.BuildStatus {
	// ROS2 packages use colcon's build system — skip source-based staleness check
	if p.Type == types.PackageTypeROS2 {
		return types.StatusUncompiled
	}

	binDir := viper.GetString("build_output")
	soName := fmt.Sprintf("lib%s_%s.so", p.Source, p.Meta.Name)
	soPath := filepath.Join(binDir, soName)

	soInfo, err := os.Stat(soPath)
	if os.IsNotExist(err) {
		return types.StatusUncompiled
	}

	isStale := false
	filepath.Walk(p.Path, func(path string, info os.FileInfo, err error) error {
		if !info.IsDir() && (strings.HasSuffix(path, ".cpp") || strings.HasSuffix(path, ".hpp")) {
			if info.ModTime().After(soInfo.ModTime()) {
				isStale = true
				return filepath.SkipAll
			}
		}
		return nil
	})

	if isStale {
		return types.StatusStale
	}
	return types.StatusCurrent
}

func ResolvePackage(inputName string, pkgs map[string]*types.Package) (*types.Package, error) {
	if strings.Contains(inputName, "/") {
		if p, ok := pkgs[inputName]; ok {
			return p, nil
		}
		return nil, fmt.Errorf("package '%s' not found", inputName)
	}

	var candidates []*types.Package
	for _, p := range pkgs {
		if p.Meta.Name == inputName {
			candidates = append(candidates, p)
		}
	}

	if len(candidates) == 0 {
		return nil, fmt.Errorf("package '%s' not found", inputName)
	}

	if len(candidates) == 1 {
		return candidates[0], nil
	}

	var sources []string
	for _, c := range candidates {
		sources = append(sources, c.Source)
	}
	return nil, fmt.Errorf("ambiguous package name '%s'. Found in sources: %s. Please use 'Source/Name' format", inputName, strings.Join(sources, ", "))
}
