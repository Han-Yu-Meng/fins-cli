package core

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"fins-cli/internal/types"
	"fins-cli/internal/utils"

	"github.com/spf13/viper"
)

var (
	buildLocks sync.Map // map[string]bool
)

const wslSanitizeLogic = `
if(EXISTS "/proc/sys/fs/binfmt_misc/WSLInterop")
    message(STATUS "WSL Environment Detected: Sanitizing environment...")

    set(_current_path "$ENV{PATH}")
    string(REPLACE ":" ";" _path_list "${_current_path}")
    set(_clean_path_list "")
    
    foreach(_p ${_path_list})
        if(NOT "${_p}" MATCHES "^/mnt/")
            list(APPEND _clean_path_list "${_p}")
        endif()
    endforeach()
    
    list(JOIN _clean_path_list ":" _clean_path)
    set(ENV{PATH} "${_clean_path}")
    message(STATUS "PATH sanitized: Windows paths removed from CMake visibility.")

    list(APPEND CMAKE_SYSTEM_IGNORE_PATH "/mnt/c" "/mnt/d" "/mnt/e" "/mnt/f" "/mnt/g" "/mnt")
    list(APPEND CMAKE_IGNORE_PATH "/mnt/c" "/mnt/d" "/mnt/e" "/mnt/f" "/mnt/g" "/mnt")
    list(APPEND CMAKE_IGNORE_PREFIX_PATH "/mnt/c" "/mnt/d" "/mnt/e" "/mnt/f" "/mnt/g" "/mnt")

    get_cmake_property(_cacheVars CACHE_VARIABLES)
    foreach(_var ${_cacheVars})
        get_property(_val CACHE ${_var} PROPERTY VALUE)
        if("${_val}" MATCHES "^/mnt/")
            if("${_var}" MATCHES "(DIR|LIBRARY|INCLUDE|FILE|PATH|PROGRAM)$")
                message(STATUS "Clearing polluted cache var: ${_var} = ${_val}")
                unset(${_var} CACHE)
            endif()
        endif()
    endforeach()
endif()
`
const link_ros_dependencies = `
function(fins_link_ros_dependencies target)
    foreach(pkg ${ARGN})
        find_package(${pkg} REQUIRED)
        message(STATUS "[FINS] Linking ${pkg} to ${target}")

        if(TARGET ${pkg}::${pkg})
            target_link_libraries(${target} PRIVATE ${pkg}::${pkg})
        
        elseif(TARGET ${pkg})
            target_link_libraries(${target} PRIVATE ${pkg})
        
        else()
            if(DEFINED ${pkg}_INCLUDE_DIRS)
                target_include_directories(${target} PRIVATE ${${pkg}_INCLUDE_DIRS})
            elseif(DEFINED ${pkg}_INCLUDE_DIR)
                target_include_directories(${target} PRIVATE ${${pkg}_INCLUDE_DIR})
            endif()

            if(DEFINED ${pkg}_LIBRARIES)
                target_link_libraries(${target} PRIVATE ${${pkg}_LIBRARIES})
            elseif(DEFINED ${pkg}_LIBRARY)
                target_link_libraries(${target} PRIVATE ${${pkg}_LIBRARY})
            endif()
        endif()
    endforeach()
endfunction()

macro(fins_optional_ros_dependency target pkg_name)
    find_package(${pkg_name} QUIET)
    if(${pkg_name}_FOUND)
        message(STATUS ">> Optional Package '${pkg_name}': FOUND. Enabling...")
        fins_link_ros_dependencies(${target} ${pkg_name})
        string(TOUPPER ${pkg_name} _PKG_UPPER)
        target_compile_definitions(${target} PRIVATE WITH_${_PKG_UPPER})
    else()
        message(STATUS ">> Optional Package '${pkg_name}': NOT FOUND. Skipping.")
    endif()
endmacro()
`

