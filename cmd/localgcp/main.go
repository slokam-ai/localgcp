package main

import (
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/slokam-ai/localgcp/internal/auth"
	"github.com/slokam-ai/localgcp/internal/cloudrun"
	"github.com/slokam-ai/localgcp/internal/cloudtasks"
	"github.com/slokam-ai/localgcp/internal/firestore"
	"github.com/slokam-ai/localgcp/internal/gcs"
	"github.com/slokam-ai/localgcp/internal/kms"
	"github.com/slokam-ai/localgcp/internal/logging"
	"github.com/slokam-ai/localgcp/internal/orchestrator"
	"github.com/slokam-ai/localgcp/internal/pubsub"
	"github.com/slokam-ai/localgcp/internal/secretmanager"
	"github.com/slokam-ai/localgcp/internal/server"
	"github.com/slokam-ai/localgcp/internal/vertexai"
)

var version = "dev"

func main() {
	root := &cobra.Command{
		Use:     "localgcp",
		Short:   "The unified GCP emulator. One binary, thirteen services, zero cloud bills.",
		Version: version,
	}

	root.AddCommand(upCmd())
	root.AddCommand(envCmd())
	root.AddCommand(pullCmd())

	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

func upCmd() *cobra.Command {
	cfg := server.DefaultConfig()

	cmd := &cobra.Command{
		Use:   "up",
		Short: "Start all emulated GCP services",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Ensure dummy credentials exist.
			credPath, err := auth.EnsureCredentials(cfg.DataDir)
			if err != nil {
				return fmt.Errorf("credential setup failed: %w", err)
			}
			_ = credPath

			srv := server.New(cfg)
			srv.Register(gcs.New(cfg.DataDir, cfg.Quiet), cfg.PortGCS)
			srv.Register(pubsub.New(cfg.DataDir, cfg.Quiet), cfg.PortPubSub)
			srv.Register(secretmanager.New(cfg.DataDir, cfg.Quiet), cfg.PortSecretManager)
			srv.Register(firestore.New(cfg.DataDir, cfg.Quiet), cfg.PortFirestore)
			srv.Register(cloudtasks.New(cfg.DataDir, cfg.Quiet), cfg.PortCloudTasks)
			srv.Register(vertexai.New(cfg.DataDir, cfg.Quiet, cfg.OllamaHost, cfg.VertexModelMap, cfg.VertexBackend, cfg.VertexAPIKey), cfg.PortVertexAI)
			srv.Register(kms.New(cfg.DataDir, cfg.Quiet), cfg.PortKMS)
			srv.Register(logging.New(cfg.DataDir, cfg.Quiet), cfg.PortLogging)
			srv.Register(cloudrun.New(cfg.DataDir, cfg.Quiet), cfg.PortCloudRun)

			// Register orchestrated Docker services (opt-in via --services).
			if cfg.Services != "" && !cfg.NoDocker {
				runtime := orchestrator.NewDockerRuntime(log.New(os.Stderr, "[orchestrator] ", log.LstdFlags))
				if !runtime.Available() {
					fmt.Fprintln(os.Stderr, "Warning: Docker not available; skipping orchestrated services")
				} else {
					// Clean up orphaned containers from crashed sessions.
					runtime.CleanupOrphans(cmd.Context(), "localgcp-")

					requested := strings.Split(cfg.Services, ",")
					portMap := map[string]int{
						"spanner":     cfg.PortSpanner,
						"bigtable":    cfg.PortBigtable,
						"cloudsql":    cfg.PortCloudSQL,
						"memorystore": cfg.PortMemorystore,
					}
					for _, svcName := range requested {
						svcName = strings.TrimSpace(svcName)
						ecfg, ok := orchestrator.ServiceRegistry[svcName]
						if !ok {
							return fmt.Errorf("unknown orchestrated service: %s (available: spanner, bigtable, cloudsql, memorystore)", svcName)
						}
						port, ok := portMap[svcName]
						if !ok {
							return fmt.Errorf("no port configured for %s", svcName)
						}
						ecfg = orchestrator.WithDataDir(ecfg, cfg.DataDir)
						srv.Register(orchestrator.NewLazyService(ecfg.Name, ecfg, runtime), port)
					}
				}
			}

			return srv.Run()
		},
	}

	cmd.Flags().StringVar(&cfg.DataDir, "data-dir", "", "Directory for persistent storage (default: in-memory)")
	cmd.Flags().IntVar(&cfg.PortGCS, "port-gcs", cfg.PortGCS, "Port for Cloud Storage")
	cmd.Flags().IntVar(&cfg.PortPubSub, "port-pubsub", cfg.PortPubSub, "Port for Pub/Sub")
	cmd.Flags().IntVar(&cfg.PortSecretManager, "port-secretmanager", cfg.PortSecretManager, "Port for Secret Manager")
	cmd.Flags().IntVar(&cfg.PortFirestore, "port-firestore", cfg.PortFirestore, "Port for Firestore")
	cmd.Flags().IntVar(&cfg.PortCloudTasks, "port-cloudtasks", cfg.PortCloudTasks, "Port for Cloud Tasks")
	cmd.Flags().IntVar(&cfg.PortVertexAI, "port-vertexai", cfg.PortVertexAI, "Port for Vertex AI")
	cmd.Flags().IntVar(&cfg.PortKMS, "port-kms", cfg.PortKMS, "Port for Cloud KMS")
	cmd.Flags().IntVar(&cfg.PortLogging, "port-logging", cfg.PortLogging, "Port for Cloud Logging")
	cmd.Flags().IntVar(&cfg.PortCloudRun, "port-cloudrun", cfg.PortCloudRun, "Port for Cloud Run")
	cmd.Flags().StringVar(&cfg.OllamaHost, "ollama-host", cfg.OllamaHost, "Ollama API host for Vertex AI backend")
	cmd.Flags().StringVar(&cfg.VertexModelMap, "vertex-model-map", "", "Model alias mapping (e.g. gemini-2.5-flash=llama3.2)")
	cmd.Flags().StringVar(&cfg.VertexBackend, "vertex-backend", "", "Vertex AI backend: ollama (default), openai, anthropic, stub")
	cmd.Flags().StringVar(&cfg.VertexAPIKey, "vertex-api-key", "", "API key for OpenAI/Anthropic Vertex AI backends")
	cmd.Flags().StringVar(&cfg.Services, "services", "", "Docker-orchestrated services: spanner,bigtable,cloudsql,memorystore")
	cmd.Flags().BoolVar(&cfg.NoDocker, "no-docker", false, "Skip all Docker-orchestrated services")
	cmd.Flags().IntVar(&cfg.PortSpanner, "port-spanner", cfg.PortSpanner, "Port for Spanner emulator")
	cmd.Flags().IntVar(&cfg.PortBigtable, "port-bigtable", cfg.PortBigtable, "Port for Bigtable emulator")
	cmd.Flags().IntVar(&cfg.PortCloudSQL, "port-cloudsql", cfg.PortCloudSQL, "Port for Cloud SQL (Postgres)")
	cmd.Flags().IntVar(&cfg.PortMemorystore, "port-memorystore", cfg.PortMemorystore, "Port for Memorystore (Redis)")
	cmd.Flags().BoolVarP(&cfg.Quiet, "quiet", "q", false, "Suppress request logging (for CI)")

	return cmd
}

