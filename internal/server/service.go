package server

import "context"

// Service is the interface that each emulated GCP service implements.
type Service interface {
	// Name returns the display name (e.g., "Cloud Storage", "Pub/Sub").
	Name() string

	// Start begins serving on the given address. It blocks until ctx is cancelled
	// or an error occurs. The server must handle graceful shutdown when ctx is done.
	Start(ctx context.Context, addr string) error
}
