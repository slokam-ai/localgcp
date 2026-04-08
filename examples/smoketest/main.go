// Smoke test for localgcp — exercises all 4 services using official GCP client libraries.
//
// Usage:
//   Terminal 1:  ./localgcp up
//   Terminal 2:  go run ./examples/smoketest/
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"cloud.google.com/go/firestore"
	"cloud.google.com/go/pubsub"
	"cloud.google.com/go/storage"
	secretmanager "cloud.google.com/go/secretmanager/apiv1"
	"cloud.google.com/go/secretmanager/apiv1/secretmanagerpb"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const (
	project  = "localgcp-test"
	gcsPort  = "localhost:4443"
	psPort   = "localhost:8085"
	smPort   = "localhost:8086"
	fsPort   = "localhost:8088"
)

var (
	passed int
	failed int
)

func main() {
	fmt.Println("===========================================")
	fmt.Println("  localgcp smoke test")
	fmt.Println("  Testing all 4 services with real GCP SDKs")
	fmt.Println("===========================================")
	fmt.Println()

	// Set emulator env vars.
	os.Setenv("STORAGE_EMULATOR_HOST", gcsPort)
	os.Setenv("PUBSUB_EMULATOR_HOST", psPort)
	os.Setenv("FIRESTORE_EMULATOR_HOST", fsPort)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	testGCS(ctx)
	testPubSub(ctx)
	testSecretManager(ctx)
	testFirestore(ctx)

	fmt.Println()
	fmt.Println("===========================================")
	fmt.Printf("  Results: %d passed, %d failed\n", passed, failed)
	fmt.Println("===========================================")

	if failed > 0 {
		os.Exit(1)
	}
}

func check(name string, err error) bool {
	if err != nil {
		fmt.Printf("  FAIL  %s: %v\n", name, err)
		failed++
		return false
	}
	fmt.Printf("  PASS  %s\n", name)
	passed++
	return true
}

// ──────────────────────────────────────────────
// Cloud Storage
// ──────────────────────────────────────────────

func testGCS(ctx context.Context) {
	fmt.Println("[Cloud Storage]")

	client, err := storage.NewClient(ctx)
	if !check("Connect", err) {
		return
	}
	defer client.Close()

	bucket := client.Bucket("smoke-test-bucket")

	// Create bucket.
	err = bucket.Create(ctx, project, nil)
	check("Create bucket", err)

	// Upload object.
	w := bucket.Object("hello.txt").NewWriter(ctx)
	w.ContentType = "text/plain"
	_, err = w.Write([]byte("Hello from localgcp!"))
	if err == nil {
		err = w.Close()
	}
	check("Upload object", err)

	// Download object.
	r, err := bucket.Object("hello.txt").NewReader(ctx)
	if check("Download object (open)", err) {
		data, err := io.ReadAll(r)
		r.Close()
		if check("Download object (read)", err) {
			if string(data) == "Hello from localgcp!" {
				check("Download object (content match)", nil)
			} else {
				check("Download object (content match)",
					fmt.Errorf("got %q, want %q", string(data), "Hello from localgcp!"))
			}
		}
	}

	// List objects.
	it := bucket.Objects(ctx, nil)
	count := 0
	for {
		_, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			check("List objects", err)
			break
		}
		count++
	}
	if count == 1 {
		check("List objects (count=1)", nil)
	} else {
		check("List objects (count=1)", fmt.Errorf("got %d objects", count))
	}

	// Delete object.
	err = bucket.Object("hello.txt").Delete(ctx)
	check("Delete object", err)

	// Delete bucket.
	err = bucket.Delete(ctx)
	check("Delete bucket", err)

	fmt.Println()
}

// ──────────────────────────────────────────────
// Pub/Sub
// ──────────────────────────────────────────────

func testPubSub(ctx context.Context) {
	fmt.Println("[Pub/Sub]")

	client, err := pubsub.NewClient(ctx, project)
	if !check("Connect", err) {
		return
	}
	defer client.Close()

	// Create topic.
	topic, err := client.CreateTopic(ctx, "smoke-topic")
	check("Create topic", err)
	if topic == nil {
		return
	}

	// Create subscription.
	sub, err := client.CreateSubscription(ctx, "smoke-sub", pubsub.SubscriptionConfig{
		Topic:       topic,
		AckDeadline: 10 * time.Second,
	})
	check("Create subscription", err)
	if sub == nil {
		return
	}

	// Publish message.
	result := topic.Publish(ctx, &pubsub.Message{
		Data:       []byte("test message"),
		Attributes: map[string]string{"key": "value"},
	})
	msgID, err := result.Get(ctx)
	if check("Publish message", err) {
		if msgID != "" {
			check("Publish message (got ID)", nil)
		} else {
			check("Publish message (got ID)", fmt.Errorf("empty message ID"))
		}
	}

	// Pull message.
	pullCtx, pullCancel := context.WithTimeout(ctx, 5*time.Second)
	defer pullCancel()

	var received string
	var receivedAttr string
	err = sub.Receive(pullCtx, func(_ context.Context, m *pubsub.Message) {
		received = string(m.Data)
		receivedAttr = m.Attributes["key"]
		m.Ack()
		pullCancel()
	})
	// Receive returns context.Canceled when we cancel — that's expected.
	if err != nil && !strings.Contains(err.Error(), "canceled") {
		check("Pull message", err)
	} else if received == "test message" && receivedAttr == "value" {
		check("Pull message (content + attributes)", nil)
	} else {
		check("Pull message (content + attributes)",
			fmt.Errorf("got data=%q attr=%q", received, receivedAttr))
	}

	// Cleanup.
	sub.Delete(ctx)
	topic.Delete(ctx)

	fmt.Println()
}

