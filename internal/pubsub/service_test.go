package pubsub

import (
	"context"
	"fmt"
	"net"
	"sort"
	"testing"
	"time"

	"cloud.google.com/go/pubsub/apiv1/pubsubpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

// testClients starts a Pub/Sub service on an ephemeral port and returns
// publisher and subscriber clients.
func testClients(t *testing.T) (pubsubpb.PublisherClient, pubsubpb.SubscriberClient) {
	t.Helper()

	svc := New("", true) // in-memory, quiet

	ln, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	ln.Close()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	started := make(chan struct{})
	go func() {
		close(started)
		svc.Start(ctx, addr)
	}()
	<-started

	// Wait for server to be ready by trying to connect.
	var conn *grpc.ClientConn
	for i := 0; i < 50; i++ {
		conn, err = grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err == nil {
			// Try a simple RPC to confirm the server is serving.
			pubClient := pubsubpb.NewPublisherClient(conn)
			_, rpcErr := pubClient.GetTopic(ctx, &pubsubpb.GetTopicRequest{Topic: "projects/test/topics/nonexistent"})
			if rpcErr != nil && status.Code(rpcErr) == codes.NotFound {
				break // Server is ready.
			}
			if rpcErr == nil {
				break
			}
			conn.Close()
		}
		time.Sleep(20 * time.Millisecond)
	}
	if conn == nil {
		t.Fatal("failed to connect to server")
	}
	t.Cleanup(func() { conn.Close() })

	return pubsubpb.NewPublisherClient(conn), pubsubpb.NewSubscriberClient(conn)
}

// --- Topic tests ---

func TestCreateTopic(t *testing.T) {
	pub, _ := testClients(t)
	ctx := context.Background()

	topic, err := pub.CreateTopic(ctx, &pubsubpb.Topic{
		Name: "projects/test-project/topics/my-topic",
	})
	if err != nil {
		t.Fatalf("CreateTopic: %v", err)
	}
	if topic.Name != "projects/test-project/topics/my-topic" {
		t.Fatalf("expected topic name 'projects/test-project/topics/my-topic', got %q", topic.Name)
	}
}

func TestCreateDuplicateTopic(t *testing.T) {
	pub, _ := testClients(t)
	ctx := context.Background()

	_, err := pub.CreateTopic(ctx, &pubsubpb.Topic{
		Name: "projects/test-project/topics/dup-topic",
	})
	if err != nil {
		t.Fatalf("CreateTopic: %v", err)
	}

	_, err = pub.CreateTopic(ctx, &pubsubpb.Topic{
		Name: "projects/test-project/topics/dup-topic",
	})
	if err == nil {
		t.Fatal("expected error for duplicate topic")
	}
	if status.Code(err) != codes.AlreadyExists {
		t.Fatalf("expected AlreadyExists, got %v", status.Code(err))
	}
}

func TestGetTopic(t *testing.T) {
	pub, _ := testClients(t)
	ctx := context.Background()

	pub.CreateTopic(ctx, &pubsubpb.Topic{
		Name: "projects/test-project/topics/get-me",
	})

	topic, err := pub.GetTopic(ctx, &pubsubpb.GetTopicRequest{
		Topic: "projects/test-project/topics/get-me",
	})
	if err != nil {
		t.Fatalf("GetTopic: %v", err)
	}
	if topic.Name != "projects/test-project/topics/get-me" {
		t.Fatalf("expected 'projects/test-project/topics/get-me', got %q", topic.Name)
	}
}

func TestGetTopicNotFound(t *testing.T) {
	pub, _ := testClients(t)
	ctx := context.Background()

	_, err := pub.GetTopic(ctx, &pubsubpb.GetTopicRequest{
		Topic: "projects/test-project/topics/nope",
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if status.Code(err) != codes.NotFound {
		t.Fatalf("expected NotFound, got %v", status.Code(err))
	}
}

func TestListTopics(t *testing.T) {
	pub, _ := testClients(t)
	ctx := context.Background()

	pub.CreateTopic(ctx, &pubsubpb.Topic{Name: "projects/test-project/topics/alpha"})
	pub.CreateTopic(ctx, &pubsubpb.Topic{Name: "projects/test-project/topics/beta"})
	// Topic in a different project should not appear.
	pub.CreateTopic(ctx, &pubsubpb.Topic{Name: "projects/other-project/topics/gamma"})

	resp, err := pub.ListTopics(ctx, &pubsubpb.ListTopicsRequest{
		Project: "projects/test-project",
	})
	if err != nil {
		t.Fatalf("ListTopics: %v", err)
	}
	if len(resp.Topics) != 2 {
		t.Fatalf("expected 2 topics, got %d", len(resp.Topics))
	}

	names := []string{resp.Topics[0].Name, resp.Topics[1].Name}
	sort.Strings(names)
	if names[0] != "projects/test-project/topics/alpha" || names[1] != "projects/test-project/topics/beta" {
		t.Fatalf("unexpected topics: %v", names)
	}
}

func TestDeleteTopic(t *testing.T) {
	pub, _ := testClients(t)
	ctx := context.Background()

	pub.CreateTopic(ctx, &pubsubpb.Topic{Name: "projects/test-project/topics/del-me"})

	_, err := pub.DeleteTopic(ctx, &pubsubpb.DeleteTopicRequest{
		Topic: "projects/test-project/topics/del-me",
	})
	if err != nil {
		t.Fatalf("DeleteTopic: %v", err)
	}

	// Verify it's gone.
	_, err = pub.GetTopic(ctx, &pubsubpb.GetTopicRequest{
		Topic: "projects/test-project/topics/del-me",
	})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("expected NotFound after delete, got %v", err)
	}
}

func TestDeleteTopicNotFound(t *testing.T) {
	pub, _ := testClients(t)
	ctx := context.Background()

	_, err := pub.DeleteTopic(ctx, &pubsubpb.DeleteTopicRequest{
		Topic: "projects/test-project/topics/nonexistent",
	})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("expected NotFound, got %v", status.Code(err))
	}
}

// --- Subscription tests ---

func TestCreateSubscription(t *testing.T) {
	pub, sub := testClients(t)
	ctx := context.Background()

	pub.CreateTopic(ctx, &pubsubpb.Topic{Name: "projects/test-project/topics/sub-topic"})

	subscription, err := sub.CreateSubscription(ctx, &pubsubpb.Subscription{
		Name:               "projects/test-project/subscriptions/my-sub",
		Topic:              "projects/test-project/topics/sub-topic",
		AckDeadlineSeconds: 15,
	})
	if err != nil {
		t.Fatalf("CreateSubscription: %v", err)
	}
	if subscription.Name != "projects/test-project/subscriptions/my-sub" {
		t.Fatalf("expected subscription name, got %q", subscription.Name)
	}
	if subscription.Topic != "projects/test-project/topics/sub-topic" {
		t.Fatalf("expected topic, got %q", subscription.Topic)
	}
	if subscription.AckDeadlineSeconds != 15 {
		t.Fatalf("expected ack deadline 15, got %d", subscription.AckDeadlineSeconds)
	}
}

func TestCreateSubscriptionDefaultAckDeadline(t *testing.T) {
	pub, sub := testClients(t)
	ctx := context.Background()

	pub.CreateTopic(ctx, &pubsubpb.Topic{Name: "projects/test-project/topics/default-ack"})

	subscription, err := sub.CreateSubscription(ctx, &pubsubpb.Subscription{
		Name:  "projects/test-project/subscriptions/default-ack-sub",
		Topic: "projects/test-project/topics/default-ack",
	})
	if err != nil {
		t.Fatalf("CreateSubscription: %v", err)
	}
	if subscription.AckDeadlineSeconds != 10 {
		t.Fatalf("expected default ack deadline 10, got %d", subscription.AckDeadlineSeconds)
	}
}

func TestCreateSubscriptionNonExistentTopic(t *testing.T) {
	_, sub := testClients(t)
	ctx := context.Background()

	_, err := sub.CreateSubscription(ctx, &pubsubpb.Subscription{
		Name:  "projects/test-project/subscriptions/orphan",
		Topic: "projects/test-project/topics/does-not-exist",
	})
	if err == nil {
		t.Fatal("expected error for non-existent topic")
	}
	if status.Code(err) != codes.NotFound {
		t.Fatalf("expected NotFound, got %v", status.Code(err))
	}
}

func TestCreateDuplicateSubscription(t *testing.T) {
	pub, sub := testClients(t)
	ctx := context.Background()

	pub.CreateTopic(ctx, &pubsubpb.Topic{Name: "projects/test-project/topics/dup-sub-topic"})

	sub.CreateSubscription(ctx, &pubsubpb.Subscription{
		Name:  "projects/test-project/subscriptions/dup-sub",
		Topic: "projects/test-project/topics/dup-sub-topic",
	})

	_, err := sub.CreateSubscription(ctx, &pubsubpb.Subscription{
		Name:  "projects/test-project/subscriptions/dup-sub",
		Topic: "projects/test-project/topics/dup-sub-topic",
	})
	if err == nil {
		t.Fatal("expected error for duplicate subscription")
	}
	if status.Code(err) != codes.AlreadyExists {
		t.Fatalf("expected AlreadyExists, got %v", status.Code(err))
	}
}

func TestGetSubscription(t *testing.T) {
	pub, sub := testClients(t)
	ctx := context.Background()

	pub.CreateTopic(ctx, &pubsubpb.Topic{Name: "projects/test-project/topics/get-sub-topic"})
	sub.CreateSubscription(ctx, &pubsubpb.Subscription{
		Name:  "projects/test-project/subscriptions/get-sub",
		Topic: "projects/test-project/topics/get-sub-topic",
	})

	got, err := sub.GetSubscription(ctx, &pubsubpb.GetSubscriptionRequest{
		Subscription: "projects/test-project/subscriptions/get-sub",
	})
	if err != nil {
		t.Fatalf("GetSubscription: %v", err)
	}
	if got.Name != "projects/test-project/subscriptions/get-sub" {
		t.Fatalf("unexpected name: %q", got.Name)
	}
}

func TestGetSubscriptionNotFound(t *testing.T) {
	_, sub := testClients(t)
	ctx := context.Background()

	_, err := sub.GetSubscription(ctx, &pubsubpb.GetSubscriptionRequest{
		Subscription: "projects/test-project/subscriptions/nope",
	})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("expected NotFound, got %v", status.Code(err))
	}
}