func CompileSDKStatic(ctx context.Context, writer io.Writer) error {
	sdkPath := utils.ExpandPath(viper.GetString("build.defaults.sdk_path"))
	installDir := utils.ExpandPath(viper.GetString("build.defaults.build_output"))

	// Build SDK static library in dedicated directory
	sdkBuildDir := filepath.Join(utils.GetFinsHome(), "build", "sdk_static")
	if err := os.MkdirAll(sdkBuildDir, 0755); err != nil {
		return fmt.Errorf("failed to create SDK build directory: %v", err)
	}

	// Ensure install lib directory exists
	libDir := filepath.Join(installDir, "lib")
	if err := os.MkdirAll(libDir, 0755); err != nil {
		return fmt.Errorf("failed to create install lib directory: %v", err)
	}

	wrapperContent := fmt.Sprintf(`
cmake_minimum_required(VERSION 3.16)
project(fins_sdk_static LANGUAGES CXX)

find_package(Threads REQUIRED)

set(CMAKE_CXX_STANDARD 20)
set(CMAKE_CXX_STANDARD_REQUIRED YES)
# Force add -DFMT_HEADER_ONLY
set(CMAKE_CXX_FLAGS "${CMAKE_CXX_FLAGS} -Wall -Wextra -fPIC -DFMT_HEADER_ONLY")

%[1]s

# === COLLECT SDK SOURCES ===
file(GLOB_RECURSE SDK_SOURCES 
    "%[2]s/fins/*.cpp"
)
# Exclude agent/inspect sources from SDK library
list(FILTER SDK_SOURCES EXCLUDE REGEX ".*/agent/.*")
list(FILTER SDK_SOURCES EXCLUDE REGEX ".*/inspect/.*")

message(STATUS "[FINS] SDK Sources: ${SDK_SOURCES}")

add_library(fins_sdk_static STATIC ${SDK_SOURCES})
target_include_directories(fins_sdk_static PUBLIC 
    "%[2]s"
    "%[2]s/fins/third_party/fmt/include"
)
target_link_libraries(fins_sdk_static PUBLIC Threads::Threads ${CMAKE_DL_LIBS})

# Install the static library and headers
install(TARGETS fins_sdk_static 
    ARCHIVE DESTINATION lib
    LIBRARY DESTINATION lib
    RUNTIME DESTINATION bin
)

install(DIRECTORY "%[2]s/fins/" 
    DESTINATION include/fins
    FILES_MATCHING PATTERN "*.h" PATTERN "*.hpp"
)
`, wslSanitizeLogic, sdkPath)

	cmakeListsPath := filepath.Join(sdkBuildDir, "CMakeLists.txt")
	if err := os.WriteFile(cmakeListsPath, []byte(wrapperContent), 0644); err != nil {
		return fmt.Errorf("failed to write SDK CMakeLists.txt: %v", err)
	}

	// Configure SDK build
	args := []string{
		"-B", sdkBuildDir,
		"-S", sdkBuildDir,
		"-G", viper.GetString("build.defaults.cmake_generator"),
		fmt.Sprintf("-DCMAKE_ARCHIVE_OUTPUT_DIRECTORY=%s", filepath.Join(installDir, "lib")),
		fmt.Sprintf("-DCMAKE_INSTALL_PREFIX=%s", installDir),
		"-DCMAKE_BUILD_TYPE=Release",
	}

	// Use provided writer or fallback to stderr
	var outputWriter io.Writer = os.Stderr
	if writer != nil {
		outputWriter = utils.NewBuildWriter(writer)
	}

	cmdConfig := exec.CommandContext(ctx, "cmake", args...)
	if err := runCommandWithColor(ctx, cmdConfig, outputWriter); err != nil {
		return fmt.Errorf("SDK CMake config failed: %v", err)
	}

	// Build SDK static library
	cmdBuild := exec.CommandContext(ctx, "cmake", "--build", sdkBuildDir, "-j", "4")
	if err := runCommandWithColor(ctx, cmdBuild, outputWriter); err != nil {
		return fmt.Errorf("SDK build failed: %v", err)
	}

	// Install SDK
	cmdInstall := exec.CommandContext(ctx, "cmake", "--install", sdkBuildDir)
	if err := runCommandWithColor(ctx, cmdInstall, outputWriter); err != nil {
		return fmt.Errorf("SDK install failed: %v", err)
	}

	return nil
}

func getMoldLibexec() string {
	paths := []string{
		"/usr/libexec/mold",
		"/usr/local/libexec/mold",
		"/usr/lib/mold",
		"/usr/local/lib/mold",
	}

	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}

	return "/usr/libexec/mold"
}

