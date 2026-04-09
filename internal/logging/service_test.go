package logging

import (
	"context"
	"net"
	"testing"

	"cloud.google.com/go/logging/apiv2/loggingpb"
	"google.golang.org/genproto/googleapis/api/monitoredres"
	ltype "google.golang.org/genproto/googleapis/logging/type"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func testClient(t *testing.T) (loggingpb.LoggingServiceV2Client, func()) {
	t.Helper()
	svc := New("", true)
	ln, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := grpc.NewServer()
	loggingpb.RegisterLoggingServiceV2Server(srv, svc)
	go srv.Serve(ln)

	conn, err := grpc.NewClient(ln.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		srv.Stop()
		t.Fatalf("dial: %v", err)
	}
	client := loggingpb.NewLoggingServiceV2Client(conn)
	return client, func() { conn.Close(); srv.Stop() }
}

func TestWriteAndListLogEntries(t *testing.T) {
	client, cleanup := testClient(t)
	defer cleanup()
	ctx := context.Background()

	// Write entries.
	_, err := client.WriteLogEntries(ctx, &loggingpb.WriteLogEntriesRequest{
		LogName: "projects/test/logs/app",
		Resource: &monitoredres.MonitoredResource{
			Type: "global",
		},
		Entries: []*loggingpb.LogEntry{
			{
				LogName:  "projects/test/logs/app",
				Severity: ltype.LogSeverity_INFO,
				Payload:  &loggingpb.LogEntry_TextPayload{TextPayload: "hello world"},
			},
			{
				LogName:  "projects/test/logs/app",
				Severity: ltype.LogSeverity_ERROR,
				Payload:  &loggingpb.LogEntry_TextPayload{TextPayload: "something broke"},
			},
		},
	})
	if err != nil {
		t.Fatalf("WriteLogEntries: %v", err)
	}

	// List all entries.
	resp, err := client.ListLogEntries(ctx, &loggingpb.ListLogEntriesRequest{
		ResourceNames: []string{"projects/test"},
	})
	if err != nil {
		t.Fatalf("ListLogEntries: %v", err)
	}
	if len(resp.Entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(resp.Entries))
	}
}

func TestListLogs(t *testing.T) {
	client, cleanup := testClient(t)
	defer cleanup()
	ctx := context.Background()

	client.WriteLogEntries(ctx, &loggingpb.WriteLogEntriesRequest{
		Entries: []*loggingpb.LogEntry{
			{LogName: "projects/test/logs/app", Payload: &loggingpb.LogEntry_TextPayload{TextPayload: "a"}},
			{LogName: "projects/test/logs/system", Payload: &loggingpb.LogEntry_TextPayload{TextPayload: "b"}},
		},
	})

	resp, err := client.ListLogs(ctx, &loggingpb.ListLogsRequest{Parent: "projects/test"})
	if err != nil {
		t.Fatalf("ListLogs: %v", err)
	}
	if len(resp.LogNames) != 2 {
		t.Fatalf("expected 2 log names, got %d", len(resp.LogNames))
	}
}

func TestDeleteLog(t *testing.T) {
	client, cleanup := testClient(t)
	defer cleanup()
	ctx := context.Background()

	client.WriteLogEntries(ctx, &loggingpb.WriteLogEntriesRequest{
		Entries: []*loggingpb.LogEntry{
			{LogName: "projects/test/logs/app", Payload: &loggingpb.LogEntry_TextPayload{TextPayload: "entry1"}},
			{LogName: "projects/test/logs/keep", Payload: &loggingpb.LogEntry_TextPayload{TextPayload: "entry2"}},
		},
	})

	_, err := client.DeleteLog(ctx, &loggingpb.DeleteLogRequest{LogName: "projects/test/logs/app"})
	if err != nil {
		t.Fatalf("DeleteLog: %v", err)
	}

	resp, err := client.ListLogEntries(ctx, &loggingpb.ListLogEntriesRequest{
		ResourceNames: []string{"projects/test"},
	})
	if err != nil {
		t.Fatalf("ListLogEntries: %v", err)
	}
	if len(resp.Entries) != 1 {
		t.Fatalf("expected 1 entry after delete, got %d", len(resp.Entries))
	}
	if resp.Entries[0].GetTextPayload() != "entry2" {
		t.Fatalf("wrong entry survived delete")
	}
}

func TestFilterBySeverity(t *testing.T) {
	client, cleanup := testClient(t)
	defer cleanup()
	ctx := context.Background()

	client.WriteLogEntries(ctx, &loggingpb.WriteLogEntriesRequest{
		Entries: []*loggingpb.LogEntry{
			{LogName: "projects/test/logs/app", Severity: ltype.LogSeverity_INFO, Payload: &loggingpb.LogEntry_TextPayload{TextPayload: "info"}},
			{LogName: "projects/test/logs/app", Severity: ltype.LogSeverity_ERROR, Payload: &loggingpb.LogEntry_TextPayload{TextPayload: "error"}},
			{LogName: "projects/test/logs/app", Severity: ltype.LogSeverity_DEBUG, Payload: &loggingpb.LogEntry_TextPayload{TextPayload: "debug"}},
		},
	})

	resp, err := client.ListLogEntries(ctx, &loggingpb.ListLogEntriesRequest{
		ResourceNames: []string{"projects/test"},
		Filter:        "severity>=ERROR",
	})
	if err != nil {
		t.Fatalf("ListLogEntries: %v", err)
	}
	if len(resp.Entries) != 1 {
		t.Fatalf("expected 1 ERROR+ entry, got %d", len(resp.Entries))
	}
}
