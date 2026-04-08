package cloudtasks

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"cloud.google.com/go/cloudtasks/apiv2/cloudtaskspb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const testParent = "projects/test/locations/us-central1"
const testQueue = testParent + "/queues/my-queue"

func taskTestClient(t *testing.T) (cloudtaskspb.CloudTasksClient, *Service, func()) {
	t.Helper()
	svc := New("", true)
	ln, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := grpc.NewServer()
	cloudtaskspb.RegisterCloudTasksServer(srv, &cloudTasksServer{svc: svc})
	go srv.Serve(ln)

	ctx, cancel := context.WithCancel(context.Background())
	go svc.dispatchLoop(ctx)

	conn, err := grpc.NewClient(ln.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		cancel()
		srv.Stop()
		t.Fatalf("dial: %v", err)
	}
	client := cloudtaskspb.NewCloudTasksClient(conn)
	cleanup := func() {
		cancel()
		conn.Close()
		srv.Stop()
	}
	return client, svc, cleanup
}

func TestCreateAndGetQueue(t *testing.T) {
	client, _, cleanup := taskTestClient(t)
	defer cleanup()
	ctx := context.Background()

	q, err := client.CreateQueue(ctx, &cloudtaskspb.CreateQueueRequest{
		Parent: testParent,
		Queue: &cloudtaskspb.Queue{
			Name: testQueue,
			RetryConfig: &cloudtaskspb.RetryConfig{
				MaxAttempts:  5,
				MinBackoff:   &durationpb.Duration{Seconds: 1},
				MaxBackoff:   &durationpb.Duration{Seconds: 30},
				MaxDoublings: 3,
			},
		},
	})
	if err != nil {
		t.Fatalf("CreateQueue: %v", err)
	}
	if q.Name != testQueue {
		t.Fatalf("got name %q", q.Name)
	}

	got, err := client.GetQueue(ctx, &cloudtaskspb.GetQueueRequest{Name: testQueue})
	if err != nil {
		t.Fatalf("GetQueue: %v", err)
	}
	if got.Name != testQueue {
		t.Fatalf("got name %q", got.Name)
	}
	if got.RetryConfig.MaxAttempts != 5 {
		t.Fatalf("max attempts: got %d", got.RetryConfig.MaxAttempts)
	}
}

func TestListQueues(t *testing.T) {
	client, _, cleanup := taskTestClient(t)
	defer cleanup()
	ctx := context.Background()

	client.CreateQueue(ctx, &cloudtaskspb.CreateQueueRequest{
		Parent: testParent,
		Queue:  &cloudtaskspb.Queue{Name: testParent + "/queues/a"},
	})
	client.CreateQueue(ctx, &cloudtaskspb.CreateQueueRequest{
		Parent: testParent,
		Queue:  &cloudtaskspb.Queue{Name: testParent + "/queues/b"},
	})

	resp, err := client.ListQueues(ctx, &cloudtaskspb.ListQueuesRequest{Parent: testParent})
	if err != nil {
		t.Fatalf("ListQueues: %v", err)
	}
	if len(resp.Queues) != 2 {
		t.Fatalf("expected 2 queues, got %d", len(resp.Queues))
	}
}

func TestDeleteQueue(t *testing.T) {
	client, _, cleanup := taskTestClient(t)
	defer cleanup()
	ctx := context.Background()

	client.CreateQueue(ctx, &cloudtaskspb.CreateQueueRequest{
		Parent: testParent,
		Queue:  &cloudtaskspb.Queue{Name: testQueue},
	})
	_, err := client.DeleteQueue(ctx, &cloudtaskspb.DeleteQueueRequest{Name: testQueue})
	if err != nil {
		t.Fatalf("DeleteQueue: %v", err)
	}
	_, err = client.GetQueue(ctx, &cloudtaskspb.GetQueueRequest{Name: testQueue})
	if err == nil {
		t.Fatal("expected NotFound after delete")
	}
	if status.Code(err) != codes.NotFound {
		t.Fatalf("expected NotFound, got %v", err)
	}
}

