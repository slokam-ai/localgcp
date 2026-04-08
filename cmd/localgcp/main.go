package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/slokam-ai/localgcp/internal/auth"
	"github.com/slokam-ai/localgcp/internal/firestore"
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

			return srv.Run()
		},
	}

	cmd.Flags().StringVar(&cfg.DataDir, "data-dir", "", "Directory for persistent storage (default: in-memory)")
	cmd.Flags().IntVar(&cfg.PortGCS, "port-gcs", cfg.PortGCS, "Port for Cloud Storage")
	cmd.Flags().IntVar(&cfg.PortPubSub, "port-pubsub", cfg.PortPubSub, "Port for Pub/Sub")
	cmd.Flags().IntVar(&cfg.PortSecretManager, "port-secretmanager", cfg.PortSecretManager, "Port for Secret Manager")
	cmd.Flags().IntVar(&cfg.PortFirestore, "port-firestore", cfg.PortFirestore, "Port for Firestore")
	cmd.Flags().BoolVarP(&cfg.Quiet, "quiet", "q", false, "Suppress request logging (for CI)")

	return cmd
}

func envCmd() *cobra.Command {
	var portGCS, portPubSub, portFirestore, portSecretManager int

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
			fmt.Println("# Secret Manager has no _EMULATOR_HOST env var.")
			fmt.Println("# Configure your client manually. Example (Go):")
			fmt.Println("#   client, _ := secretmanager.NewClient(ctx,")
			fmt.Printf("#     option.WithEndpoint(\"localhost:%d\"),\n", portSecretManager)
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

	return cmd
}