func TestListSubscriptions(t *testing.T) {
	pub, sub := testClients(t)
	ctx := context.Background()

	pub.CreateTopic(ctx, &pubsubpb.Topic{Name: "projects/test-project/topics/list-sub-topic"})
	sub.CreateSubscription(ctx, &pubsubpb.Subscription{
		Name:  "projects/test-project/subscriptions/sub-a",
		Topic: "projects/test-project/topics/list-sub-topic",
	})
	sub.CreateSubscription(ctx, &pubsubpb.Subscription{
		Name:  "projects/test-project/subscriptions/sub-b",
		Topic: "projects/test-project/topics/list-sub-topic",
	})
	// Different project.
	pub.CreateTopic(ctx, &pubsubpb.Topic{Name: "projects/other/topics/other-topic"})
	sub.CreateSubscription(ctx, &pubsubpb.Subscription{
		Name:  "projects/other/subscriptions/other-sub",
		Topic: "projects/other/topics/other-topic",
	})

	resp, err := sub.ListSubscriptions(ctx, &pubsubpb.ListSubscriptionsRequest{
		Project: "projects/test-project",
	})
	if err != nil {
		t.Fatalf("ListSubscriptions: %v", err)
	}
	if len(resp.Subscriptions) != 2 {
		t.Fatalf("expected 2 subscriptions, got %d", len(resp.Subscriptions))
	}
}

