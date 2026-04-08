package pubsub

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"cloud.google.com/go/pubsub/apiv1/pubsubpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

func pushTestClients(t *testing.T) (pubsubpb.PublisherClient, pubsubpb.SubscriberClient, func()) {
	t.Helper()

	svc := New("", true)
	ln, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	srv := grpc.NewServer()
	pubsubpb.RegisterPublisherServer(srv, svc)
	pubsubpb.RegisterSubscriberServer(srv, svc)
	go srv.Serve(ln)

	// Start push delivery loop.
	ctx, cancel := context.WithCancel(context.Background())
	go svc.pushDeliveryLoop(ctx)

	conn, err := grpc.NewClient(ln.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		cancel()
		srv.Stop()
		t.Fatalf("dial: %v", err)
	}

	pub := pubsubpb.NewPublisherClient(conn)
	sub := pubsubpb.NewSubscriberClient(conn)
	cleanup := func() {
		cancel()
		conn.Close()
		srv.Stop()
	}
	return pub, sub, cleanup
}

func TestPushSubscriptionDelivery(t *testing.T) {
	pub, sub, cleanup := pushTestClients(t)
	defer cleanup()

	// Start HTTP server to receive push messages.
	var mu sync.Mutex
	var received []pushMessage
	pushSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var msg pushMessage
		json.NewDecoder(r.Body).Decode(&msg)
		mu.Lock()
		received = append(received, msg)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer pushSrv.Close()

	ctx := context.Background()

	// Create topic.
	pub.CreateTopic(ctx, &pubsubpb.Topic{
		Name: "projects/test/topics/push-topic",
	})

	// Create push subscription.
	sub.CreateSubscription(ctx, &pubsubpb.Subscription{
		Name:  "projects/test/subscriptions/push-sub",
		Topic: "projects/test/topics/push-topic",
		PushConfig: &pubsubpb.PushConfig{
			PushEndpoint: pushSrv.URL,
		},
	})

	// Publish a message.
	pub.Publish(ctx, &pubsubpb.PublishRequest{
		Topic: "projects/test/topics/push-topic",
		Messages: []*pubsubpb.PubsubMessage{
			{Data: []byte("hello push")},
		},
	})

	// Wait for push delivery.
	deadline := time.After(5 * time.Second)
	for {
		mu.Lock()
		n := len(received)
		mu.Unlock()
		if n > 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("timeout waiting for push delivery")
		case <-time.After(50 * time.Millisecond):
		}
	}

	mu.Lock()
	if len(received) != 1 {
		t.Fatalf("expected 1 push message, got %d", len(received))
	}
	if received[0].Message.MessageID == "" {
		t.Fatal("push message missing messageId")
	}
	mu.Unlock()
}

func TestPushSubscriptionStreamingPullBlocked(t *testing.T) {
	pub, sub, cleanup := pushTestClients(t)
	defer cleanup()

	ctx := context.Background()

	pub.CreateTopic(ctx, &pubsubpb.Topic{
		Name: "projects/test/topics/push-topic2",
	})

	sub.CreateSubscription(ctx, &pubsubpb.Subscription{
		Name:  "projects/test/subscriptions/push-sub2",
		Topic: "projects/test/topics/push-topic2",
		PushConfig: &pubsubpb.PushConfig{
			PushEndpoint: "http://localhost:9999",
		},
	})

	// StreamingPull should fail with FAILED_PRECONDITION.
	stream, err := sub.StreamingPull(ctx)
	if err != nil {
		t.Fatalf("StreamingPull dial: %v", err)
	}
	err = stream.Send(&pubsubpb.StreamingPullRequest{
		Subscription: "projects/test/subscriptions/push-sub2",
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}

	// Try to receive. Should get an error.
	_, err = stream.Recv()
	if err == nil {
		t.Fatal("expected error for StreamingPull on push subscription")
	}
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.FailedPrecondition {
		t.Fatalf("expected FailedPrecondition, got %v", err)
	}
}

func TestPushSubscriptionDeadLetter(t *testing.T) {
	pub, sub, cleanup := pushTestClients(t)
	defer cleanup()

	// Push endpoint that always fails.
	failSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer failSrv.Close()

	ctx := context.Background()

	// Create main topic + dead letter topic.
	pub.CreateTopic(ctx, &pubsubpb.Topic{Name: "projects/test/topics/main"})
	pub.CreateTopic(ctx, &pubsubpb.Topic{Name: "projects/test/topics/dead"})

	// Create pull subscription on dead letter topic to verify forwarding.
	sub.CreateSubscription(ctx, &pubsubpb.Subscription{
		Name:  "projects/test/subscriptions/dead-pull",
		Topic: "projects/test/topics/dead",
	})

	// Create push subscription with dead letter policy (max 1 attempt for fast test).
	sub.CreateSubscription(ctx, &pubsubpb.Subscription{
		Name:  "projects/test/subscriptions/push-dl",
		Topic: "projects/test/topics/main",
		PushConfig: &pubsubpb.PushConfig{
			PushEndpoint: failSrv.URL,
		},
		DeadLetterPolicy: &pubsubpb.DeadLetterPolicy{
			DeadLetterTopic:     "projects/test/topics/dead",
			MaxDeliveryAttempts: 1,
		},
	})

	// Publish.
	pub.Publish(ctx, &pubsubpb.PublishRequest{
		Topic: "projects/test/topics/main",
		Messages: []*pubsubpb.PubsubMessage{
			{Data: []byte("will fail")},
		},
	})

	// Wait for dead letter delivery.
	deadline := time.After(10 * time.Second)
	for {
		resp, err := sub.Pull(ctx, &pubsubpb.PullRequest{
			Subscription: "projects/test/subscriptions/dead-pull",
			MaxMessages:  10,
		})
		if err == nil && len(resp.ReceivedMessages) > 0 {
			if string(resp.ReceivedMessages[0].Message.Data) != "will fail" {
				t.Fatalf("wrong dead letter data: %s", resp.ReceivedMessages[0].Message.Data)
			}
			break
		}
		select {
		case <-deadline:
			t.Fatal("timeout waiting for dead letter message")
		case <-time.After(200 * time.Millisecond):
		}
	}
}
