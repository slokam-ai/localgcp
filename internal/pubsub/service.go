package pubsub

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"strings"
	"time"

	"cloud.google.com/go/pubsub/apiv1/pubsubpb"
	"github.com/slokam-ai/localgcp/internal/dispatch"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/reflection"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Service implements the Pub/Sub emulator.
type Service struct {
	pubsubpb.UnimplementedPublisherServer
	pubsubpb.UnimplementedSubscriberServer

	dataDir    string
	quiet      bool
	logger     *log.Logger
	store      *Store
	dispatcher *dispatch.Dispatcher
}

// New creates a new Pub/Sub service.
func New(dataDir string, quiet bool) *Service {
	logger := log.New(os.Stderr, "[pubsub] ", log.LstdFlags)
	return &Service{
		dataDir: dataDir,
		quiet:   quiet,
		logger:  logger,
		store:   NewStore(dataDir),
		dispatcher: dispatch.New(dispatch.Config{
			MaxRetries:     3,
			InitialBackoff: 1 * time.Second,
			Multiplier:     2.0,
			MaxBackoff:     10 * time.Second,
			Timeout:        30 * time.Second,
		}),
	}
}

func (s *Service) Name() string { return "Pub/Sub" }

func (s *Service) Start(ctx context.Context, addr string) error {
	srv := grpc.NewServer(
		grpc.UnaryInterceptor(s.loggingInterceptor),
	)
	pubsubpb.RegisterPublisherServer(srv, s)
	pubsubpb.RegisterSubscriberServer(srv, s)
	reflection.Register(srv)

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", addr, err)
	}

	// Start push delivery goroutine.
	go s.pushDeliveryLoop(ctx)

	go func() {
		<-ctx.Done()
		srv.GracefulStop()
	}()

	if err := srv.Serve(ln); err != nil {
		return err
	}
	return nil
}

// pushDeliveryLoop polls push subscriptions and dispatches messages via HTTP.
func (s *Service) pushDeliveryLoop(ctx context.Context) {
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for _, sub := range s.store.PushSubscriptions() {
				msgs := s.store.PullAllUndelivered(sub.Name)
				for _, m := range msgs {
					s.pushMessage(ctx, sub, m)
				}
			}
		}
	}
}

// pushMessage message represents the JSON body POSTed to push endpoints.
type pushMessage struct {
	Message struct {
		Data        string            `json:"data"`
		MessageID   string            `json:"messageId"`
		Attributes  map[string]string `json:"attributes,omitempty"`
		PublishTime string            `json:"publishTime"`
	} `json:"message"`
	Subscription string `json:"subscription"`
}

func (s *Service) pushMessage(ctx context.Context, sub *subscription, m *message) {
	body := pushMessage{Subscription: sub.Name}
	body.Message.Data = encodeBase64(m.Data)
	body.Message.MessageID = m.ID
	body.Message.Attributes = m.Attributes
	body.Message.PublishTime = m.PublishTime.Format(time.RFC3339Nano)

	jsonBody, _ := json.Marshal(body)

	result := s.dispatcher.Dispatch(ctx, sub.PushEndpoint, jsonBody, nil)
	if result.Err == nil {
		// 2xx: auto-ack.
		s.store.Acknowledge(sub.Name, []string{m.AckID})
	} else {
		// Delivery failed. Check if we should dead-letter.
		if sub.DeadLetterTopic != "" && sub.MaxDeliveryAttempts > 0 && m.DeliveryAttempt >= sub.MaxDeliveryAttempts {
			s.store.ForwardToDeadLetter(sub.Name, m.AckID)
			if !s.quiet {
				s.logger.Printf("push %s: dead-lettered message %s after %d attempts", sub.Name, m.ID, m.DeliveryAttempt)
			}
		}
	}
}

func encodeBase64(data []byte) string {
	return base64.StdEncoding.EncodeToString(data)
}


