package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/slokam-ai/localgcp/internal/auth"
	"github.com/slokam-ai/localgcp/internal/cloudtasks"
	"github.com/slokam-ai/localgcp/internal/firestore"
	"github.com/slokam-ai/localgcp/internal/vertexai"
	"github.com/slokam-ai/localgcp/internal/gcs"
	"github.com/slokam-ai/localgcp/internal/pubsub"
	"github.com/slokam-ai/localgcp/internal/secretmanager"
	"github.com/slokam-ai/localgcp/internal/server"
)

var version = "dev"

func main() {
	root := &cobra.Command{
		Use:     "localgcp",
		Short:   "The first unified GCP emulator. One binary, four services, zero cloud bills.",
		Version: version,
	}

	root.AddCommand(upCmd())
	root.AddCommand(envCmd())

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
			srv.Register(vertexai.New(cfg.DataDir, cfg.Quiet, cfg.OllamaHost, cfg.VertexModelMap), cfg.PortVertexAI)

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
	cmd.Flags().StringVar(&cfg.OllamaHost, "ollama-host", cfg.OllamaHost, "Ollama API host for Vertex AI backend")
	cmd.Flags().StringVar(&cfg.VertexModelMap, "vertex-model-map", "", "Model alias mapping (e.g. gemini-2.5-flash=llama3.2)")
	cmd.Flags().BoolVarP(&cfg.Quiet, "quiet", "q", false, "Suppress request logging (for CI)")

	return cmd
}

func envCmd() *cobra.Command {
	var portGCS, portPubSub, portFirestore, portSecretManager, portCloudTasks, portVertexAI int

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

	return cmd
}