func TestDeleteSubscription(t *testing.T) {
	pub, sub := testClients(t)
	ctx := context.Background()

	pub.CreateTopic(ctx, &pubsubpb.Topic{Name: "projects/test-project/topics/del-sub-topic"})
	sub.CreateSubscription(ctx, &pubsubpb.Subscription{
		Name:  "projects/test-project/subscriptions/del-sub",
		Topic: "projects/test-project/topics/del-sub-topic",
	})

	_, err := sub.DeleteSubscription(ctx, &pubsubpb.DeleteSubscriptionRequest{
		Subscription: "projects/test-project/subscriptions/del-sub",
	})
	if err != nil {
		t.Fatalf("DeleteSubscription: %v", err)
	}

	_, err = sub.GetSubscription(ctx, &pubsubpb.GetSubscriptionRequest{
		Subscription: "projects/test-project/subscriptions/del-sub",
	})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("expected NotFound after delete, got %v", err)
	}
}

// --- Publish / Pull / Ack tests ---

func TestPublishAndPull(t *testing.T) {
	pub, sub := testClients(t)
	ctx := context.Background()

	pub.CreateTopic(ctx, &pubsubpb.Topic{Name: "projects/test-project/topics/msg-topic"})
	sub.CreateSubscription(ctx, &pubsubpb.Subscription{
		Name:  "projects/test-project/subscriptions/msg-sub",
		Topic: "projects/test-project/topics/msg-topic",
	})

	// Publish two messages.
	publishResp, err := pub.Publish(ctx, &pubsubpb.PublishRequest{
		Topic: "projects/test-project/topics/msg-topic",
		Messages: []*pubsubpb.PubsubMessage{
			{Data: []byte("hello")},
			{Data: []byte("world"), Attributes: map[string]string{"key": "value"}},
		},
	})
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if len(publishResp.MessageIds) != 2 {
		t.Fatalf("expected 2 message IDs, got %d", len(publishResp.MessageIds))
	}

	// Pull messages.
	pullResp, err := sub.Pull(ctx, &pubsubpb.PullRequest{
		Subscription: "projects/test-project/subscriptions/msg-sub",
		MaxMessages:  10,
	})
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if len(pullResp.ReceivedMessages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(pullResp.ReceivedMessages))
	}

	// Verify message content.
	msgs := pullResp.ReceivedMessages
	// Sort by data for deterministic checks.
	sort.Slice(msgs, func(i, j int) bool {
		return string(msgs[i].Message.Data) < string(msgs[j].Message.Data)
	})

	if string(msgs[0].Message.Data) != "hello" {
		t.Fatalf("expected 'hello', got %q", string(msgs[0].Message.Data))
	}
	if string(msgs[1].Message.Data) != "world" {
		t.Fatalf("expected 'world', got %q", string(msgs[1].Message.Data))
	}
	if msgs[1].Message.Attributes["key"] != "value" {
		t.Fatalf("expected attribute key=value, got %v", msgs[1].Message.Attributes)
	}
	if msgs[0].Message.MessageId == "" {
		t.Fatal("expected non-empty message ID")
	}
	if msgs[0].Message.PublishTime == nil {
		t.Fatal("expected non-nil publish time")
	}
}