func (s *Service) loggingInterceptor(
	ctx context.Context,
	req interface{},
	info *grpc.UnaryServerInfo,
	handler grpc.UnaryHandler,
) (interface{}, error) {
	resp, err := handler(ctx, req)
	if !s.quiet {
		code := codes.OK
		if err != nil {
			code = status.Code(err)
		}
		s.logger.Printf("%s %s", info.FullMethod, code)
	}
	return resp, err
}

// --- Publisher RPCs ---

func (s *Service) CreateTopic(_ context.Context, req *pubsubpb.Topic) (*pubsubpb.Topic, error) {
	if req.GetName() == "" {
		return nil, status.Errorf(codes.InvalidArgument, "topic name is required")
	}

	t, err := s.store.CreateTopic(req.GetName(), req.GetLabels())
	if err != nil {
		if strings.Contains(err.Error(), "already exists") {
			return nil, status.Errorf(codes.AlreadyExists, "Topic already exists: %s", req.GetName())
		}
		return nil, status.Errorf(codes.Internal, "%v", err)
	}

	return &pubsubpb.Topic{
		Name:   t.Name,
		Labels: t.Labels,
	}, nil
}

func (s *Service) GetTopic(_ context.Context, req *pubsubpb.GetTopicRequest) (*pubsubpb.Topic, error) {
	if req.GetTopic() == "" {
		return nil, status.Errorf(codes.InvalidArgument, "topic name is required")
	}

	t, ok := s.store.GetTopic(req.GetTopic())
	if !ok {
		return nil, status.Errorf(codes.NotFound, "Topic not found: %s", req.GetTopic())
	}

	return &pubsubpb.Topic{
		Name:   t.Name,
		Labels: t.Labels,
	}, nil
}

func (s *Service) ListTopics(_ context.Context, req *pubsubpb.ListTopicsRequest) (*pubsubpb.ListTopicsResponse, error) {
	project := req.GetProject()
	if project == "" {
		return nil, status.Errorf(codes.InvalidArgument, "project is required")
	}

	// Extract project ID from "projects/{project}" format.
	projectID := strings.TrimPrefix(project, "projects/")

	topics := s.store.ListTopics(projectID)
	var pbTopics []*pubsubpb.Topic
	for _, t := range topics {
		pbTopics = append(pbTopics, &pubsubpb.Topic{
			Name:   t.Name,
			Labels: t.Labels,
		})
	}

	return &pubsubpb.ListTopicsResponse{
		Topics: pbTopics,
	}, nil
}

func (s *Service) DeleteTopic(_ context.Context, req *pubsubpb.DeleteTopicRequest) (*emptypb.Empty, error) {
	if req.GetTopic() == "" {
		return nil, status.Errorf(codes.InvalidArgument, "topic name is required")
	}

	err := s.store.DeleteTopic(req.GetTopic())
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return nil, status.Errorf(codes.NotFound, "Topic not found: %s", req.GetTopic())
		}
		return nil, status.Errorf(codes.Internal, "%v", err)
	}

	return &emptypb.Empty{}, nil
}

func (s *Service) Publish(_ context.Context, req *pubsubpb.PublishRequest) (*pubsubpb.PublishResponse, error) {
	if req.GetTopic() == "" {
		return nil, status.Errorf(codes.InvalidArgument, "topic is required")
	}
	if len(req.GetMessages()) == 0 {
		return nil, status.Errorf(codes.InvalidArgument, "messages are required")
	}

	var inputs []messageInput
	for _, m := range req.GetMessages() {
		inputs = append(inputs, messageInput{
			Data:        m.GetData(),
			Attributes:  m.GetAttributes(),
			OrderingKey: m.GetOrderingKey(),
		})
	}

	ids, err := s.store.Publish(req.GetTopic(), inputs)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return nil, status.Errorf(codes.NotFound, "Topic not found: %s", req.GetTopic())
		}
		return nil, status.Errorf(codes.Internal, "%v", err)
	}

	return &pubsubpb.PublishResponse{
		MessageIds: ids,
	}, nil
}