func envCmd() *cobra.Command {
	var portGCS, portPubSub, portFirestore, portSecretManager, portCloudTasks, portVertexAI, portKMS, portLogging, portCloudRun int

	cmd := &cobra.Command{
		Use:   "env",
		Short: "Print export statements to configure GCP client libraries",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Printf("export STORAGE_EMULATOR_HOST=localhost:%d\n", portGCS)
			fmt.Printf("export PUBSUB_EMULATOR_HOST=localhost:%d\n", portPubSub)
			fmt.Printf("export FIRESTORE_EMULATOR_HOST=localhost:%d\n", portFirestore)

			// Check if existing GOOGLE_APPLICATION_CREDENTIALS is set.
			if existing := os.Getenv("GOOGLE_APPLICATION_CREDENTIALS"); existing != "" {
				fmt.Fprintln(os.Stderr, "# Note: your existing GOOGLE_APPLICATION_CREDENTIALS is preserved.")
				fmt.Fprintln(os.Stderr, "# localgcp only sets *_EMULATOR_HOST vars.")
			}

			fmt.Println()
			fmt.Println("# Secret Manager and Cloud Tasks have no _EMULATOR_HOST env var.")
			fmt.Println("# Configure your client manually. Example (Go):")
			fmt.Println("#")
			fmt.Println("# Secret Manager:")
			fmt.Println("#   client, _ := secretmanager.NewClient(ctx,")
			fmt.Printf("#     option.WithEndpoint(\"localhost:%d\"),\n", portSecretManager)
			fmt.Println("#     option.WithoutAuthentication(),")
			fmt.Println("#     option.WithGRPCDialOption(grpc.WithTransportCredentials(insecure.NewCredentials())),")
			fmt.Println("#   )")
			fmt.Println("#")
			fmt.Println("# Cloud Tasks:")
			fmt.Println("#   client, _ := cloudtasks.NewClient(ctx,")
			fmt.Printf("#     option.WithEndpoint(\"localhost:%d\"),\n", portCloudTasks)
			fmt.Println("#     option.WithoutAuthentication(),")
			fmt.Println("#     option.WithGRPCDialOption(grpc.WithTransportCredentials(insecure.NewCredentials())),")
			fmt.Println("#   )")
			fmt.Println("#")
			fmt.Println("# Vertex AI (google.golang.org/genai):")
			fmt.Println("#   client, _ := genai.NewClient(ctx, &genai.ClientConfig{")
			fmt.Println("#     Project: \"my-project\", Location: \"us-central1\",")
			fmt.Println("#     Backend: genai.BackendVertexAI,")
			fmt.Printf("#     HTTPOptions: genai.HTTPOptions{BaseURL: \"http://localhost:%d\"},\n", portVertexAI)
			fmt.Println("#   })")
			fmt.Println("#")
			fmt.Println("# Cloud KMS:")
			fmt.Println("#   client, _ := kms.NewKeyManagementClient(ctx,")
			fmt.Printf("#     option.WithEndpoint(\"localhost:%d\"),\n", portKMS)
			fmt.Println("#     option.WithoutAuthentication(),")
			fmt.Println("#     option.WithGRPCDialOption(grpc.WithTransportCredentials(insecure.NewCredentials())),")
			fmt.Println("#   )")
			fmt.Println("#")
			fmt.Println("# Cloud Logging:")
			fmt.Println("#   client, _ := logadmin.NewClient(ctx, \"my-project\",")
			fmt.Printf("#     option.WithEndpoint(\"localhost:%d\"),\n", portLogging)
			fmt.Println("#     option.WithoutAuthentication(),")
			fmt.Println("#     option.WithGRPCDialOption(grpc.WithTransportCredentials(insecure.NewCredentials())),")
			fmt.Println("#   )")
			fmt.Println("#")
			fmt.Println("# Cloud Run:")
			fmt.Println("#   client, _ := run.NewServicesClient(ctx,")
			fmt.Printf("#     option.WithEndpoint(\"localhost:%d\"),\n", portCloudRun)
			fmt.Println("#     option.WithoutAuthentication(),")
			fmt.Println("#     option.WithGRPCDialOption(grpc.WithTransportCredentials(insecure.NewCredentials())),")
			fmt.Println("#   )")

			return nil
		},
	}

	cfg := server.DefaultConfig()
	cmd.Flags().IntVar(&portGCS, "port-gcs", cfg.PortGCS, "Port for Cloud Storage")
	cmd.Flags().IntVar(&portPubSub, "port-pubsub", cfg.PortPubSub, "Port for Pub/Sub")
	cmd.Flags().IntVar(&portFirestore, "port-firestore", cfg.PortFirestore, "Port for Firestore")
	cmd.Flags().IntVar(&portSecretManager, "port-secretmanager", cfg.PortSecretManager, "Port for Secret Manager")
	cmd.Flags().IntVar(&portCloudTasks, "port-cloudtasks", cfg.PortCloudTasks, "Port for Cloud Tasks")
	cmd.Flags().IntVar(&portVertexAI, "port-vertexai", cfg.PortVertexAI, "Port for Vertex AI")
	cmd.Flags().IntVar(&portKMS, "port-kms", cfg.PortKMS, "Port for Cloud KMS")
	cmd.Flags().IntVar(&portLogging, "port-logging", cfg.PortLogging, "Port for Cloud Logging")
	cmd.Flags().IntVar(&portCloudRun, "port-cloudrun", cfg.PortCloudRun, "Port for Cloud Run")

	return cmd
}