// ──────────────────────────────────────────────
// Secret Manager
// ──────────────────────────────────────────────

func testSecretManager(ctx context.Context) {
	fmt.Println("[Secret Manager]")

	// Secret Manager has no _EMULATOR_HOST env var — connect manually.
	client, err := secretmanager.NewClient(ctx,
		option.WithEndpoint(smPort),
		option.WithoutAuthentication(),
		option.WithGRPCDialOption(grpc.WithTransportCredentials(insecure.NewCredentials())),
	)
	if !check("Connect", err) {
		return
	}
	defer client.Close()

	parent := fmt.Sprintf("projects/%s", project)
	secretName := parent + "/secrets/smoke-secret"

	// Create secret.
	_, err = client.CreateSecret(ctx, &secretmanagerpb.CreateSecretRequest{
		Parent:   parent,
		SecretId: "smoke-secret",
		Secret:   &secretmanagerpb.Secret{},
	})
	check("Create secret", err)

	// Add version.
	version, err := client.AddSecretVersion(ctx, &secretmanagerpb.AddSecretVersionRequest{
		Parent: secretName,
		Payload: &secretmanagerpb.SecretPayload{
			Data: []byte("super-secret-value-42"),
		},
	})
	check("Add version", err)

	// Access version.
	if version != nil {
		resp, err := client.AccessSecretVersion(ctx, &secretmanagerpb.AccessSecretVersionRequest{
			Name: version.Name,
		})
		if check("Access version", err) {
			payload := string(resp.Payload.Data)
			if payload == "super-secret-value-42" {
				check("Access version (content match)", nil)
			} else {
				check("Access version (content match)",
					fmt.Errorf("got %q", payload))
			}
		}
	}

	// Access latest.
	resp, err := client.AccessSecretVersion(ctx, &secretmanagerpb.AccessSecretVersionRequest{
		Name: secretName + "/versions/latest",
	})
	if check("Access latest version", err) {
		if string(resp.Payload.Data) == "super-secret-value-42" {
			check("Access latest (content match)", nil)
		}
	}

	// Delete secret.
	err = client.DeleteSecret(ctx, &secretmanagerpb.DeleteSecretRequest{
		Name: secretName,
	})
	check("Delete secret", err)

	fmt.Println()
}

// ──────────────────────────────────────────────
// Firestore
// ──────────────────────────────────────────────

func testFirestore(ctx context.Context) {
	fmt.Println("[Firestore]")

	client, err := firestore.NewClient(ctx, project)
	if !check("Connect", err) {
		return
	}
	defer client.Close()

	col := client.Collection("smoke-test")

	// Create document.
	doc, _, err := col.Add(ctx, map[string]interface{}{
		"name":  "Alice",
		"age":   30,
		"city":  "Seattle",
	})
	check("Create document", err)

	// Read document.
	if doc != nil {
		snap, err := doc.Get(ctx)
		if check("Get document", err) {
			data := snap.Data()
			if data["name"] == "Alice" {
				check("Get document (field match)", nil)
			} else {
				check("Get document (field match)",
					fmt.Errorf("got name=%v", data["name"]))
			}
		}
	}

	// Create more docs for querying.
	col.Add(ctx, map[string]interface{}{"name": "Bob", "age": 25, "city": "Portland"})
	col.Add(ctx, map[string]interface{}{"name": "Charlie", "age": 35, "city": "Seattle"})

	// Query: city == "Seattle"
	iter := col.Where("city", "==", "Seattle").Documents(ctx)
	qCount := 0
	for {
		_, err := iter.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			check("Query (city==Seattle)", err)
			break
		}
		qCount++
	}
	if qCount == 2 {
		check("Query city==Seattle (2 results)", nil)
	} else {
		check("Query city==Seattle (2 results)", fmt.Errorf("got %d", qCount))
	}

	// Query: age >= 30, ordered by age.
	iter = col.Where("age", ">=", 30).OrderBy("age", firestore.Asc).Documents(ctx)
	var ages []int
	for {
		snap, err := iter.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			check("Query (age>=30)", err)
			break
		}
		if v, ok := snap.Data()["age"]; ok {
			switch a := v.(type) {
			case int64:
				ages = append(ages, int(a))
			case float64:
				ages = append(ages, int(a))
			}
		}
	}
	if len(ages) == 2 && ages[0] <= ages[1] {
		check("Query age>=30 ordered (2 results, ascending)", nil)
	} else {
		check("Query age>=30 ordered (2 results, ascending)",
			fmt.Errorf("got ages=%v", ages))
	}

	// Delete docs.
	if doc != nil {
		_, err = doc.Delete(ctx)
		check("Delete document", err)
	}

	fmt.Println()
}