func TestPublishToNonExistentTopic(t *testing.T) {
	pub, _ := testClients(t)
	ctx := context.Background()

	_, err := pub.Publish(ctx, &pubsubpb.PublishRequest{
		Topic: "projects/test-project/topics/nonexistent",
		Messages: []*pubsubpb.PubsubMessage{
			{Data: []byte("hello")},
		},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if status.Code(err) != codes.NotFound {
		t.Fatalf("expected NotFound, got %v", status.Code(err))
	}
}

func TestAcknowledge(t *testing.T) {
	pub, sub := testClients(t)
	ctx := context.Background()

	pub.CreateTopic(ctx, &pubsubpb.Topic{Name: "projects/test-project/topics/ack-topic"})
	sub.CreateSubscription(ctx, &pubsubpb.Subscription{
		Name:               "projects/test-project/subscriptions/ack-sub",
		Topic:              "projects/test-project/topics/ack-topic",
		AckDeadlineSeconds: 600, // long deadline so messages won't be redelivered
	})

	pub.Publish(ctx, &pubsubpb.PublishRequest{
		Topic: "projects/test-project/topics/ack-topic",
		Messages: []*pubsubpb.PubsubMessage{
			{Data: []byte("msg1")},
			{Data: []byte("msg2")},
		},
	})

	// Pull messages.
	pullResp, err := sub.Pull(ctx, &pubsubpb.PullRequest{
		Subscription: "projects/test-project/subscriptions/ack-sub",
		MaxMessages:  10,
	})
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if len(pullResp.ReceivedMessages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(pullResp.ReceivedMessages))
	}

	// Ack first message.
	ackIDs := []string{pullResp.ReceivedMessages[0].AckId}
	_, err = sub.Acknowledge(ctx, &pubsubpb.AcknowledgeRequest{
		Subscription: "projects/test-project/subscriptions/ack-sub",
		AckIds:       ackIDs,
	})
	if err != nil {
		t.Fatalf("Acknowledge: %v", err)
	}

	// Pull again — should get 0 because ack deadline hasn't expired for msg2
	// and msg1 was acked.
	pullResp2, err := sub.Pull(ctx, &pubsubpb.PullRequest{
		Subscription: "projects/test-project/subscriptions/ack-sub",
		MaxMessages:  10,
	})
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if len(pullResp2.ReceivedMessages) != 0 {
		t.Fatalf("expected 0 messages after ack (deadline not expired), got %d", len(pullResp2.ReceivedMessages))
	}
}

func TestFanOut(t *testing.T) {
	pub, sub := testClients(t)
	ctx := context.Background()

	pub.CreateTopic(ctx, &pubsubpb.Topic{Name: "projects/test-project/topics/fanout-topic"})

	// Create two subscriptions for the same topic.
	sub.CreateSubscription(ctx, &pubsubpb.Subscription{
		Name:  "projects/test-project/subscriptions/fanout-sub-1",
		Topic: "projects/test-project/topics/fanout-topic",
	})
	sub.CreateSubscription(ctx, &pubsubpb.Subscription{
		Name:  "projects/test-project/subscriptions/fanout-sub-2",
		Topic: "projects/test-project/topics/fanout-topic",
	})

	// Publish one message.
	pub.Publish(ctx, &pubsubpb.PublishRequest{
		Topic: "projects/test-project/topics/fanout-topic",
		Messages: []*pubsubpb.PubsubMessage{
			{Data: []byte("broadcast")},
		},
	})

	// Both subscriptions should get the message.
	for _, subName := range []string{
		"projects/test-project/subscriptions/fanout-sub-1",
		"projects/test-project/subscriptions/fanout-sub-2",
	} {
		pullResp, err := sub.Pull(ctx, &pubsubpb.PullRequest{
			Subscription: subName,
			MaxMessages:  10,
		})
		if err != nil {
			t.Fatalf("Pull(%s): %v", subName, err)
		}
		if len(pullResp.ReceivedMessages) != 1 {
			t.Fatalf("expected 1 message from %s, got %d", subName, len(pullResp.ReceivedMessages))
		}
		if string(pullResp.ReceivedMessages[0].Message.Data) != "broadcast" {
			t.Fatalf("expected 'broadcast', got %q", string(pullResp.ReceivedMessages[0].Message.Data))
		}
	}
}

func TestModifyAckDeadline(t *testing.T) {
	pub, sub := testClients(t)
	ctx := context.Background()

	pub.CreateTopic(ctx, &pubsubpb.Topic{Name: "projects/test-project/topics/mod-ack-topic"})
	sub.CreateSubscription(ctx, &pubsubpb.Subscription{
		Name:               "projects/test-project/subscriptions/mod-ack-sub",
		Topic:              "projects/test-project/topics/mod-ack-topic",
		AckDeadlineSeconds: 600, // long deadline
	})

	pub.Publish(ctx, &pubsubpb.PublishRequest{
		Topic: "projects/test-project/topics/mod-ack-topic",
		Messages: []*pubsubpb.PubsubMessage{
			{Data: []byte("deadline-test")},
		},
	})

	// Pull the message.
	pullResp, err := sub.Pull(ctx, &pubsubpb.PullRequest{
		Subscription: "projects/test-project/subscriptions/mod-ack-sub",
		MaxMessages:  10,
	})
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if len(pullResp.ReceivedMessages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(pullResp.ReceivedMessages))
	}

	ackID := pullResp.ReceivedMessages[0].AckId

	// Modify ack deadline to 0 (nack) — makes it immediately available.
	_, err = sub.ModifyAckDeadline(ctx, &pubsubpb.ModifyAckDeadlineRequest{
		Subscription:       "projects/test-project/subscriptions/mod-ack-sub",
		AckIds:             []string{ackID},
		AckDeadlineSeconds: 0,
	})
	if err != nil {
		t.Fatalf("ModifyAckDeadline: %v", err)
	}

	// Pull again — message should be redelivered.
	pullResp2, err := sub.Pull(ctx, &pubsubpb.PullRequest{
		Subscription: "projects/test-project/subscriptions/mod-ack-sub",
		MaxMessages:  10,
	})
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if len(pullResp2.ReceivedMessages) != 1 {
		t.Fatalf("expected 1 message after nack, got %d", len(pullResp2.ReceivedMessages))
	}
	if string(pullResp2.ReceivedMessages[0].Message.Data) != "deadline-test" {
		t.Fatalf("expected 'deadline-test', got %q", string(pullResp2.ReceivedMessages[0].Message.Data))
	}
}

