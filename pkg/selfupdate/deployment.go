package selfupdate

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/nuomiiiii/lite/cmd/flags"
)

const (
	DeploymentDocker  = "docker"
	DeploymentLinux   = "linux"
	DeploymentWindows = "windows"
	DeploymentUnknown = "unknown"
	defaultService    = "lite.service"
	managerSystemd    = "systemd"
	managerProcd      = "procd"
)

type Capability struct {
	Deployment          string        `json:"deployment"`
	Distribution        string        `json:"distribution,omitempty"`
	DistributionVersion string        `json:"distribution_version,omitempty"`
	Supported           bool          `json:"supported"`
	Reason              string        `json:"reason,omitempty"`
	Service             string        `json:"service,omitempty"`
	LastResult          *UpdateResult `json:"last_result,omitempty"`
}

func firstEnv(keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return ""
}

func DeploymentType() string {
	switch strings.ToLower(firstEnv("LITE_DEPLOYMENT", "KOMARI_DEPLOYMENT")) {
	case DeploymentDocker:
		return DeploymentDocker
	case DeploymentLinux, "binary":
		return DeploymentLinux
	case DeploymentWindows:
		return DeploymentWindows
	}
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return DeploymentDocker
	}
	if runtime.GOOS == "linux" {
		return DeploymentLinux
	}
	if runtime.GOOS == "windows" {
		return DeploymentWindows
	}
	return DeploymentUnknown
}

func DetectCapability() Capability {
	deployment := DeploymentType()
	result := Capability{Deployment: deployment}
	if localPatchMarker != "" {
		result.Reason = reasonLocalPatch
		return result
	}
	result.Distribution, result.DistributionVersion = linuxDistribution()
	if deployment != DeploymentLinux || runtime.GOOS != "linux" {
		result.Reason = "not_managed_linux"
		return result
	}
	if output, err := exec.Command("id", "-u").Output(); err != nil || strings.TrimSpace(string(output)) != "0" {
		result.Reason = "root_required"
		return result
	}
	manager := detectServiceManager()
	switch manager {
	case managerSystemd:
		if _, err := exec.LookPath("systemctl"); err != nil {
			result.Reason = "systemd_unavailable"
			return result
		}
		if _, err := exec.LookPath("systemd-run"); err != nil {
			result.Reason = "systemd_run_unavailable"
			return result
		}
	case managerProcd:
		if _, err := os.Stat("/etc/init.d/" + serviceBaseName(serviceName())); err != nil {
			result.Reason = "procd_init_unavailable"
			return result
		}
		if !hasDetachedHelperLauncher() {
			result.Reason = "helper_launcher_unavailable"
			return result
		}
	default:
		result.Reason = "systemd_unavailable"
		return result
	}

	service := serviceName()
	result.Service = service
	mainPID := lookupServicePID(service)
	if mainPID == "" || mainPID != strconv.Itoa(os.Getpid()) {
		result.Reason = "service_mismatch"
		return result
	}

	executable, dataDir, err := managedPaths()
	if err != nil {
		result.Reason = "unsupported_layout"
		return result
	}
	if info, err := os.Stat(executable); err != nil || !info.Mode().IsRegular() {
		result.Reason = "executable_unavailable"
		return result
	}
	if info, err := os.Lstat(dataDir); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		result.Reason = "data_directory_unavailable"
		return result
	}
	if isMountPoint(dataDir) {
		result.Reason = "data_directory_is_mount"
		return result
	}

	result.Supported = true
	result.LastResult, _ = ReadLastResult(filepath.Dir(executable))
	return result
}

func linuxDistribution() (string, string) {
	content, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return "", ""
	}
	values := make(map[string]string)
	for _, line := range strings.Split(string(content), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		values[key] = strings.Trim(strings.TrimSpace(value), `"'`)
	}
	return values["ID"], values["VERSION_ID"]
}

func managedPaths() (string, string, error) {
	executable, err := os.Executable()
	if err != nil {
		return "", "", err
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		return "", "", err
	}
	workingDir, err := os.Getwd()
	if err != nil {
		return "", "", err
	}
	workingDir, err = filepath.Abs(workingDir)
	if err != nil {
		return "", "", err
	}
	dataDir := filepath.Join(workingDir, "data")
	if !pathWithin(dataDir, workingDir) || filepath.Clean(dataDir) == filepath.Clean(workingDir) {
		return "", "", errors.New("invalid data directory")
	}
	if !configuredDatabaseWithin(dataDir, workingDir) {
		return "", "", errors.New("database is outside the managed data directory")
	}
	return executable, dataDir, nil
}

func configuredDatabaseWithin(dataDir, workingDir string) bool {
	databasePath := "./data/lite.db"
	for i := 1; i < len(os.Args); i++ {
		arg := os.Args[i]
		switch {
		case arg == "-d" || arg == "--database":
			if i+1 < len(os.Args) {
				databasePath = os.Args[i+1]
			}
		case strings.HasPrefix(arg, "--database="):
			databasePath = strings.TrimPrefix(arg, "--database=")
		}
	}
	if strings.HasPrefix(strings.ToLower(databasePath), "file:") {
		databasePath = strings.TrimPrefix(databasePath, "file:")
		if index := strings.IndexByte(databasePath, '?'); index >= 0 {
			databasePath = databasePath[:index]
		}
	}
	if !filepath.IsAbs(databasePath) {
		databasePath = filepath.Join(workingDir, databasePath)
	}
	return pathWithin(databasePath, dataDir)
}

