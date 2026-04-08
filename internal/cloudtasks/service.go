package cloudtasks

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"strings"
	"time"

	"cloud.google.com/go/cloudtasks/apiv2/cloudtaskspb"
	"github.com/slokam-ai/localgcp/internal/dispatch"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/reflection"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Service implements the Cloud Tasks emulator.
type Service struct {
	dataDir    string
	quiet      bool
	logger     *log.Logger
	store      *Store
	dispatcher *dispatch.Dispatcher
}

// New creates a new Cloud Tasks service.
func New(dataDir string, quiet bool) *Service {
	logger := log.New(os.Stderr, "[cloudtasks] ", log.LstdFlags)
	return &Service{
		dataDir: dataDir,
		quiet:   quiet,
		logger:  logger,
		store:   NewStore(dataDir),
		dispatcher: dispatch.New(dispatch.Config{
			MaxRetries:     0, // We handle retries at the task level.
			InitialBackoff: 0,
			Multiplier:     1,
			MaxBackoff:     0,
			Timeout:        30 * time.Second,
		}),
	}
}

func (s *Service) Name() string { return "Cloud Tasks" }

func (s *Service) Start(ctx context.Context, addr string) error {
	srv := grpc.NewServer(
		grpc.UnaryInterceptor(s.loggingInterceptor),
	)
	cloudtaskspb.RegisterCloudTasksServer(srv, &cloudTasksServer{svc: s})
	reflection.Register(srv)

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", addr, err)
	}

	// Start task dispatch polling loop.
	go s.dispatchLoop(ctx)

	go func() {
		<-ctx.Done()
		srv.GracefulStop()
	}()

	return srv.Serve(ln)
}

// dispatchLoop polls for due tasks every 500ms and dispatches them.
//
//	TASK LIFECYCLE
//	══════════════
//	PENDING ──► schedule time passed? ──► DISPATCHING ──► HTTP POST
//	                                          │
//	                                     2xx? ─┬─► COMPLETED (removed)
//	                                           └─► retry? ──► PENDING (with backoff)
//	                                                  └─► max attempts ──► dropped
func (s *Service) dispatchLoop(ctx context.Context) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for _, t := range s.store.DueTasks() {
				t := t // capture loop var
				go s.dispatchTask(ctx, &t)
			}
		}
	}
}