func runCommandWithColor(ctx context.Context, cmd *exec.Cmd, writer io.Writer) error {
	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()

	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		return err
	}

	done := make(chan bool, 2)
	go func() {
		io.Copy(writer, stdout)
		done <- true
	}()
	go func() {
		io.Copy(writer, stderr)
		done <- true
	}()

	cmdErrChan := make(chan error, 1)
	go func() {
		cmdErrChan <- cmd.Wait()
	}()

	var cmdErr error
	select {
	case <-ctx.Done():
		if cmd.Process != nil {
			syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)

			go func() {
				time.Sleep(2 * time.Second)
				syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			}()
		}
		cmdErr = ctx.Err()
	case err := <-cmdErrChan:
		cmdErr = err
	}

	<-done
	<-done

	if f, ok := writer.(interface{ Flush() }); ok {
		f.Flush()
	}

	return cmdErr
}

func CompilePackageStream(ctx context.Context, pkgName string, rawWriter io.Writer) error {
	if _, loaded := buildLocks.LoadOrStore(pkgName, true); loaded {
		return fmt.Errorf("package %s is already being compiled", pkgName)
	}
	defer buildLocks.Delete(pkgName)

	startTime := time.Now()
	pkgs, _ := ScanPackages()
	pkg, exists := pkgs[pkgName]
	if !exists {
		return fmt.Errorf("package %s not found", pkgName)
	}

	// ROS2/colcon packages use colcon build (which handles its own deps)
	if pkg.Type == types.PackageTypeROS2 {
		return compileROS2Package(ctx, pkg, rawWriter)
	}

	if err := SolveDependencies(ctx, pkg, rawWriter, false); err != nil {
		return err
	}

	// Check if SDK static library exists, build it if not
	installDir := utils.ExpandPath(viper.GetString("build.defaults.build_output"))
	sdkStaticLib := filepath.Join(installDir, "lib", "libfins_sdk_static.a")
	if _, err := os.Stat(sdkStaticLib); os.IsNotExist(err) {
		utils.LogSection(rawWriter, "Building SDK static library...")
		if err := CompileSDKStatic(ctx, rawWriter); err != nil {
			return fmt.Errorf("failed to build SDK static library: %v", err)
		}
	}

	var depPaths []string
	var rpathEntries []string

	for lib, ver := range pkg.Meta.Depends {
		if ver == "system" {
			continue
		}

		// --- Key modification: Get hash-based paths ---
		var recipe *types.DependencyRecipe
		if r, ok := pkg.Meta.Recipes[lib]; ok {
			recipe = &r
		} else {
			recipe, _ = LoadGlobalRecipe(lib)
		}

		if recipe != nil {
			// Call dep_manager's public logic to get paths
			installPath, _, _, _ := GetDependencyPaths(lib, ver, recipe)
			path := filepath.ToSlash(installPath)
			depPaths = append(depPaths, path)
			// Add dependency lib directories to RPATH
			rpathEntries = append(rpathEntries, filepath.Join(path, "lib"))
		}
	}

	cmakePrefixPath := strings.Join(depPaths, ";")
	cmakeRpath := strings.Join(rpathEntries, ";")

	var preLoadDeps strings.Builder
	for lib, ver := range pkg.Meta.Depends {
		if ver != "system" {
			preLoadDeps.WriteString(fmt.Sprintf("message(STATUS \"[FINS] Pre-loading dependency: %s\")\n", lib))
			preLoadDeps.WriteString(fmt.Sprintf("find_package(%s REQUIRED)\n", lib))
			preLoadDeps.WriteString(fmt.Sprintf("message(STATUS \"   -- Locked %s to: ${%s_DIR}\")\n", lib, lib))
		}
	}

	sdkPath := utils.ExpandPath(viper.GetString("build.defaults.sdk_path"))
	globalDir := utils.ExpandPath(viper.GetString("build.defaults.build_output"))

	// Determine output directory: workspace packages → <ws>/install/<pkg>,
	// global (GitHub) packages → ~/.fins/install/<pkg>
	type LocalSource struct {
		Name string `mapstructure:"name"`
		Path string `mapstructure:"path"`
	}
	var localSources []LocalSource
	viper.UnmarshalKey("local_packages", &localSources)
	wsPathMap := make(map[string]string)
	for _, src := range localSources {
		wsPathMap[src.Name] = src.Path
	}

	workspaceRoot := findWorkspaceRoot(pkg.Path)
	if workspaceRoot == "" {
		workspaceRoot = filepath.Dir(pkg.Path)
	}

	// lib output directory: structured as <base>/<pkg_name>/
	var libOutputDir string
	if wsPath, ok := wsPathMap[pkg.Source]; ok {
		libOutputDir = filepath.Join(wsPath, "install", pkg.Meta.Name, "lib")
	} else {
		libOutputDir = filepath.Join(globalDir, pkg.Meta.Name, "lib")
	}
	buildDir := filepath.Join(workspaceRoot, "build", pkg.Meta.Name)
	if err := os.MkdirAll(buildDir, 0755); err != nil {
		return fmt.Errorf("failed to create build directory: %v", err)
	}

	wrapperContent := fmt.Sprintf(`
cmake_minimum_required(VERSION 3.16)
project(fins_wrapper LANGUAGES CXX)

find_package(Threads REQUIRED)

set(CMAKE_INSTALL_RPATH_USE_LINK_PATH TRUE)
set(CMAKE_INSTALL_RPATH "%[7]s;%[7]s/lib;%[8]s")

set(FINS_DEP_PATHS "%[1]s")
if(FINS_DEP_PATHS)
	list(INSERT CMAKE_PREFIX_PATH 0 ${FINS_DEP_PATHS})
endif()

set(CMAKE_CXX_STANDARD 20)
set(CMAKE_CXX_STANDARD_REQUIRED YES)
# Globally force define FMT_HEADER_ONLY to prevent subproject overrides
set(CMAKE_CXX_FLAGS "${CMAKE_CXX_FLAGS} -Wall -Wextra -fPIC -DFMT_HEADER_ONLY")
add_compile_definitions(FMT_HEADER_ONLY)

# Ensure internal fmt header path has highest priority
include_directories(BEFORE "%[7]s/include/fins/third_party/fmt/include")
include_directories("%[7]s/include")

# --- FINS PRE-LOAD DEPENDENCIES ---
%[2]s
# ----------------------------------

%[3]s

%[4]s

# === USE PRE-BUILT SDK STATIC LIBRARY ===
add_library(fins_sdk STATIC IMPORTED GLOBAL)
set_target_properties(fins_sdk PROPERTIES
    IMPORTED_LOCATION "%[7]s/lib/libfins_sdk_static.a"
    INTERFACE_INCLUDE_DIRECTORIES "%[7]s/include;%[7]s/include/fins/third_party/fmt/include"
)

target_link_libraries(fins_sdk INTERFACE 
    Threads::Threads 
    ${CMAKE_DL_LIBS}
)

add_library(fins_shared ALIAS fins_sdk)

macro(fins_add_node _target)
    add_library(${_target} SHARED ${ARGN})
    target_link_libraries(${_target} PRIVATE 
        fins_sdk
        Threads::Threads
        ${CMAKE_DL_LIBS}
    )
    target_compile_definitions(${_target} PRIVATE FMT_HEADER_ONLY FINS_NODE)
    set_target_properties(${_target} PROPERTIES 
        OUTPUT_NAME "${_target}"
        POSITION_INDEPENDENT_CODE ON
        INSTALL_RPATH "%[7]s;%[7]s/lib;%[8]s"
    )
endmacro()

add_compile_definitions(PKG_NAME="${FINS_META_NAME}")
add_compile_definitions(PKG_VERSION="${FINS_META_VERSION}")
add_compile_definitions(PKG_MAINTAINER="${FINS_META_MAINTAINER}")
add_compile_definitions(PKG_DESCRIPTION="${FINS_META_DESC}")
add_compile_definitions(PKG_SOURCE="${FINS_META_SOURCE}")

add_subdirectory("%[6]s" "${CMAKE_BINARY_DIR}/node_build")

if(TARGET ${FINS_META_NAME})
    set_target_properties(${FINS_META_NAME} PROPERTIES OUTPUT_NAME "${FINS_META_NAME}")
endif()
`, cmakePrefixPath, preLoadDeps.String(), wslSanitizeLogic, link_ros_dependencies, sdkPath, pkg.Path, installDir, cmakeRpath)

	os.WriteFile(filepath.Join(buildDir, "CMakeLists.txt"), []byte(wrapperContent), 0644)

	currentPreset := viper.GetString("build.default_preset")
	presetKey := fmt.Sprintf("build.presets.%s", currentPreset)

	buildType := viper.GetString(presetKey + ".build_type")
	if buildType == "" {
		buildType = "Release"
	}
	sanitize := viper.GetString(presetKey + ".sanitize")
	cmakeArgs := viper.GetStringSlice(presetKey + ".cmake_args")

	args := []string{
		"-B", buildDir,
		"-S", buildDir,
		"-G", viper.GetString("build.defaults.cmake_generator"),
		fmt.Sprintf("-DCMAKE_LIBRARY_OUTPUT_DIRECTORY=%s", libOutputDir),
		fmt.Sprintf("-DCMAKE_BUILD_TYPE=%s", buildType),
		fmt.Sprintf("-DFINS_META_NAME=%s", pkg.Meta.Name),
		fmt.Sprintf("-DFINS_META_VERSION=%s", pkg.Meta.Version),
		fmt.Sprintf("-DFINS_META_MAINTAINER=\"%s\"", pkg.Meta.Maintainers[0].Name),
		fmt.Sprintf("-DFINS_META_DESC=\"%s\"", pkg.Meta.Description),
		fmt.Sprintf("-DFINS_META_SOURCE=%s", pkg.Source),
	}

	if sanitize != "" && sanitize != "none" {
		sanFlags := fmt.Sprintf("-fsanitize=%s", sanitize)
		args = append(args, fmt.Sprintf("-DCMAKE_CXX_FLAGS=%s", sanFlags))
		args = append(args, fmt.Sprintf("-DCMAKE_SHARED_LINKER_FLAGS=%s", sanFlags))
		args = append(args, fmt.Sprintf("-DCMAKE_EXE_LINKER_FLAGS=%s", sanFlags))
		args = append(args, fmt.Sprintf("-DCMAKE_MODULE_LINKER_FLAGS=%s", sanFlags))
	}

	args = append(args, cmakeArgs...)

	if _, err := exec.LookPath("mold"); err == nil {
		useFuseLd := false
		if out, err := exec.Command("gcc", "-dumpversion").Output(); err == nil {
			major := strings.Split(strings.TrimSpace(string(out)), ".")[0]
			if major >= "10" {
				useFuseLd = true
			}
		}

		if useFuseLd {
			args = append(args,
				"-DCMAKE_EXE_LINKER_FLAGS=-fuse-ld=mold",
				"-DCMAKE_SHARED_LINKER_FLAGS=-fuse-ld=mold",
				"-DCMAKE_MODULE_LINKER_FLAGS=-fuse-ld=mold",
			)
		} else {
			moldLibexec := getMoldLibexec()
			flag := fmt.Sprintf("-B%s", moldLibexec)
			args = append(args,
				fmt.Sprintf("-DCMAKE_EXE_LINKER_FLAGS=%s", flag),
				fmt.Sprintf("-DCMAKE_SHARED_LINKER_FLAGS=%s", flag),
				fmt.Sprintf("-DCMAKE_MODULE_LINKER_FLAGS=%s", flag),
			)
		}
	}

	utils.LogSection(rawWriter, "Configuring %s (Preset: %s)", pkgName, currentPreset)

	buildWriter := utils.NewBuildWriter(rawWriter)

	cmdConfig := exec.CommandContext(ctx, "cmake", args...)

	if err := runCommandWithColor(ctx, cmdConfig, buildWriter); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("CMake Config failed: %v", err)
	}

	utils.LogSection(rawWriter, "Building %s...", pkgName)

	targetName := pkg.Meta.Name
	buildJobs := viper.GetString("build.defaults.build_jobs")
	if buildJobs == "" {
		buildJobs = "4"
	}
	buildArgs := []string{"--build", buildDir, "--target", targetName, "-j", buildJobs}

	cmdBuild := exec.CommandContext(ctx, "cmake", buildArgs...)
	if err := runCommandWithColor(ctx, cmdBuild, buildWriter); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("Build failed: %v", err)
	}

	elapsed := time.Since(startTime)
	utils.LogSuccess(rawWriter, "Build Completed Successfully! (%.1fs)", elapsed.Seconds())
	return nil
}

