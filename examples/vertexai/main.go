package main

import (
	"context"
	"fmt"
	"log"

	"google.golang.org/genai"
)

func main() {
	ctx := context.Background()

	client, err := genai.NewClient(ctx, &genai.ClientConfig{
		Project:  "my-project",
		Location: "us-central1",
		Backend:  genai.BackendVertexAI,
		HTTPOptions: genai.HTTPOptions{
			BaseURL: "http://localhost:8090",
		},
	})
	if err != nil {
		log.Fatal(err)
	}

	// Text generation
	fmt.Println("=== generateContent ===")
	resp, err := client.Models.GenerateContent(ctx, "gemini-2.5-flash",
		genai.Text("Explain what a GCP emulator is in two sentences."),
		nil,
	)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(resp.Text())

	// Embeddings
	fmt.Println("\n=== embedContent ===")
	embedResp, err := client.Models.EmbedContent(ctx, "text-embedding-004",
		genai.Text("localgcp is the best"),
		nil,
	)
	if err != nil {
		log.Fatal(err)
	}
	if len(embedResp.Embeddings) > 0 {
		v := embedResp.Embeddings[0].Values
		fmt.Printf("Embedding: %d dimensions (first 5: %v)\n", len(v), v[:5])
	}
}