// --- Subscriber RPCs ---

func (s *Service) CreateSubscription(_ context.Context, req *pubsubpb.Subscription) (*pubsubpb.Subscription, error) {
	if req.GetName() == "" {
		return nil, status.Errorf(codes.InvalidArgument, "subscription name is required")
	}
	if req.GetTopic() == "" {
		return nil, status.Errorf(codes.InvalidArgument, "topic is required")
	}

	cfg := SubscriptionConfig{
		Name:               req.GetName(),
		Topic:              req.GetTopic(),
		AckDeadlineSeconds: req.GetAckDeadlineSeconds(),
	}
	if pc := req.GetPushConfig(); pc != nil {
		cfg.PushEndpoint = pc.GetPushEndpoint()
	}
	if dlp := req.GetDeadLetterPolicy(); dlp != nil {
		cfg.DeadLetterTopic = dlp.GetDeadLetterTopic()
		cfg.MaxDeliveryAttempts = dlp.GetMaxDeliveryAttempts()
	}

	sub, err := s.store.CreateSubscription(cfg)
	if err != nil {
		if strings.Contains(err.Error(), "already exists") {
			return nil, status.Errorf(codes.AlreadyExists, "Subscription already exists: %s", req.GetName())
		}
		if strings.Contains(err.Error(), "not found") {
			return nil, status.Errorf(codes.NotFound, "Topic not found: %s", req.GetTopic())
		}
		return nil, status.Errorf(codes.Internal, "%v", err)
	}

	return subscriptionToProto(sub), nil
}

func subscriptionToProto(sub *subscription) *pubsubpb.Subscription {
	pb := &pubsubpb.Subscription{
		Name:               sub.Name,
		Topic:              sub.Topic,
		AckDeadlineSeconds: sub.AckDeadlineSeconds,
	}
	if sub.PushEndpoint != "" {
		pb.PushConfig = &pubsubpb.PushConfig{PushEndpoint: sub.PushEndpoint}
	}
	if sub.DeadLetterTopic != "" {
		pb.DeadLetterPolicy = &pubsubpb.DeadLetterPolicy{
			DeadLetterTopic:     sub.DeadLetterTopic,
			MaxDeliveryAttempts: sub.MaxDeliveryAttempts,
		}
	}
	return pb
}

func (s *Service) GetSubscription(_ context.Context, req *pubsubpb.GetSubscriptionRequest) (*pubsubpb.Subscription, error) {
	if req.GetSubscription() == "" {
		return nil, status.Errorf(codes.InvalidArgument, "subscription name is required")
	}

	sub, ok := s.store.GetSubscription(req.GetSubscription())
	if !ok {
		return nil, status.Errorf(codes.NotFound, "Subscription not found: %s", req.GetSubscription())
	}

	return subscriptionToProto(sub), nil
}

func (s *Service) ListSubscriptions(_ context.Context, req *pubsubpb.ListSubscriptionsRequest) (*pubsubpb.ListSubscriptionsResponse, error) {
	project := req.GetProject()
	if project == "" {
		return nil, status.Errorf(codes.InvalidArgument, "project is required")
	}

	projectID := strings.TrimPrefix(project, "projects/")

	subs := s.store.ListSubscriptions(projectID)
	var pbSubs []*pubsubpb.Subscription
	for _, sub := range subs {
		pbSubs = append(pbSubs, subscriptionToProto(sub))
	}

	return &pubsubpb.ListSubscriptionsResponse{
		Subscriptions: pbSubs,
	}, nil
}

func (s *Service) DeleteSubscription(_ context.Context, req *pubsubpb.DeleteSubscriptionRequest) (*emptypb.Empty, error) {
	if req.GetSubscription() == "" {
		return nil, status.Errorf(codes.InvalidArgument, "subscription name is required")
	}

	err := s.store.DeleteSubscription(req.GetSubscription())
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return nil, status.Errorf(codes.NotFound, "Subscription not found: %s", req.GetSubscription())
		}
		return nil, status.Errorf(codes.Internal, "%v", err)
	}

	return &emptypb.Empty{}, nil
}