func TestPullEmptySubscription(t *testing.T) {
	pub, sub := testClients(t)
	ctx := context.Background()

	pub.CreateTopic(ctx, &pubsubpb.Topic{Name: "projects/test-project/topics/empty-topic"})
	sub.CreateSubscription(ctx, &pubsubpb.Subscription{
		Name:  "projects/test-project/subscriptions/empty-sub",
		Topic: "projects/test-project/topics/empty-topic",
	})

	pullResp, err := sub.Pull(ctx, &pubsubpb.PullRequest{
		Subscription: "projects/test-project/subscriptions/empty-sub",
		MaxMessages:  10,
	})
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if len(pullResp.ReceivedMessages) != 0 {
		t.Fatalf("expected 0 messages, got %d", len(pullResp.ReceivedMessages))
	}
}

func TestPullNonExistentSubscription(t *testing.T) {
	_, sub := testClients(t)
	ctx := context.Background()

	_, err := sub.Pull(ctx, &pubsubpb.PullRequest{
		Subscription: "projects/test-project/subscriptions/nonexistent",
		MaxMessages:  10,
	})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("expected NotFound, got %v", status.Code(err))
	}
}

func TestDeleteTopicOrphansSubscription(t *testing.T) {
	pub, sub := testClients(t)
	ctx := context.Background()

	pub.CreateTopic(ctx, &pubsubpb.Topic{Name: "projects/test-project/topics/orphan-topic"})
	sub.CreateSubscription(ctx, &pubsubpb.Subscription{
		Name:  "projects/test-project/subscriptions/orphan-sub",
		Topic: "projects/test-project/topics/orphan-topic",
	})

	// Delete the topic.
	pub.DeleteTopic(ctx, &pubsubpb.DeleteTopicRequest{
		Topic: "projects/test-project/topics/orphan-topic",
	})

	// The subscription should still exist, with topic set to _deleted-topic_.
	got, err := sub.GetSubscription(ctx, &pubsubpb.GetSubscriptionRequest{
		Subscription: "projects/test-project/subscriptions/orphan-sub",
	})
	if err != nil {
		t.Fatalf("GetSubscription: %v", err)
	}
	if got.Topic != "_deleted-topic_" {
		t.Fatalf("expected topic '_deleted-topic_', got %q", got.Topic)
	}
}