func (s *Service) dispatchTask(ctx context.Context, t *task) {
	result := s.dispatcher.Dispatch(ctx, t.HttpURL, t.HttpBody, t.HttpHeaders)
	if result.Err == nil {
		s.store.CompleteTask(t.Queue, t.Name)
		if !s.quiet {
			s.logger.Printf("dispatched %s -> %s %d", t.Name, t.HttpURL, result.StatusCode)
		}
	} else {
		if !s.store.RetryTask(t.Queue, t.Name) {
			if !s.quiet {
				s.logger.Printf("dropped %s after %d attempts", t.Name, t.DispatchCount)
			}
		}
	}
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

// --- gRPC server ---

type cloudTasksServer struct {
	cloudtaskspb.UnimplementedCloudTasksServer
	svc *Service
}

func (cs *cloudTasksServer) CreateQueue(_ context.Context, req *cloudtaskspb.CreateQueueRequest) (*cloudtaskspb.Queue, error) {
	q := req.GetQueue()
	if q == nil || q.GetName() == "" {
		// Auto-generate name from parent.
		if req.GetParent() == "" {
			return nil, status.Errorf(codes.InvalidArgument, "parent is required")
		}
	}

	name := q.GetName()
	if name == "" {
		return nil, status.Errorf(codes.InvalidArgument, "queue name is required")
	}

	var maxAttempts, minBackoff, maxBackoff, maxDoublings int32
	if rc := q.GetRetryConfig(); rc != nil {
		maxAttempts = rc.GetMaxAttempts()
		if rc.GetMinBackoff() != nil {
			minBackoff = int32(rc.GetMinBackoff().GetSeconds())
		}
		if rc.GetMaxBackoff() != nil {
			maxBackoff = int32(rc.GetMaxBackoff().GetSeconds())
		}
		maxDoublings = rc.GetMaxDoublings()
	}

	stored, err := cs.svc.store.CreateQueue(name, maxAttempts, minBackoff, maxBackoff, maxDoublings)
	if err != nil {
		if strings.Contains(err.Error(), "already exists") {
			return nil, status.Errorf(codes.AlreadyExists, "%v", err)
		}
		return nil, status.Errorf(codes.Internal, "%v", err)
	}
	return queueToProto(stored), nil
}

func (cs *cloudTasksServer) GetQueue(_ context.Context, req *cloudtaskspb.GetQueueRequest) (*cloudtaskspb.Queue, error) {
	q, ok := cs.svc.store.GetQueue(req.GetName())
	if !ok {
		return nil, status.Errorf(codes.NotFound, "queue %q not found", req.GetName())
	}
	return queueToProto(q), nil
}

func (cs *cloudTasksServer) ListQueues(_ context.Context, req *cloudtaskspb.ListQueuesRequest) (*cloudtaskspb.ListQueuesResponse, error) {
	queues := cs.svc.store.ListQueues(req.GetParent())
	var pb []*cloudtaskspb.Queue
	for _, q := range queues {
		pb = append(pb, queueToProto(q))
	}
	return &cloudtaskspb.ListQueuesResponse{Queues: pb}, nil
}

func (cs *cloudTasksServer) DeleteQueue(_ context.Context, req *cloudtaskspb.DeleteQueueRequest) (*emptypb.Empty, error) {
	if err := cs.svc.store.DeleteQueue(req.GetName()); err != nil {
		if strings.Contains(err.Error(), "not found") {
			return nil, status.Errorf(codes.NotFound, "%v", err)
		}
		return nil, status.Errorf(codes.Internal, "%v", err)
	}
	return &emptypb.Empty{}, nil
}

func (cs *cloudTasksServer) PurgeQueue(_ context.Context, req *cloudtaskspb.PurgeQueueRequest) (*cloudtaskspb.Queue, error) {
	if err := cs.svc.store.PurgeQueue(req.GetName()); err != nil {
		if strings.Contains(err.Error(), "not found") {
			return nil, status.Errorf(codes.NotFound, "%v", err)
		}
		return nil, status.Errorf(codes.Internal, "%v", err)
	}
	q, _ := cs.svc.store.GetQueue(req.GetName())
	return queueToProto(q), nil
}

func (cs *cloudTasksServer) CreateTask(_ context.Context, req *cloudtaskspb.CreateTaskRequest) (*cloudtaskspb.Task, error) {
	queueName := req.GetParent()
	if queueName == "" {
		return nil, status.Errorf(codes.InvalidArgument, "parent (queue) is required")
	}

	t := req.GetTask()
	var taskName, httpURL, httpMethod string
	var body []byte
	var headers map[string]string
	var scheduleTime time.Time

	if t != nil {
		taskName = t.GetName()
		if t.GetScheduleTime() != nil {
			scheduleTime = t.GetScheduleTime().AsTime()
		}
		if ht := t.GetHttpRequest(); ht != nil {
			httpURL = ht.GetUrl()
			httpMethod = ht.GetHttpMethod().String()
			body = ht.GetBody()
			headers = ht.GetHeaders()
		}
	}

	if httpURL == "" {
		return nil, status.Errorf(codes.InvalidArgument, "task must have an http_request with url")
	}

	stored, err := cs.svc.store.CreateTask(queueName, taskName, httpURL, httpMethod, body, headers, scheduleTime)
	if err != nil {
		if strings.Contains(err.Error(), "already exists") {
			return nil, status.Errorf(codes.AlreadyExists, "%v", err)
		}
		if strings.Contains(err.Error(), "not found") {
			return nil, status.Errorf(codes.NotFound, "%v", err)
		}
		return nil, status.Errorf(codes.Internal, "%v", err)
	}
	return taskToProto(stored), nil
}

func (cs *cloudTasksServer) GetTask(_ context.Context, req *cloudtaskspb.GetTaskRequest) (*cloudtaskspb.Task, error) {
	queueName := queueFromTaskName(req.GetName())
	t, ok := cs.svc.store.GetTask(queueName, req.GetName())
	if !ok {
		return nil, status.Errorf(codes.NotFound, "task %q not found", req.GetName())
	}
	return taskToProto(&t), nil
}

func (cs *cloudTasksServer) ListTasks(_ context.Context, req *cloudtaskspb.ListTasksRequest) (*cloudtaskspb.ListTasksResponse, error) {
	tasks := cs.svc.store.ListTasks(req.GetParent())
	var pb []*cloudtaskspb.Task
	for i := range tasks {
		pb = append(pb, taskToProto(&tasks[i]))
	}
	return &cloudtaskspb.ListTasksResponse{Tasks: pb}, nil
}

func (cs *cloudTasksServer) DeleteTask(_ context.Context, req *cloudtaskspb.DeleteTaskRequest) (*emptypb.Empty, error) {
	queueName := queueFromTaskName(req.GetName())
	if err := cs.svc.store.DeleteTask(queueName, req.GetName()); err != nil {
		if strings.Contains(err.Error(), "not found") {
			return nil, status.Errorf(codes.NotFound, "%v", err)
		}
		return nil, status.Errorf(codes.Internal, "%v", err)
	}
	return &emptypb.Empty{}, nil
}

// --- helpers ---

func queueToProto(q *queue) *cloudtaskspb.Queue {
	pb := &cloudtaskspb.Queue{Name: q.Name}
	if q.MaxAttempts > 0 || q.MinBackoffSeconds > 0 || q.MaxBackoffSeconds > 0 {
		pb.RetryConfig = &cloudtaskspb.RetryConfig{
			MaxAttempts:  q.MaxAttempts,
			MaxDoublings: q.MaxDoublings,
		}
		if q.MinBackoffSeconds > 0 {
			pb.RetryConfig.MinBackoff = &durationpb.Duration{Seconds: int64(q.MinBackoffSeconds)}
		}
		if q.MaxBackoffSeconds > 0 {
			pb.RetryConfig.MaxBackoff = &durationpb.Duration{Seconds: int64(q.MaxBackoffSeconds)}
		}
	}
	return pb
}

func taskToProto(t *task) *cloudtaskspb.Task {
	pb := &cloudtaskspb.Task{
		Name:          t.Name,
		ScheduleTime:  timestamppb.New(t.ScheduleTime),
		CreateTime:    timestamppb.New(t.CreateTime),
		DispatchCount: t.DispatchCount,
		ResponseCount: t.ResponseCount,
	}
	if t.HttpURL != "" {
		method := cloudtaskspb.HttpMethod_POST
		switch t.HttpMethod {
		case "GET":
			method = cloudtaskspb.HttpMethod_GET
		case "PUT":
			method = cloudtaskspb.HttpMethod_PUT
		case "DELETE":
			method = cloudtaskspb.HttpMethod_DELETE
		case "PATCH":
			method = cloudtaskspb.HttpMethod_PATCH
		}
		pb.MessageType = &cloudtaskspb.Task_HttpRequest{
			HttpRequest: &cloudtaskspb.HttpRequest{
				Url:        t.HttpURL,
				HttpMethod: method,
				Body:       t.HttpBody,
				Headers:    t.HttpHeaders,
			},
		}
	}
	return pb
}

func queueFromTaskName(taskName string) string {
	// Format: projects/P/locations/L/queues/Q/tasks/T
	idx := strings.Index(taskName, "/tasks/")
	if idx < 0 {
		return ""
	}
	return taskName[:idx]
}
