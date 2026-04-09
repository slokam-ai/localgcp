package orchestrator

// Predefined emulator configurations.
var (
	SpannerConfig = ContainerConfig{
		Name:         "localgcp-spanner",
		Image:        "gcr.io/cloud-spanner-emulator/emulator:1.5.23",
		InternalPort: "9010/tcp",
	}

	BigtableConfig = ContainerConfig{
		Name:         "localgcp-bigtable",
		Image:        "google/cloud-sdk:emulators",
		InternalPort: "8086/tcp",
		Cmd:          []string{"gcloud", "beta", "emulators", "bigtable", "start", "--host-port=0.0.0.0:8086"},
	}

	CloudSQLConfig = ContainerConfig{
		Name:         "localgcp-cloudsql",
		Image:        "postgres:16-alpine",
		InternalPort: "5432/tcp",
		Env:          []string{"POSTGRES_HOST_AUTH_METHOD=trust", "POSTGRES_DB=localgcp"},
	}

	MemorystoreConfig = ContainerConfig{
		Name:         "localgcp-memorystore",
		Image:        "redis:7-alpine",
		InternalPort: "6379/tcp",
	}
)

// ServiceRegistry maps user-facing service names to their container configs.
var ServiceRegistry = map[string]ContainerConfig{
	"spanner":     SpannerConfig,
	"bigtable":    BigtableConfig,
	"cloudsql":    CloudSQLConfig,
	"memorystore": MemorystoreConfig,
}
