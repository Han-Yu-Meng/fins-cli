package core

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"finsd/internal/utils"

	"github.com/spf13/viper"
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
                message(STATUS "🧹 Clearing polluted cache var: ${_var} = ${_val}")
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

// runCommandWithColor 执行命令并实时输出到 writer，使用管道确保实时性
func runCommandWithColor(cmd *exec.Cmd, writer io.Writer) error {
	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()

	if err := cmd.Start(); err != nil {
		return err
	}

	// 并发读取 stdout 和 stderr
	done := make(chan bool, 2)
	go func() {
		io.Copy(writer, stdout)
		done <- true
	}()
	go func() {
		io.Copy(writer, stderr)
		done <- true
	}()

	// 等待命令完成
	cmdErr := cmd.Wait()

	<-done
	<-done

	if f, ok := writer.(interface{ Flush() }); ok {
		f.Flush()
	}

	return cmdErr
}

func CompilePackageStream(pkgName string, rawWriter io.Writer) error {
	pkgs, _ := ScanPackages()
	pkg, exists := pkgs[pkgName]
	if !exists {
		return fmt.Errorf("package %s not found", pkgName)
	}

	if err := SolveDependencies(pkg, rawWriter, false); err != nil {
		return err
	}

	depRoot := GetDepRoot()
	var depPaths []string

	for lib, ver := range pkg.Meta.Depends {
		if ver == "system" {
			continue
		}
		path := filepath.Join(depRoot, "install", lib, ver)
		path = filepath.ToSlash(path)
		depPaths = append(depPaths, path)
	}

	cmakePrefixPath := strings.Join(depPaths, ";")

	var preLoadDeps strings.Builder
	for lib, ver := range pkg.Meta.Depends {
		if ver != "system" {
			preLoadDeps.WriteString(fmt.Sprintf("message(STATUS \"[FINS] Pre-loading dependency: %s\")\n", lib))
			preLoadDeps.WriteString(fmt.Sprintf("find_package(%s REQUIRED)\n", lib))
			preLoadDeps.WriteString(fmt.Sprintf("message(STATUS \"   -- Locked %s to: ${%s_DIR}\")\n", lib, lib))
		}
	}

	sdkPath := utils.ExpandPath(viper.GetString("build.defaults.sdk_path"))
	binDir := utils.ExpandPath(viper.GetString("build.defaults.build_output"))

	buildDir := filepath.Join(pkg.Path, "build")
	if err := os.MkdirAll(buildDir, 0755); err != nil {
		return fmt.Errorf("failed to create build directory: %v", err)
	}

	wrapperContent := fmt.Sprintf(`
cmake_minimum_required(VERSION 3.16)
project(fins_wrapper)

find_package(Threads REQUIRED)

set(FINS_DEP_PATHS "%s")
if(FINS_DEP_PATHS)
	list(INSERT CMAKE_PREFIX_PATH 0 ${FINS_DEP_PATHS})
	message(STATUS "FINS: Injected local dependencies: ${CMAKE_PREFIX_PATH}")
endif()

set(CMAKE_CXX_STANDARD 20)
set(CMAKE_CXX_STANDARD_REQUIRED YES)
set(CMAKE_CXX_FLAGS "${CMAKE_CXX_FLAGS} -Wall -Wextra -fPIC")

# --- FINS PRE-LOAD DEPENDENCIES ---
%s
# ----------------------------------

macro(fins_add_node _target)
    add_library(${_target} SHARED ${ARGN})
    target_link_libraries(${_target} PRIVATE fins_sdk)
    set_target_properties(${_target} PROPERTIES OUTPUT_NAME "${PKG_SOURCE}_${_target}")
    target_compile_definitions(${_target} PRIVATE FINS_NODE)
endmacro()

%s

%s

add_compile_definitions(PKG_NAME="${FINS_META_NAME}")
add_compile_definitions(PKG_VERSION="${FINS_META_VERSION}")
add_compile_definitions(PKG_MAINTAINER="${FINS_META_MAINTAINER}")
add_compile_definitions(PKG_DESCRIPTION="${FINS_META_DESC}")
add_compile_definitions(PKG_SOURCE="${FINS_META_SOURCE}")

add_subdirectory("%s" "${CMAKE_BINARY_DIR}/sdk_build")
add_subdirectory("%s" "${CMAKE_BINARY_DIR}/node_build")

if(TARGET ${FINS_META_NAME})
    set_target_properties(${FINS_META_NAME} PROPERTIES OUTPUT_NAME "${FINS_META_SOURCE}_${FINS_META_NAME}")
endif()
`, cmakePrefixPath, preLoadDeps.String(), wslSanitizeLogic, link_ros_dependencies, sdkPath, pkg.Path)

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
		fmt.Sprintf("-DCMAKE_LIBRARY_OUTPUT_DIRECTORY=%s", binDir),
		fmt.Sprintf("-DCMAKE_BUILD_TYPE=%s", buildType),
		fmt.Sprintf("-DFINS_META_NAME=%s", pkg.Meta.Name),
		fmt.Sprintf("-DFINS_META_VERSION=%s", pkg.Meta.Version),
		fmt.Sprintf("-DFINS_META_MAINTAINER=\"%s\"", pkg.Meta.Maintainers[0].Name),
		fmt.Sprintf("-DFINS_META_DESC=\"%s\"", pkg.Meta.Description),
		fmt.Sprintf("-DFINS_META_SOURCE=%s", pkg.Source),
	}

	var sanFlags string
	if sanitize != "" && sanitize != "none" {
		sanFlags = fmt.Sprintf("-fsanitize=%s", sanitize)
	}
	args = append(args, fmt.Sprintf("-DCMAKE_CXX_FLAGS=%s", sanFlags))
	args = append(args, fmt.Sprintf("-DCMAKE_SHARED_LINKER_FLAGS=%s", sanFlags))
	args = append(args, fmt.Sprintf("-DCMAKE_EXE_LINKER_FLAGS=%s", sanFlags))
	args = append(args, fmt.Sprintf("-DCMAKE_MODULE_LINKER_FLAGS=%s", sanFlags))

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
			moldLibexec := "/usr/libexec/mold"
			if _, err := os.Stat(moldLibexec); os.IsNotExist(err) {
				moldLibexec = "/usr/local/libexec/mold"
			}
			flag := fmt.Sprintf("-B%s", moldLibexec)
			args = append(args,
				fmt.Sprintf("-DCMAKE_EXE_LINKER_FLAGS=%s", flag),
				fmt.Sprintf("-DCMAKE_SHARED_LINKER_FLAGS=%s", flag),
				fmt.Sprintf("-DCMAKE_MODULE_LINKER_FLAGS=%s", flag),
			)
		}
	}

	// 1. 使用 LogSection 打印 FINS 标题
	utils.LogSection(rawWriter, "Configuring %s (Preset: %s)", pkgName, currentPreset)

	// 2. 创建一个带缩进的 Writer 给 CMake 使用
	buildWriter := utils.NewBuildWriter(rawWriter)

	cmdConfig := exec.Command("cmake", args...)

	// 3. 传入缩进 Writer
	if err := runCommandWithColor(cmdConfig, buildWriter); err != nil {
		return fmt.Errorf("CMake Config failed: %v", err)
	}

	utils.LogSection(rawWriter, "Building %s...", pkgName)

	targetName := pkg.Meta.Name
	buildJobs := viper.GetString("build.defaults.build_jobs")
	if buildJobs == "" {
		buildJobs = "4"
	}
	buildArgs := []string{"--build", buildDir, "--target", targetName, "-j", buildJobs}

	cmdBuild := exec.Command("cmake", buildArgs...)
	if err := runCommandWithColor(cmdBuild, buildWriter); err != nil {
		return fmt.Errorf("Build failed: %v", err)
	}

	utils.LogSuccess(rawWriter, "Build Completed Successfully!")
	return nil
}

