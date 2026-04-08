package pubsub

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"strings"
	"time"

	"cloud.google.com/go/pubsub/apiv1/pubsubpb"
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

	dataDir string
	quiet   bool
	logger  *log.Logger
	store   *Store
}

// New creates a new Pub/Sub service.
func New(dataDir string, quiet bool) *Service {
	logger := log.New(os.Stderr, "[pubsub] ", log.LstdFlags)
	return &Service{
		dataDir: dataDir,
		quiet:   quiet,
		logger:  logger,
		store:   NewStore(dataDir),
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

	go func() {
		<-ctx.Done()
		srv.GracefulStop()
	}()

	if err := srv.Serve(ln); err != nil {
		return err
	}
	return nil
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

	sub, err := s.store.CreateSubscription(req.GetName(), req.GetTopic(), req.GetAckDeadlineSeconds())
	if err != nil {
		if strings.Contains(err.Error(), "already exists") {
			return nil, status.Errorf(codes.AlreadyExists, "Subscription already exists: %s", req.GetName())
		}
		if strings.Contains(err.Error(), "not found") {
			return nil, status.Errorf(codes.NotFound, "Topic not found: %s", req.GetTopic())
		}
		return nil, status.Errorf(codes.Internal, "%v", err)
	}

	return &pubsubpb.Subscription{
		Name:               sub.Name,
		Topic:              sub.Topic,
		AckDeadlineSeconds: sub.AckDeadlineSeconds,
	}, nil
}

func (s *Service) GetSubscription(_ context.Context, req *pubsubpb.GetSubscriptionRequest) (*pubsubpb.Subscription, error) {
	if req.GetSubscription() == "" {
		return nil, status.Errorf(codes.InvalidArgument, "subscription name is required")
	}

	sub, ok := s.store.GetSubscription(req.GetSubscription())
	if !ok {
		return nil, status.Errorf(codes.NotFound, "Subscription not found: %s", req.GetSubscription())
	}

	return &pubsubpb.Subscription{
		Name:               sub.Name,
		Topic:              sub.Topic,
		AckDeadlineSeconds: sub.AckDeadlineSeconds,
	}, nil
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
		pbSubs = append(pbSubs, &pubsubpb.Subscription{
			Name:               sub.Name,
			Topic:              sub.Topic,
			AckDeadlineSeconds: sub.AckDeadlineSeconds,
		})
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

	if _, ok := s.store.GetSubscription(subName); !ok {
		return status.Errorf(codes.NotFound, "Subscription not found: %s", subName)
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