func TestPurgeQueue(t *testing.T) {
	client, _, cleanup := taskTestClient(t)
	defer cleanup()
	ctx := context.Background()

	client.CreateQueue(ctx, &cloudtaskspb.CreateQueueRequest{
		Parent: testParent,
		Queue:  &cloudtaskspb.Queue{Name: testQueue},
	})

	// Add some tasks.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	defer srv.Close()

	client.CreateTask(ctx, &cloudtaskspb.CreateTaskRequest{
		Parent: testQueue,
		Task: &cloudtaskspb.Task{
			ScheduleTime: timestamppb.New(time.Now().Add(1 * time.Hour)), // far future
			MessageType: &cloudtaskspb.Task_HttpRequest{
				HttpRequest: &cloudtaskspb.HttpRequest{Url: srv.URL},
			},
		},
	})

	// Purge.
	_, err := client.PurgeQueue(ctx, &cloudtaskspb.PurgeQueueRequest{Name: testQueue})
	if err != nil {
		t.Fatalf("PurgeQueue: %v", err)
	}

	resp, _ := client.ListTasks(ctx, &cloudtaskspb.ListTasksRequest{Parent: testQueue})
	if len(resp.Tasks) != 0 {
		t.Fatalf("expected 0 tasks after purge, got %d", len(resp.Tasks))
	}
}

func TestCreateAndListTasks(t *testing.T) {
	client, _, cleanup := taskTestClient(t)
	defer cleanup()
	ctx := context.Background()

	client.CreateQueue(ctx, &cloudtaskspb.CreateQueueRequest{
		Parent: testParent,
		Queue:  &cloudtaskspb.Queue{Name: testQueue},
	})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	defer srv.Close()

	task, err := client.CreateTask(ctx, &cloudtaskspb.CreateTaskRequest{
		Parent: testQueue,
		Task: &cloudtaskspb.Task{
			Name:         testQueue + "/tasks/task1",
			ScheduleTime: timestamppb.New(time.Now().Add(1 * time.Hour)),
			MessageType: &cloudtaskspb.Task_HttpRequest{
				HttpRequest: &cloudtaskspb.HttpRequest{
					Url:        srv.URL,
					HttpMethod: cloudtaskspb.HttpMethod_POST,
					Body:       []byte(`{"key":"value"}`),
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if task.Name != testQueue+"/tasks/task1" {
		t.Fatalf("got task name %q", task.Name)
	}

	resp, _ := client.ListTasks(ctx, &cloudtaskspb.ListTasksRequest{Parent: testQueue})
	if len(resp.Tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(resp.Tasks))
	}
}

func TestTaskDispatch(t *testing.T) {
	client, _, cleanup := taskTestClient(t)
	defer cleanup()
	ctx := context.Background()

	var received atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.Add(1)
		w.WriteHeader(200)
	}))
	defer srv.Close()

	client.CreateQueue(ctx, &cloudtaskspb.CreateQueueRequest{
		Parent: testParent,
		Queue:  &cloudtaskspb.Queue{Name: testQueue},
	})

	// Create task with immediate schedule.
	client.CreateTask(ctx, &cloudtaskspb.CreateTaskRequest{
		Parent: testQueue,
		Task: &cloudtaskspb.Task{
			MessageType: &cloudtaskspb.Task_HttpRequest{
				HttpRequest: &cloudtaskspb.HttpRequest{Url: srv.URL},
			},
		},
	})

	// Wait for dispatch.
	deadline := time.After(5 * time.Second)
	for {
		if received.Load() > 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("timeout waiting for task dispatch")
		case <-time.After(50 * time.Millisecond):
		}
	}

	// Task should be removed after successful dispatch.
	time.Sleep(200 * time.Millisecond)
	resp, _ := client.ListTasks(ctx, &cloudtaskspb.ListTasksRequest{Parent: testQueue})
	if len(resp.Tasks) != 0 {
		t.Fatalf("task should be removed after dispatch, got %d", len(resp.Tasks))
	}
}