func TestMaxMessages(t *testing.T) {
	pub, sub := testClients(t)
	ctx := context.Background()

	pub.CreateTopic(ctx, &pubsubpb.Topic{Name: "projects/test-project/topics/max-topic"})
	sub.CreateSubscription(ctx, &pubsubpb.Subscription{
		Name:  "projects/test-project/subscriptions/max-sub",
		Topic: "projects/test-project/topics/max-topic",
	})

	// Publish 5 messages.
	var msgs []*pubsubpb.PubsubMessage
	for i := 0; i < 5; i++ {
		msgs = append(msgs, &pubsubpb.PubsubMessage{
			Data: []byte(fmt.Sprintf("msg-%d", i)),
		})
	}
	pub.Publish(ctx, &pubsubpb.PublishRequest{
		Topic:    "projects/test-project/topics/max-topic",
		Messages: msgs,
	})

	// Pull only 2.
	pullResp, err := sub.Pull(ctx, &pubsubpb.PullRequest{
		Subscription: "projects/test-project/subscriptions/max-sub",
		MaxMessages:  2,
	})
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if len(pullResp.ReceivedMessages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(pullResp.ReceivedMessages))
	}
}

func TestAnyProjectID(t *testing.T) {
	pub, sub := testClients(t)
	ctx := context.Background()

	// Use a different project ID to verify we accept any project.
	pub.CreateTopic(ctx, &pubsubpb.Topic{Name: "projects/my-custom-project/topics/custom-topic"})
	sub.CreateSubscription(ctx, &pubsubpb.Subscription{
		Name:  "projects/my-custom-project/subscriptions/custom-sub",
		Topic: "projects/my-custom-project/topics/custom-topic",
	})

	pub.Publish(ctx, &pubsubpb.PublishRequest{
		Topic: "projects/my-custom-project/topics/custom-topic",
		Messages: []*pubsubpb.PubsubMessage{
			{Data: []byte("custom project")},
		},
	})

	pullResp, err := sub.Pull(ctx, &pubsubpb.PullRequest{
		Subscription: "projects/my-custom-project/subscriptions/custom-sub",
		MaxMessages:  10,
	})
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if len(pullResp.ReceivedMessages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(pullResp.ReceivedMessages))
	}
}
