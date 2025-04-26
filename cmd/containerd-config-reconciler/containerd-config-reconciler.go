package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/BurntSushi/toml"
	"stackdome.io/cluster-agent/internal/types"
)

// Configuration constants
const (
	defaultConfigVersion = "2"
)

type Registry = types.Registry
type RegistryConfig = types.RegistryConfig

// Command line flags
var (
	configDir       = flag.String("config-dir", "/etc/containerd", "Directory containing containerd config")
	configFile      = flag.String("config-file", "config.toml", "Containerd config filename")
	registryConfig  = flag.String("registry-config", "/config/registries", "Path to registry configuration")
	monitorInterval = flag.Int("interval", 30, "Monitoring interval in seconds")
)

// ContainerdConfigReconciler manages containerd configuration updates
type ContainerdConfigReconciler struct {
	mu            sync.RWMutex
	ConfigPath    string
	RegistryPath  string
	Interval      time.Duration
	Logger        *log.Logger
	IsLastSuccess bool
}

// NewContainerdConfigReconciler creates a new reconciler instance
func NewContainerdConfigReconciler(configDir, configFile, registryPath string, intervalSeconds int) *ContainerdConfigReconciler {
	return &ContainerdConfigReconciler{
		ConfigPath:   filepath.Join(configDir, configFile),
		RegistryPath: registryPath,
		Interval:     time.Duration(intervalSeconds) * time.Second,
		Logger:       log.New(os.Stdout, "containerd-config: ", log.LstdFlags),
	}
}

// Start begins the reconciliation process and health server
func (c *ContainerdConfigReconciler) Start(ctx context.Context) {
	// Initial reconciliation
	if err := c.Reconcile(); err != nil {
		c.Logger.Printf("Error in initial configuration: %v", err)
		c.setHealthStatus(false)
	} else {
		c.setHealthStatus(true)
	}

	// Start health server
	go c.StartHealthServer(ctx)

	// Begin monitoring
	ticker := time.NewTicker(c.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			c.Logger.Printf("Context done, stopping monitoring")
			return
		case <-ticker.C:
			c.Logger.Printf("Checking for registry config changes")
			if err := c.Reconcile(); err != nil {
				c.setHealthStatus(false)
				c.Logger.Printf("Error updating configuration: %v", err)
			} else {
				c.setHealthStatus(true)
			}
		}
	}
}

// StartHealthServer starts the HTTP server for health checks
func (c *ContainerdConfigReconciler) StartHealthServer(ctx context.Context) {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", c.healthHandler)

	server := &http.Server{
		Addr:    ":8080",
		Handler: mux,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			c.Logger.Printf("Error shutting down health server: %v", err)
		}
	}()

	if err := server.ListenAndServe(); err != http.ErrServerClosed {
		c.Logger.Printf("Error starting health server: %v", err)
	}
}

// healthHandler handles health check requests
func (c *ContainerdConfigReconciler) healthHandler(w http.ResponseWriter, r *http.Request) {
	c.mu.RLock()
	isHealthy := c.IsLastSuccess
	c.mu.RUnlock()

	if isHealthy {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "Containerd config written successfully")
	} else {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintln(w, "Containerd config write failed")
	}
}

// setHealthStatus safely updates the health status
func (c *ContainerdConfigReconciler) setHealthStatus(isSuccess bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.IsLastSuccess = isSuccess
}

// Reconcile performs the full reconciliation process
func (c *ContainerdConfigReconciler) Reconcile() error {
	// Read registry configurations
	registryConfig, err := c.readRegistryConfig()
	if err != nil {
		return fmt.Errorf("failed to read registry config: %w", err)
	}

	registries := registryConfig.ValidRegistries()
	// Skip if no registries defined
	if len(registries) == 0 {
		c.Logger.Printf("No registries defined, skipping update")
		return nil
	}

	// Update containerd config
	needsRestart, err := c.updateContainerdConfig(registries)
	if err != nil {
		return fmt.Errorf("failed to update containerd config: %w", err)
	}

	c.Logger.Printf("Updated containerd configuration successfully")

	// Restart containerd if needed
	if needsRestart {
		c.Logger.Printf("restarting containerd as config file changed")
		return c.reloadContainerd()
	}
	return nil
}

// readRegistryConfig reads and parses the registry configuration file
func (c *ContainerdConfigReconciler) readRegistryConfig() (*types.RegistryConfig, error) {
	// Check if file exists
	if _, err := os.Stat(c.RegistryPath); os.IsNotExist(err) {
		return nil, fmt.Errorf("registry config file does not exist: %s", c.RegistryPath)
	}

	// Read and parse configuration
	data, err := os.ReadFile(c.RegistryPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read registry config: %w", err)
	}

	var config RegistryConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse registry config: %w", err)
	}

	return &config, nil
}