func TestTaskScheduledDelay(t *testing.T) {
	client, _, cleanup := taskTestClient(t)
	defer cleanup()
	ctx := context.Background()

	var received atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.Add(1)
		w.WriteHeader(200)
	}))
	defer srv.Close()

	client.CreateQueue(ctx, &cloudtaskspb.CreateQueueRequest{
		Parent: testParent,
		Queue:  &cloudtaskspb.Queue{Name: testQueue},
	})

	// Schedule 1 second in the future.
	client.CreateTask(ctx, &cloudtaskspb.CreateTaskRequest{
		Parent: testQueue,
		Task: &cloudtaskspb.Task{
			ScheduleTime: timestamppb.New(time.Now().Add(1 * time.Second)),
			MessageType: &cloudtaskspb.Task_HttpRequest{
				HttpRequest: &cloudtaskspb.HttpRequest{Url: srv.URL},
			},
		},
	})

	// Should NOT be dispatched immediately.
	time.Sleep(300 * time.Millisecond)
	if received.Load() > 0 {
		t.Fatal("task dispatched too early")
	}

	// Should be dispatched after ~1 second.
	deadline := time.After(3 * time.Second)
	for {
		if received.Load() > 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("timeout waiting for scheduled task dispatch")
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func TestTaskRetryAndDrop(t *testing.T) {
	client, _, cleanup := taskTestClient(t)
	defer cleanup()
	ctx := context.Background()

	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.WriteHeader(500)
	}))
	defer srv.Close()

	client.CreateQueue(ctx, &cloudtaskspb.CreateQueueRequest{
		Parent: testParent,
		Queue: &cloudtaskspb.Queue{
			Name: testQueue,
			RetryConfig: &cloudtaskspb.RetryConfig{
				MaxAttempts:  2,
				MinBackoff:   &durationpb.Duration{Seconds: 0},
				MaxBackoff:   &durationpb.Duration{Seconds: 1},
				MaxDoublings: 0,
			},
		},
	})

	client.CreateTask(ctx, &cloudtaskspb.CreateTaskRequest{
		Parent: testQueue,
		Task: &cloudtaskspb.Task{
			MessageType: &cloudtaskspb.Task_HttpRequest{
				HttpRequest: &cloudtaskspb.HttpRequest{Url: srv.URL},
			},
		},
	})

	// Wait for task to be dropped after max attempts.
	deadline := time.After(10 * time.Second)
	for {
		resp, _ := client.ListTasks(ctx, &cloudtaskspb.ListTasksRequest{Parent: testQueue})
		if len(resp.Tasks) == 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("timeout: task still present after %d attempts", attempts.Load())
		case <-time.After(200 * time.Millisecond):
		}
	}

	if a := attempts.Load(); a < 2 {
		t.Fatalf("expected at least 2 attempts, got %d", a)
	}
}

func TestDeleteTask(t *testing.T) {
	client, _, cleanup := taskTestClient(t)
	defer cleanup()
	ctx := context.Background()

	client.CreateQueue(ctx, &cloudtaskspb.CreateQueueRequest{
		Parent: testParent,
		Queue:  &cloudtaskspb.Queue{Name: testQueue},
	})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	defer srv.Close()

	task, _ := client.CreateTask(ctx, &cloudtaskspb.CreateTaskRequest{
		Parent: testQueue,
		Task: &cloudtaskspb.Task{
			Name:         testQueue + "/tasks/to-delete",
			ScheduleTime: timestamppb.New(time.Now().Add(1 * time.Hour)),
			MessageType: &cloudtaskspb.Task_HttpRequest{
				HttpRequest: &cloudtaskspb.HttpRequest{Url: srv.URL},
			},
		},
	})

	_, err := client.DeleteTask(ctx, &cloudtaskspb.DeleteTaskRequest{Name: task.Name})
	if err != nil {
		t.Fatalf("DeleteTask: %v", err)
	}

	resp, _ := client.ListTasks(ctx, &cloudtaskspb.ListTasksRequest{Parent: testQueue})
	if len(resp.Tasks) != 0 {
		t.Fatal("task should be deleted")
	}
}