func PathWithinData(path, dataDir, workingDir string) bool {
	path = strings.TrimSpace(path)
	if path == "" {
		path = "./data/metrics.db"
	}
	if strings.HasPrefix(strings.ToLower(path), "file:") {
		path = strings.TrimPrefix(path, "file:")
		if index := strings.IndexByte(path, '?'); index >= 0 {
			path = path[:index]
		}
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(workingDir, path)
	}
	return pathWithin(path, dataDir)
}

func pathWithin(path, parent string) bool {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	absParent, err := filepath.Abs(parent)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(absParent, absPath)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func detectServiceManager() string {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("LITE_SERVICE_MANAGER"))) {
	case managerProcd:
		return managerProcd
	case managerSystemd:
		return managerSystemd
	}
	// systemd-run helpers do not inherit lite.service env. Treat a live
	// systemd host as systemd so 2.3.2 Linux installs keep using systemctl.
	if _, err := os.Stat("/run/systemd/system"); err == nil {
		return managerSystemd
	}
	if _, err := os.Stat("/etc/openwrt_release"); err == nil {
		return managerProcd
	}
	if _, err := os.Stat("/etc/openwrt_version"); err == nil {
		return managerProcd
	}
	if _, err := exec.LookPath("procd"); err == nil {
		return managerProcd
	}
	if _, err := exec.LookPath("systemctl"); err == nil {
		return managerSystemd
	}
	return ""
}

func serviceBaseName(name string) string {
	return strings.TrimSuffix(strings.TrimSpace(name), ".service")
}

func normalizeServiceName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	if detectServiceManager() == managerProcd {
		return serviceBaseName(name)
	}
	if !strings.HasSuffix(name, ".service") {
		name += ".service"
	}
	return name
}

func explicitServiceName() string {
	return normalizeServiceName(firstEnv("LITE_SERVICE_NAME", "KOMARI_SERVICE_NAME"))
}

var lookupServicePID = serviceMainPID
var lookupPath = exec.LookPath
var procdPIDFilePaths = defaultProcdPIDFilePaths

func defaultProcdPIDFilePaths(name string) []string {
	base := serviceBaseName(name)
	return []string{
		"/var/run/" + base + ".pid",
		"/tmp/run/" + base + ".pid",
	}
}

func hasDetachedHelperLauncher() bool {
	for _, name := range []string{"start-stop-daemon", "setsid", "nohup", "sh"} {
		if _, err := lookupPath(name); err == nil {
			return true
		}
	}
	return false
}

func serviceMainPID(name string) string {
	if detectServiceManager() == managerProcd {
		return procdServicePID(name)
	}
	unit := name
	if !strings.HasSuffix(unit, ".service") {
		unit += ".service"
	}
	output, err := exec.Command("systemctl", "show", unit, "--property=MainPID").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(output)), "MainPID="))
}

func procdServicePID(name string) string {
	for _, path := range procdPIDFilePaths(name) {
		content, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		pid := strings.TrimSpace(string(content))
		if pid != "" {
			return pid
		}
	}
	return ""
}

func serviceName() string {
	if name := explicitServiceName(); name != "" {
		return name
	}
	pid := strconv.Itoa(os.Getpid())
	for _, name := range []string{defaultService, "komari.service"} {
		if lookupServicePID(name) == pid {
			return name
		}
	}
	return defaultService
}

func localHealthURL() (string, error) {
	listen := firstEnv("LITE_LISTEN", "KOMARI_LISTEN")
	if listen == "" {
		listen = strings.TrimSpace(flags.Listen)
	}
	if listen == "" {
		listen = "0.0.0.0:27777"
	}
	for i := 1; i < len(os.Args); i++ {
		arg := os.Args[i]
		switch {
		case arg == "-l" || arg == "--listen":
			if i+1 < len(os.Args) {
				listen = os.Args[i+1]
			}
		case strings.HasPrefix(arg, "--listen="):
			listen = strings.TrimPrefix(arg, "--listen=")
		}
	}
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return "", fmt.Errorf("unsupported listen address %q: %w", listen, err)
	}
	switch host {
	case "", "0.0.0.0":
		host = "127.0.0.1"
	case "::", "[::]":
		host = "::1"
	}
	if !isAllowedHealthLoopback(host) {
		return "", fmt.Errorf("listen address %q cannot be reached through an approved loopback health check", listen)
	}
	return "http://" + net.JoinHostPort(host, port) + "/api/version", nil
}

func isMountPoint(path string) bool {
	content, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return false
	}
	clean := filepath.Clean(path)
	for _, line := range strings.Split(string(content), "\n") {
		fields := strings.Fields(line)
		if len(fields) > 4 && filepath.Clean(strings.ReplaceAll(fields[4], `\040`, " ")) == clean {
			return true
		}
	}
	return false
}

func ReadLastResult(executableDir string) (*UpdateResult, error) {
	var lastErr error
	for _, name := range []string{updateRootName, legacyUpdateRootName} {
		content, err := os.ReadFile(filepath.Join(executableDir, name, lastResultName))
		if err != nil {
			if os.IsNotExist(err) {
				lastErr = err
				continue
			}
			return nil, err
		}
		var result UpdateResult
		if err := json.Unmarshal(content, &result); err != nil {
			return nil, err
		}
		if time.Since(result.UpdatedAt) > 7*24*time.Hour {
			return nil, nil
		}
		return &result, nil
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, os.ErrNotExist
}