// updateContainerdConfig updates the containerd configuration
func (c *ContainerdConfigReconciler) updateContainerdConfig(registries []Registry) (bool, error) {
	c.Logger.Printf("Updating containerd config at %s", c.ConfigPath)

	// Create bootstrap config if file doesn't exist
	if _, err := os.Stat(c.ConfigPath); os.IsNotExist(err) {
		c.Logger.Printf("Config file not found, creating bootstrap config")
		return c.createBootstrapConfig(registries)
	}

	// Read existing config
	data, err := os.ReadFile(c.ConfigPath)
	if err != nil {
		return false, fmt.Errorf("error reading containerd config: %w", err)
	}

	// Parse TOML
	var config map[string]interface{}
	configMeta, err := toml.Decode(string(data), &config)
	if err != nil {
		return false, fmt.Errorf("error parsing containerd config: %w", err)
	}

	// Handle version
	configVersion := defaultConfigVersion
	containerdConfigFileUpdated := false

	if configMeta.IsDefined("version") {
		if versionStr, ok := config["version"].(string); ok {
			configVersion = versionStr
		}
	} else {
		config["version"] = defaultConfigVersion
		containerdConfigFileUpdated = true
	}

	// Get the appropriate config section based on version
	imagesConfig, err := c.getImagesConfig(config, configVersion)
	if err != nil {
		return false, err
	}

	// Ensure registry config exists
	registryConfig, modified := c.ensureMapExists(imagesConfig, "registry")
	if modified {
		containerdConfigFileUpdated = true
	}

	// Set hosts config directory if not present
	hostsConfigDir := filepath.Join(*configDir, "certs.d")
	configPathKey, exists := registryConfig["config_path"]
	if !exists {
		registryConfig["config_path"] = hostsConfigDir
		containerdConfigFileUpdated = true
	} else if pathStr, ok := configPathKey.(string); ok {
		hostsConfigDir = pathStr
	} else {
		return false, fmt.Errorf("invalid config_path type in containerd config")
	}

	// Update hosts config
	if err := c.reconcileHostsConfig(hostsConfigDir, registries); err != nil {
		return false, err
	}

	if containerdConfigFileUpdated {
		c.Logger.Println("updating containerd config file")
		// Backup original config
		backupPath := c.ConfigPath + ".bak"
		if err := os.WriteFile(backupPath, data, 0644); err != nil {
			c.Logger.Printf("Warning: Failed to backup config: %v", err)
		}

		// Write updated config
		if err := c.writeConfigTOML(c.ConfigPath, config); err != nil {
			return false, fmt.Errorf("error writing updated config: %w", err)
		}
		return true, nil
	}

	c.Logger.Println("containerd config file already in expected state")
	return false, nil
}

// createBootstrapConfig creates a new containerd config with registry settings
func (c *ContainerdConfigReconciler) createBootstrapConfig(registries []Registry) (bool, error) {
	hostConfigDir := filepath.Join(*configDir, "certs.d")

	bootstrapConfig := map[string]interface{}{
		"version": defaultConfigVersion,
		"plugins": map[string]interface{}{
			"io.containerd.grpc.v1.cri": map[string]interface{}{
				"registry": map[string]interface{}{
					"config_path": hostConfigDir,
				},
			},
		},
	}

	if err := c.reconcileHostsConfig(hostConfigDir, registries); err != nil {
		return false, err
	}

	return true, c.writeConfigTOML(c.ConfigPath, bootstrapConfig)
}

// getImagesConfig extracts the images config section based on containerd version
func (c *ContainerdConfigReconciler) getImagesConfig(config map[string]interface{}, version string) (map[string]interface{}, error) {
	plugins, _ := c.ensureMapExists(config, "plugins")

	var targetPlugin string
	switch version {
	case "2":
		targetPlugin = "io.containerd.grpc.v1.cri"
	case "3":
		targetPlugin = "io.containerd.cri.v1.images"
	default:
		return nil, fmt.Errorf("unsupported containerd config version: %s", version)
	}

	imagesConfig, _ := c.ensureMapExists(plugins, targetPlugin)
	return imagesConfig, nil
}

// ensureMapExists ensures a nested map exists, creating it if needed
func (c *ContainerdConfigReconciler) ensureMapExists(parent map[string]interface{}, key string) (map[string]interface{}, bool) {
	modified := false
	value, exists := parent[key]
	if !exists {
		parent[key] = make(map[string]interface{})
		modified = true
		return parent[key].(map[string]interface{}), modified
	}

	if mapValue, ok := value.(map[string]interface{}); ok {
		return mapValue, modified
	}

	// Type is wrong, replace with empty map
	parent[key] = make(map[string]interface{})
	return parent[key].(map[string]interface{}), true
}