// CompileWorkspace builds all packages in a workspace, handling mixed fins+ros2 workspaces.
// It first builds fins native packages, then runs colcon build for ROS2 packages,
// inheriting preset configuration for colcon args.
func CompileWorkspace(ctx context.Context, workspacePath string, writer io.Writer) error {
	// Scan all packages
	allPkgs, err := ScanPackages()
	if err != nil {
		return fmt.Errorf("failed to scan packages: %v", err)
	}

	// Filter by workspace path
	var finsPkgs []*types.Package
	var ros2Pkgs []*types.Package
	for _, pkg := range allPkgs {
		rel, err := filepath.Rel(workspacePath, pkg.Path)
		if err == nil && !strings.HasPrefix(rel, "..") {
			switch pkg.Type {
			case types.PackageTypeROS2:
				ros2Pkgs = append(ros2Pkgs, pkg)
			default:
				finsPkgs = append(finsPkgs, pkg)
			}
		}
	}

	if len(finsPkgs) == 0 && len(ros2Pkgs) == 0 {
		return fmt.Errorf("no packages found in workspace: %s", workspacePath)
	}

	utils.LogSection(writer, "Workspace build: %d fins + %d ros2 packages", len(finsPkgs), len(ros2Pkgs))

	// If there are ROS2 packages, add COLCON_IGNORE to fins package dirs
	// so colcon doesn't try to build them
	var colconIgnoreDirs []string
	if len(ros2Pkgs) > 0 && len(finsPkgs) > 0 {
		utils.LogInfo(writer, "Adding COLCON_IGNORE markers to %d fins package(s)...", len(finsPkgs))
		for _, pkg := range finsPkgs {
			ignoreFile := filepath.Join(pkg.Path, "COLCON_IGNORE")
			if err := os.WriteFile(ignoreFile, []byte{}, 0644); err != nil {
				// Clean up any already-created ignore files on failure
				for _, d := range colconIgnoreDirs {
					os.Remove(filepath.Join(d, "COLCON_IGNORE"))
				}
				return fmt.Errorf("failed to create COLCON_IGNORE in %s: %v", pkg.Path, err)
			}
			colconIgnoreDirs = append(colconIgnoreDirs, pkg.Path)
		}
		// Ensure cleanup after build completes
		defer func() {
			utils.LogInfo(writer, "Removing COLCON_IGNORE markers...")
			for _, d := range colconIgnoreDirs {
				os.Remove(filepath.Join(d, "COLCON_IGNORE"))
			}
		}()
	}

	// Phase 1: Build all fins native packages
	for _, pkg := range finsPkgs {
		fullName := fmt.Sprintf("%s/%s", pkg.Source, pkg.Meta.Name)
		utils.LogSection(writer, "Building fins package: %s", fullName)
		if err := CompilePackageStream(ctx, fullName, writer); err != nil {
			return fmt.Errorf("fins package '%s' build failed: %v", fullName, err)
		}
	}

	// Phase 2: Run colcon build for ROS2 packages
	if len(ros2Pkgs) > 0 {
		utils.LogSection(writer, "Running colcon build for %d ROS2 package(s)...", len(ros2Pkgs))
		if err := runColconBuild(ctx, workspacePath, writer, ""); err != nil {
			return fmt.Errorf("colcon build failed: %v", err)
		}
		utils.LogSuccess(writer, "colcon build completed successfully")
	}

	utils.LogSuccess(writer, "Workspace build completed")
	return nil
}

