package types

type BuildStatus string

const (
	StatusUncompiled BuildStatus = "Uncompiled"
	StatusStale      BuildStatus = "Modified"
	StatusCurrent    BuildStatus = "Ready"
	StatusCompiling  BuildStatus = "Compiling"
	StatusFailed     BuildStatus = "Failed"
)

type PackageType string

const (
	PackageTypeFins PackageType = "fins"
	PackageTypeROS2 PackageType = "ros2"
)

type DependencyRecipe struct {
	Type          string   `yaml:"type" json:"type"`
	SystemPackage string   `yaml:"system_pkg" json:"system_pkg"`
	PPA           string   `yaml:"ppa" json:"ppa"`
	GitURL        string   `yaml:"git" json:"git"`
	BuildSystem   string   `yaml:"build_system" json:"build_system"`
	CMakeArgs     []string `yaml:"cmake_args" json:"cmake_args"`
	Versions      map[string]struct {
		Tag       string   `yaml:"tag" json:"tag"`
		GitURL    string   `yaml:"git" json:"git"`
		CMakeArgs []string `yaml:"cmake_args" json:"cmake_args"`
	} `yaml:"versions" json:"versions"`
}

type PackageMetadata struct {
	Name        string `yaml:"name" json:"name"`
	Version     string `yaml:"version" json:"version"`
	Description string `yaml:"description" json:"description"`
	Maintainers []struct {
		Name  string `yaml:"name" json:"name"`
		Email string `yaml:"email" json:"email"`
	} `yaml:"maintainers" json:"maintainers"`
	Licenses []string                    `yaml:"licenses" json:"licenses"`
	Depends  map[string]string           `yaml:"depends" json:"depends"`
	Recipes  map[string]DependencyRecipe `yaml:"recipes" json:"recipes"`
}

type Package struct {
	Path       string          `json:"path"`
	Meta       PackageMetadata `json:"meta"`
	ReadmePath string          `json:"readme_path"`
	ScriptPath string          `json:"script_path"`
	IconPath   string          `json:"icon_path"`
	Status     BuildStatus     `json:"status"`
	Source     string          `json:"source"`
	Type       PackageType     `json:"type"`
}

type PackageDetails struct {
	Package
	ReadmeContent string `json:"readme_content"`
}

type PackageInfo struct {
	Name        string      `json:"name"`
	Version     string      `json:"version"`
	Description string      `json:"description"`
	Status      BuildStatus `json:"status"`
	Path        string      `json:"path"`
	Maintainer  string      `json:"maintainer"`
	Source      string      `json:"source"`
	IconPath    string      `json:"icon_path"`
	Type        PackageType `json:"type"`
}

type CompileRequest struct {
	PackageName string `json:"package_name"`
}