func CleanAllBuilds() error {
	pkgs, _ := ScanPackages()
	for _, pkg := range pkgs {
		buildPath := filepath.Join(pkg.Path, "build")
		if _, err := os.Stat(buildPath); err == nil {
			utils.LogSection(os.Stdout, "Cleaning %s", buildPath)
			os.RemoveAll(buildPath)
		}
	}

	sdkPath := utils.ExpandPath(viper.GetString("build.defaults.sdk_path"))
	coreExes := []string{"agent", "inspect"}
	for _, name := range coreExes {
		buildPath := filepath.Join(sdkPath, "fins", name, "build")
		if _, err := os.Stat(buildPath); err == nil {
			utils.LogSection(os.Stdout, "Cleaning %s", buildPath)
			os.RemoveAll(buildPath)
		}
	}

	return nil
}

func CompileExe(writer io.Writer, name string) error {
	sdkPath := utils.ExpandPath(viper.GetString("build.defaults.sdk_path"))
	binDir := utils.ExpandPath(viper.GetString("build.defaults.build_output"))

	exeSourceDir := filepath.Join(sdkPath, "fins", name)
	buildDir := filepath.Join(exeSourceDir, "build")
	os.MkdirAll(buildDir, 0755)

	srcPath := filepath.Join(exeSourceDir, name+".cpp")

	wrapperContent := fmt.Sprintf(`
cmake_minimum_required(VERSION 3.16)
project(fins_%[1]s_wrapper)

set(CMAKE_CXX_STANDARD 20)
set(CMAKE_CXX_STANDARD_REQUIRED YES)

%[2]s

add_subdirectory("%[3]s" "${CMAKE_BINARY_DIR}/sdk_build")

if(EXISTS "%[4]s")
    add_executable(%[1]s "%[4]s")
    
    target_link_libraries(%[1]s PRIVATE fins_sdk ${CMAKE_DL_LIBS})
    
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

	if _, err := exec.LookPath("mold"); err == nil {
		useFuseLd := false
		if out, err := exec.Command("gcc", "-dumpversion").Output(); err == nil {
			major := strings.Split(string(out), ".")[0]
			if major >= "10" {
				useFuseLd = true
			}
		}

		if useFuseLd {
			args = append(args, "-DCMAKE_EXE_LINKER_FLAGS=-fuse-ld=mold")
		} else {
			moldLibexec := "/usr/libexec/mold"
			if _, err := os.Stat(moldLibexec); os.IsNotExist(err) {
				moldLibexec = "/usr/local/libexec/mold"
			}
			args = append(args, fmt.Sprintf("-DCMAKE_EXE_LINKER_FLAGS=-B%s", moldLibexec))
		}
	}

	utils.LogSection(writer, "Configuring %s (Type: %s)", name, buildType)
	buildWriter := utils.NewBuildWriter(writer)

	if err := runCommandWithColor(exec.Command("cmake", args...), buildWriter); err != nil {
		return fmt.Errorf("CMake Config failed: %v", err)
	}

	utils.LogSection(writer, "Building %s...", name)
	buildJobs := viper.GetString("build.defaults.build_jobs")
	if buildJobs == "" {
		buildJobs = "4"
	}

	if err := runCommandWithColor(exec.Command("cmake", "--build", buildDir, "-j", buildJobs), buildWriter); err != nil {
		return fmt.Errorf("Build failed: %v", err)
	}

	utils.LogSuccess(writer, "%s Build Completed Successfully!", name)
	return nil
}

func CompileAgent(writer io.Writer) error {
	return CompileExe(writer, "agent")
}

func CompileInspect(writer io.Writer) error {
	return CompileExe(writer, "inspect")
}