// compileROS2Package builds a single ROS2 package using colcon from its workspace root.
func compileROS2Package(ctx context.Context, pkg *types.Package, writer io.Writer) error {
	workspaceRoot := findWorkspaceRoot(pkg.Path)
	if workspaceRoot == "" {
		return fmt.Errorf("could not determine workspace root for ROS2 package: %s", pkg.Meta.Name)
	}

	utils.LogSection(writer, "Building ROS2 package %s with colcon...", pkg.Meta.Name)
	return runColconBuild(ctx, workspaceRoot, writer, pkg.Meta.Name)
}

// findWorkspaceRoot walks up from pkgPath to find the registered workspace root.
func findWorkspaceRoot(pkgPath string) string {
	type LocalSource struct {
		Name string `mapstructure:"name"`
		Path string `mapstructure:"path"`
	}
	var localSources []LocalSource
	if err := viper.UnmarshalKey("local_packages", &localSources); err != nil {
		return ""
	}
	for _, src := range localSources {
		rel, err := filepath.Rel(src.Path, pkgPath)
		if err == nil && !strings.HasPrefix(rel, "..") {
			return src.Path
		}
	}
	return ""
}

// runColconBuild executes colcon build with inherited preset configuration.
// If pkgName is non-empty, --packages-select is added to build a single package.
func runColconBuild(ctx context.Context, workspacePath string, writer io.Writer, pkgName string) error {
	args := []string{"build"}

	// If building a single package, use --packages-select
	if pkgName != "" {
		args = append(args, "--packages-select", pkgName)
	}

	// Inherit presets
	currentPreset := viper.GetString("build.default_preset")
	presetKey := fmt.Sprintf("build.presets.%s", currentPreset)

	buildType := viper.GetString(presetKey + ".build_type")
	if buildType == "" {
		buildType = "Release"
	}

	cmakeArgs := []string{
		fmt.Sprintf("-DCMAKE_BUILD_TYPE=%s", buildType),
	}

	presetCmakeArgs := viper.GetStringSlice(presetKey + ".cmake_args")
	cmakeArgs = append(cmakeArgs, presetCmakeArgs...)

	// Use console_direct+ for unfiltered colcon output
	args = append(args, "--event-handlers", "console_direct+")

	// Use a single --cmake-args followed by all cmake args
	args = append(args, "--cmake-args")

	// Add cmake generator from config
	cmakeGenerator := viper.GetString("build.defaults.cmake_generator")
	if cmakeGenerator != "" {
		args = append(args, fmt.Sprintf("-G%s", cmakeGenerator))
	}

	args = append(args, cmakeArgs...)

	// Add parallel workers
	buildJobs := viper.GetString("build.defaults.build_jobs")
	if buildJobs != "" {
		args = append(args, "--parallel-workers", buildJobs)
	}

	utils.LogInfo(writer, "colcon %s", strings.Join(args, " "))

	// Source the SDK setup.bash so colcon/CMake can find finevision and other dependencies
	sdkInstallDir := utils.ExpandPath("~/.fins/sdk/install")
	setupScript := filepath.Join(sdkInstallDir, "setup.bash")
	// Shell-quote each argument to preserve spaces in cmake flags
	quotedArgs := make([]string, len(args))
	for i, a := range args {
		quotedArgs[i] = strconv.Quote(a)
	}
	shellCmd := fmt.Sprintf("source %s && colcon %s", strconv.Quote(setupScript), strings.Join(quotedArgs, " "))

	cmd := exec.CommandContext(ctx, "bash", "-c", shellCmd)
	cmd.Dir = workspacePath

	// Use direct writer so colcon's own formatting is preserved
	cmd.Stdout = writer
	cmd.Stderr = writer

	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}

	return nil
}