func pullCmd() *cobra.Command {
	var services string

	cmd := &cobra.Command{
		Use:   "pull",
		Short: "Pre-fetch Docker images for orchestrated services",
		RunE: func(cmd *cobra.Command, args []string) error {
			runtime := orchestrator.NewDockerRuntime(log.New(os.Stderr, "[pull] ", log.LstdFlags))
			if !runtime.Available() {
				return fmt.Errorf("Docker is not available. Install Docker Desktop, OrbStack, or Colima to use orchestrated services")
			}

			var targets []string
			if services == "" {
				// Pull all by default.
				for name := range orchestrator.ServiceRegistry {
					targets = append(targets, name)
				}
			} else {
				targets = strings.Split(services, ",")
			}

			for _, name := range targets {
				name = strings.TrimSpace(name)
				cfg, ok := orchestrator.ServiceRegistry[name]
				if !ok {
					fmt.Fprintf(os.Stderr, "Warning: unknown service %q, skipping\n", name)
					continue
				}
				if err := runtime.Pull(cmd.Context(), cfg.Image); err != nil {
					fmt.Fprintf(os.Stderr, "Error pulling %s: %v\n", name, err)
				}
			}
			fmt.Println("All images pulled. First-request latency is now ~3s (container start only).")
			return nil
		},
	}

	cmd.Flags().StringVar(&services, "services", "", "Services to pull (default: all). Example: spanner,bigtable")
	return cmd
}