// updateHostsConfig updates the containerd hosts configuration
func (c *ContainerdConfigReconciler) reconcileHostsConfig(hostsConfigDir string, registries []Registry) error {
	for _, registry := range registries {
		u, err := url.Parse(registry.Endpoint)
		if err != nil {
			return err
		}
		currentHostDir := filepath.Join(hostsConfigDir, u.Hostname())
		if err := os.MkdirAll(currentHostDir, 0755); err != nil {
			return fmt.Errorf("failed to create hosts config directory: %w", err)
		}
		currentHostsFilePath := filepath.Join(currentHostDir, "hosts.toml")

		if err := c.createHostsConfig(currentHostsFilePath, registry); err != nil {
			return err
		}
	}
	return nil
}

// createHostsConfig creates a new hosts config file
func (c *ContainerdConfigReconciler) createHostsConfig(path string, registry Registry) error {
	hostsConfig := make(map[string]interface{})

	hostsConfig[fmt.Sprintf("http://%s", registry.ServiceIp)] = map[string]interface{}{
		"capabilities": []string{"pull", "resolve"},
		"skip_verify":  true,
	}

	config := map[string]interface{}{
		"server": registry.Endpoint,
		"host":   hostsConfig,
	}

	return c.writeConfigTOML(path, config)
}

// updateExistingHostsConfig updates an existing hosts config file
func (c *ContainerdConfigReconciler) updateExistingHostsConfig(path string, registries []Registry) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("failed to read hosts config: %w", err)
	}

	var config map[string]interface{}
	meta, err := toml.Decode(string(data), &config)
	if err != nil {
		return fmt.Errorf("failed to parse hosts config: %w", err)
	}

	// Check if updates are needed
	updatesNeeded := false
	for _, registry := range registries {
		if !meta.IsDefined("host", registry.Endpoint) {
			updatesNeeded = true
			break
		}
	}

	if !updatesNeeded {
		return nil
	}

	// Update hosts config
	hostsConfig, _ := c.ensureMapExists(config, "host")

	for _, registry := range registries {
		hostsConfig[registry.Endpoint] = map[string]interface{}{
			"capabilities": []string{"pull", "resolve"},
		}
	}

	return c.writeConfigTOML(path, config)
}

// writeConfigTOML writes config to TOML file atomically
func (c *ContainerdConfigReconciler) writeConfigTOML(path string, config map[string]interface{}) error {
	// Create temporary file
	tmpPath := path + ".tmp"
	f, err := os.Create(tmpPath)
	if err != nil {
		return fmt.Errorf("failed to create temporary file: %w", err)
	}

	// Write config
	encoder := toml.NewEncoder(f)
	if err := encoder.Encode(config); err != nil {
		f.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("failed to encode TOML: %w", err)
	}

	// Close file before renaming
	if err := f.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("failed to close temporary file: %w", err)
	}

	// Atomically replace old file
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("failed to rename temporary file: %w", err)
	}

	// Set permissions
	if err := os.Chmod(path, 0644); err != nil {
		return fmt.Errorf("failed to set permissions: %w", err)
	}

	return nil
}

func (c *ContainerdConfigReconciler) reloadContainerd() error {
	pidCmd := exec.Command("pidof", "containerd")
	pidOutput, err := pidCmd.Output()
	if err != nil {
		c.Logger.Printf("Failed to find containerd process: %v", err)
		// Don't return an error if containerd isn't running
		return nil
	}

	pidStr := strings.TrimSpace(string(pidOutput))
	c.Logger.Printf("Found containerd PID(s): %s", pidStr)

	for _, pid := range strings.Fields(pidStr) {
		c.Logger.Printf("Sending SIGHUP to containerd process %s", pid)
		killCmd := exec.Command("kill", "-HUP", pid)
		if output, err := killCmd.CombinedOutput(); err != nil {
			c.Logger.Printf("Failed to send SIGHUP to PID %s: %v, output: %s", pid, err, output)
			// Continue with other PIDs, don't fail immediately
		} else {
			c.Logger.Printf("Successfully sent SIGHUP to PID %s", pid)
		}
	}

	return nil
}

func main() {
	flag.Parse()

	log.Printf("Registry config updater starting")
	log.Printf("Monitoring %s for changes", *registryConfig)

	reconciler := NewContainerdConfigReconciler(*configDir, *configFile, *registryConfig, *monitorInterval)

	// Create context with cancellation for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Set up signal handling
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		sig := <-sigChan
		reconciler.Logger.Printf("Received signal: %s, shutting down", sig)
		cancel()
	}()

	// Start reconciliation process
	reconciler.Start(ctx)
}