func (s *Service) Pull(_ context.Context, req *pubsubpb.PullRequest) (*pubsubpb.PullResponse, error) {
	if req.GetSubscription() == "" {
		return nil, status.Errorf(codes.InvalidArgument, "subscription is required")
	}
	maxMessages := req.GetMaxMessages()
	if maxMessages <= 0 {
		maxMessages = 100
	}

	msgs, err := s.store.Pull(req.GetSubscription(), maxMessages)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return nil, status.Errorf(codes.NotFound, "Subscription not found: %s", req.GetSubscription())
		}
		return nil, status.Errorf(codes.Internal, "%v", err)
	}

	var received []*pubsubpb.ReceivedMessage
	for _, m := range msgs {
		received = append(received, &pubsubpb.ReceivedMessage{
			AckId: m.AckID,
			Message: &pubsubpb.PubsubMessage{
				MessageId:   m.ID,
				Data:        m.Data,
				Attributes:  m.Attributes,
				PublishTime: timestamppb.New(m.PublishTime),
				OrderingKey: m.OrderingKey,
			},
		})
	}

	return &pubsubpb.PullResponse{
		ReceivedMessages: received,
	}, nil
}

func (s *Service) Acknowledge(_ context.Context, req *pubsubpb.AcknowledgeRequest) (*emptypb.Empty, error) {
	if req.GetSubscription() == "" {
		return nil, status.Errorf(codes.InvalidArgument, "subscription is required")
	}

	err := s.store.Acknowledge(req.GetSubscription(), req.GetAckIds())
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return nil, status.Errorf(codes.NotFound, "Subscription not found: %s", req.GetSubscription())
		}
		return nil, status.Errorf(codes.Internal, "%v", err)
	}

	return &emptypb.Empty{}, nil
}

func (s *Service) ModifyAckDeadline(_ context.Context, req *pubsubpb.ModifyAckDeadlineRequest) (*emptypb.Empty, error) {
	if req.GetSubscription() == "" {
		return nil, status.Errorf(codes.InvalidArgument, "subscription is required")
	}

	err := s.store.ModifyAckDeadline(req.GetSubscription(), req.GetAckIds(), req.GetAckDeadlineSeconds())
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return nil, status.Errorf(codes.NotFound, "Subscription not found: %s", req.GetSubscription())
		}
		return nil, status.Errorf(codes.Internal, "%v", err)
	}

	return &emptypb.Empty{}, nil
}

// --- Unimplemented RPCs ---

func (s *Service) UpdateTopic(context.Context, *pubsubpb.UpdateTopicRequest) (*pubsubpb.Topic, error) {
	return nil, status.Errorf(codes.Unimplemented, "localgcp: UpdateTopic not yet supported")
}

func (s *Service) ListTopicSubscriptions(context.Context, *pubsubpb.ListTopicSubscriptionsRequest) (*pubsubpb.ListTopicSubscriptionsResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "localgcp: ListTopicSubscriptions not yet supported")
}

func (s *Service) ListTopicSnapshots(context.Context, *pubsubpb.ListTopicSnapshotsRequest) (*pubsubpb.ListTopicSnapshotsResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "localgcp: ListTopicSnapshots not yet supported")
}

func (s *Service) DetachSubscription(context.Context, *pubsubpb.DetachSubscriptionRequest) (*pubsubpb.DetachSubscriptionResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "localgcp: DetachSubscription not yet supported")
}

func (s *Service) UpdateSubscription(context.Context, *pubsubpb.UpdateSubscriptionRequest) (*pubsubpb.Subscription, error) {
	return nil, status.Errorf(codes.Unimplemented, "localgcp: UpdateSubscription not yet supported")
}

