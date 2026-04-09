package orchestrator

import (
	"os"
	"path/filepath"
)

// Predefined emulator configurations.
var (
	SpannerConfig = ContainerConfig{
		Name:         "localgcp-spanner",
		Image:        "gcr.io/cloud-spanner-emulator/emulator:1.5.23",
		InternalPort: "9010/tcp",
		DataPath:     "", // Spanner emulator doesn't support persistence
	}

	BigtableConfig = ContainerConfig{
		Name:         "localgcp-bigtable",
		Image:        "google/cloud-sdk:emulators",
		InternalPort: "8086/tcp",
		Cmd:          []string{"gcloud", "beta", "emulators", "bigtable", "start", "--host-port=0.0.0.0:8086"},
		DataPath:     "", // Bigtable emulator doesn't support persistence
	}

	CloudSQLConfig = ContainerConfig{
		Name:         "localgcp-cloudsql",
		Image:        "postgres:16-alpine",
		InternalPort: "5432/tcp",
		Env:          []string{"POSTGRES_HOST_AUTH_METHOD=trust", "POSTGRES_DB=localgcp"},
		DataPath:     "/var/lib/postgresql/data", // Postgres data directory
	}

	MemorystoreConfig = ContainerConfig{
		Name:         "localgcp-memorystore",
		Image:        "redis:7-alpine",
		InternalPort: "6379/tcp",
		DataPath:     "/data", // Redis data directory
		Cmd:          nil,     // default cmd; overridden with appendonly when persisting
	}
)

// ServiceRegistry maps user-facing service names to their container configs.
var ServiceRegistry = map[string]ContainerConfig{
	"spanner":     SpannerConfig,
	"bigtable":    BigtableConfig,
	"cloudsql":    CloudSQLConfig,
	"memorystore": MemorystoreConfig,
}

// WithDataDir returns a copy of the config with volume mounts set for persistence.
// If dataDir is empty or the service doesn't support persistence, returns the config unchanged.
func WithDataDir(cfg ContainerConfig, dataDir string) ContainerConfig {
	if dataDir == "" || cfg.DataPath == "" {
		return cfg
	}

	// Create the host directory for this service.
	hostPath := filepath.Join(dataDir, cfg.Name)
	os.MkdirAll(hostPath, 0o755)

	cfg.Volumes = map[string]string{
		hostPath: cfg.DataPath,
	}

	// Redis needs appendonly mode for persistence.
	if cfg.Name == "localgcp-memorystore" {
		cfg.Cmd = []string{"redis-server", "--appendonly", "yes", "--dir", "/data"}
	}

	return cfg
}