func CleanAllBuilds() error {
	pkgs, _ := ScanPackages()
	for _, pkg := range pkgs {
		cleanPackageBuildDir(pkg)
	}

	// Clean isolated core build directory
	coreBuildRoot := filepath.Join(utils.GetFinsHome(), "build", "core")
	if _, err := os.Stat(coreBuildRoot); err == nil {
		utils.LogSection(os.Stdout, "Cleaning %s", coreBuildRoot)
		os.RemoveAll(coreBuildRoot)
	}

	return nil
}

func cleanPackageBuildDir(pkg *types.Package) {
	workspaceRoot := findWorkspaceRoot(pkg.Path)
	if workspaceRoot == "" {
		workspaceRoot = filepath.Dir(pkg.Path)
	}
	newBuildPath := filepath.Join(workspaceRoot, "build", pkg.Meta.Name)
	if _, err := os.Stat(newBuildPath); err == nil {
		utils.LogSection(os.Stdout, "Cleaning %s", newBuildPath)
		os.RemoveAll(newBuildPath)
	}
	// Also clean legacy per-package build directory (transitional)
	oldBuildPath := filepath.Join(pkg.Path, "build")
	if _, err := os.Stat(oldBuildPath); err == nil {
		utils.LogSection(os.Stdout, "Cleaning %s", oldBuildPath)
		os.RemoveAll(oldBuildPath)
	}
}