func (s *Service) StreamingPull(stream pubsubpb.Subscriber_StreamingPullServer) error {
	// Read the initial request to get subscription name and config.
	req, err := stream.Recv()
	if err != nil {
		return err
	}

	subName := req.GetSubscription()
	if subName == "" {
		return status.Errorf(codes.InvalidArgument, "subscription is required")
	}

	sub, ok := s.store.GetSubscription(subName)
	if !ok {
		return status.Errorf(codes.NotFound, "Subscription not found: %s", subName)
	}
	if sub.PushEndpoint != "" {
		return status.Errorf(codes.FailedPrecondition, "Subscription %s has a push endpoint configured; cannot use StreamingPull", subName)
	}

	ctx := stream.Context()

	// Process incoming acks/modifyDeadlines in a goroutine.
	go func() {
		for {
			req, err := stream.Recv()
			if err != nil {
				return
			}
			if len(req.GetAckIds()) > 0 {
				s.store.Acknowledge(subName, req.GetAckIds())
			}
			if len(req.GetModifyDeadlineAckIds()) > 0 {
				for i, ackID := range req.GetModifyDeadlineAckIds() {
					deadline := int32(0)
					if i < len(req.GetModifyDeadlineSeconds()) {
						deadline = req.GetModifyDeadlineSeconds()[i]
					}
					s.store.ModifyAckDeadline(subName, []string{ackID}, deadline)
				}
			}
		}
	}()

	// Poll for messages and send them.
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			msgs, _ := s.store.Pull(subName, 100)
			if len(msgs) == 0 {
				continue
			}

			var pbMsgs []*pubsubpb.ReceivedMessage
			for _, m := range msgs {
				pbMsgs = append(pbMsgs, &pubsubpb.ReceivedMessage{
					AckId: m.AckID,
					Message: &pubsubpb.PubsubMessage{
						MessageId:   m.ID,
						Data:        m.Data,
						Attributes:  m.Attributes,
						PublishTime: timestamppb.New(m.PublishTime),
					},
				})
			}

			if err := stream.Send(&pubsubpb.StreamingPullResponse{
				ReceivedMessages: pbMsgs,
			}); err != nil {
				return err
			}
		}
	}
}

func (s *Service) ModifyPushConfig(context.Context, *pubsubpb.ModifyPushConfigRequest) (*emptypb.Empty, error) {
	return nil, status.Errorf(codes.Unimplemented, "localgcp: ModifyPushConfig not yet supported")
}

func (s *Service) GetSnapshot(context.Context, *pubsubpb.GetSnapshotRequest) (*pubsubpb.Snapshot, error) {
	return nil, status.Errorf(codes.Unimplemented, "localgcp: GetSnapshot not yet supported")
}

func (s *Service) ListSnapshots(context.Context, *pubsubpb.ListSnapshotsRequest) (*pubsubpb.ListSnapshotsResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "localgcp: ListSnapshots not yet supported")
}

func (s *Service) CreateSnapshot(context.Context, *pubsubpb.CreateSnapshotRequest) (*pubsubpb.Snapshot, error) {
	return nil, status.Errorf(codes.Unimplemented, "localgcp: CreateSnapshot not yet supported")
}

func (s *Service) UpdateSnapshot(context.Context, *pubsubpb.UpdateSnapshotRequest) (*pubsubpb.Snapshot, error) {
	return nil, status.Errorf(codes.Unimplemented, "localgcp: UpdateSnapshot not yet supported")
}

func (s *Service) DeleteSnapshot(context.Context, *pubsubpb.DeleteSnapshotRequest) (*emptypb.Empty, error) {
	return nil, status.Errorf(codes.Unimplemented, "localgcp: DeleteSnapshot not yet supported")
}

func (s *Service) Seek(context.Context, *pubsubpb.SeekRequest) (*pubsubpb.SeekResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "localgcp: Seek not yet supported")
}