func CleanPackageBuild(pkgName string) error {
	pkgs, _ := ScanPackages()
	if pkg, exists := pkgs[pkgName]; exists {
		cleanPackageBuildDir(pkg)
	} else {
		return fmt.Errorf("package %s not found", pkgName)
	}
	return nil
}

func CleanWorkspaceBuilds(workspacePath string) error {
	pkgs, _ := ScanPackages()
	for _, pkg := range pkgs {
		rel, err := filepath.Rel(workspacePath, pkg.Path)
		if err == nil && !strings.HasPrefix(rel, "..") {
			cleanPackageBuildDir(pkg)
		}
	}
	return nil
}

func CompileExe(ctx context.Context, writer io.Writer, name string) error {
	startTime := time.Now()
	sdkPath := utils.ExpandPath(viper.GetString("build.defaults.sdk_path"))
	binDir := utils.ExpandPath(viper.GetString("build.defaults.build_output"))

	exeSourceDir := filepath.Join(sdkPath, "fins", name)

	buildDir := filepath.Join(utils.GetFinsHome(), "build", "core", name)
	os.MkdirAll(buildDir, 0755)

	srcPath := filepath.Join(exeSourceDir, name+".cpp")

	wrapperContent := fmt.Sprintf(`
cmake_minimum_required(VERSION 3.16)
project(fins_%[1]s_wrapper)

find_package(Threads REQUIRED)

set(CMAKE_CXX_STANDARD 20)
set(CMAKE_CXX_STANDARD_REQUIRED YES)

%[2]s

add_subdirectory("%[3]s" "${CMAKE_BINARY_DIR}/sdk_build")

if(EXISTS "%[4]s")
    add_executable(%[1]s "%[4]s")

    # Force whole archive to ensure all symbols (singletons, etc) are baked into the binary
    # and ready to be exported via -rdynamic for plugins.
    if(CMAKE_CXX_COMPILER_ID MATCHES "Clang|GNU")
        target_link_libraries(%[1]s PRIVATE
            -Wl,--whole-archive fins_sdk -Wl,--no-whole-archive
            ${CMAKE_DL_LIBS}
            Threads::Threads
        )
    else()
        target_link_libraries(%[1]s PRIVATE fins_sdk ${CMAKE_DL_LIBS})
    endif()

    # Optional ROS2 linking (needed by ros2_manager.hpp at the executable level)
    find_package(rclcpp QUIET)
    if(rclcpp_FOUND)
        target_link_libraries(%[1]s PRIVATE rclcpp::rclcpp)
    endif()

    set_target_properties(%[1]s PROPERTIES ENABLE_EXPORTS ON)
else()
    message(FATAL_ERROR "Source file not found at: %[4]s")
endif()
`, name, wslSanitizeLogic, sdkPath, srcPath)

	if err := os.WriteFile(filepath.Join(buildDir, "CMakeLists.txt"), []byte(wrapperContent), 0644); err != nil {
		return fmt.Errorf("failed to write CMakeLists.txt: %v", err)
	}

	currentPreset := viper.GetString("build.default_preset")
	presetKey := fmt.Sprintf("build.presets.%s", currentPreset)

	buildType := viper.GetString(presetKey + ".build_type")
	if buildType == "" {
		buildType = "Release"
	}

	args := []string{
		"-B", buildDir,
		"-S", buildDir,
		"-G", viper.GetString("build.defaults.cmake_generator"),
		fmt.Sprintf("-DCMAKE_RUNTIME_OUTPUT_DIRECTORY=%s", binDir),
		fmt.Sprintf("-DCMAKE_BUILD_TYPE=%s", buildType),
	}

	linkerFlags := ""
	if _, err := exec.LookPath("mold"); err == nil {
		useFuseLd := false
		if out, err := exec.Command("gcc", "-dumpversion").Output(); err == nil {
			major := strings.Split(strings.TrimSpace(string(out)), ".")[0]
			if major >= "10" {
				useFuseLd = true
			}
		}

		if useFuseLd {
			linkerFlags += " -fuse-ld=mold"
		} else {
			moldLibexec := getMoldLibexec()
			linkerFlags += fmt.Sprintf(" -B%s", moldLibexec)
		}
	}
	args = append(args, fmt.Sprintf("-DCMAKE_EXE_LINKER_FLAGS=%s", linkerFlags))

	utils.LogSection(writer, "Configuring %s (Type: %s)", name, buildType)
	buildWriter := utils.NewBuildWriter(writer)

	cmdConfig := exec.CommandContext(ctx, "cmake", args...)
	if err := runCommandWithColor(ctx, cmdConfig, buildWriter); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("CMake Config failed: %v", err)
	}

	utils.LogSection(writer, "Building %s...", name)
	buildJobs := viper.GetString("build.defaults.build_jobs")
	if buildJobs == "" {
		buildJobs = "4"
	}

	cmdBuild := exec.CommandContext(ctx, "cmake", "--build", buildDir, "-j", buildJobs)
	if err := runCommandWithColor(ctx, cmdBuild, buildWriter); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("Build failed: %v", err)
	}

	elapsed := time.Since(startTime)
	utils.LogSuccess(writer, "%s Build Completed Successfully! (%.1fs)", name, elapsed.Seconds())
	return nil
}

func CompileAgent(ctx context.Context, writer io.Writer) error {
	return CompileExe(ctx, writer, "agent")
}

func CompileInspect(ctx context.Context, writer io.Writer) error {
	return CompileExe(ctx, writer, "inspect")
}
