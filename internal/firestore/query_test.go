package firestore

import (
	"context"
	"io"
	"net"
	"testing"

	"cloud.google.com/go/firestore/apiv1/firestorepb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func queryTestClient(t *testing.T) firestorepb.FirestoreClient {
	t.Helper()

	svc := New("", true)
	ln, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	srv := grpc.NewServer()
	firestorepb.RegisterFirestoreServer(srv, &firestoreServer{svc: svc})
	go srv.Serve(ln)

	conn, err := grpc.NewClient(ln.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		srv.Stop()
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() {
		conn.Close()
		srv.Stop()
	})

	client := firestorepb.NewFirestoreClient(conn)

	// Seed test data.
	parent := "projects/test/databases/(default)/documents"
	docs := []struct {
		id     string
		fields map[string]*firestorepb.Value
	}{
		{"doc1", map[string]*firestorepb.Value{
			"status": strVal("active"),
			"tags":   arrayVal("go", "grpc"),
			"score":  intVal(10),
		}},
		{"doc2", map[string]*firestorepb.Value{
			"status": strVal("pending"),
			"tags":   arrayVal("python", "rest"),
			"score":  intVal(20),
		}},
		{"doc3", map[string]*firestorepb.Value{
			"status": strVal("archived"),
			"tags":   arrayVal("go", "rest"),
			"score":  intVal(30),
		}},
		{"doc4", map[string]*firestorepb.Value{
			"status": strVal("active"),
			"tags":   arrayVal("java"),
			"score":  intVal(40),
		}},
	}
	for _, d := range docs {
		_, err := client.CreateDocument(context.Background(), &firestorepb.CreateDocumentRequest{
			Parent:       parent,
			CollectionId: "items",
			DocumentId:   d.id,
			Document:     &firestorepb.Document{Fields: d.fields},
		})
		if err != nil {
			t.Fatalf("seed %s: %v", d.id, err)
		}
	}

	return client
}

func arrayVal(strs ...string) *firestorepb.Value {
	var vals []*firestorepb.Value
	for _, s := range strs {
		vals = append(vals, strVal(s))
	}
	return &firestorepb.Value{
		ValueType: &firestorepb.Value_ArrayValue{
			ArrayValue: &firestorepb.ArrayValue{Values: vals},
		},
	}
}

func runQuery(t *testing.T, client firestorepb.FirestoreClient, filter *firestorepb.StructuredQuery_Filter) []string {
	t.Helper()
	parent := "projects/test/databases/(default)/documents"
	stream, err := client.RunQuery(context.Background(), &firestorepb.RunQueryRequest{
		Parent: parent,
		QueryType: &firestorepb.RunQueryRequest_StructuredQuery{
			StructuredQuery: &firestorepb.StructuredQuery{
				From:  []*firestorepb.StructuredQuery_CollectionSelector{{CollectionId: "items"}},
				Where: filter,
			},
		},
	})
	if err != nil {
		t.Fatalf("RunQuery: %v", err)
	}

	var names []string
	for {
		resp, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Recv: %v", err)
		}
		if resp.Document != nil {
			names = append(names, resp.Document.Name)
		}
	}
	return names
}

func fieldFilter(field string, op firestorepb.StructuredQuery_FieldFilter_Operator, val *firestorepb.Value) *firestorepb.StructuredQuery_Filter {
	return &firestorepb.StructuredQuery_Filter{
		FilterType: &firestorepb.StructuredQuery_Filter_FieldFilter{
			FieldFilter: &firestorepb.StructuredQuery_FieldFilter{
				Field: &firestorepb.StructuredQuery_FieldReference{FieldPath: field},
				Op:    op,
				Value: val,
			},
		},
	}
}

func TestQueryIN(t *testing.T) {
	client := queryTestClient(t)
	// status IN ["active", "pending"] → doc1, doc2, doc4
	names := runQuery(t, client, fieldFilter("status",
		firestorepb.StructuredQuery_FieldFilter_IN,
		arrayVal("active", "pending"),
	))
	if len(names) != 3 {
		t.Fatalf("expected 3 results, got %d: %v", len(names), names)
	}
}

func TestQueryNOT_IN(t *testing.T) {
	client := queryTestClient(t)
	// status NOT_IN ["active", "pending"] → doc3 (archived)
	names := runQuery(t, client, fieldFilter("status",
		firestorepb.StructuredQuery_FieldFilter_NOT_IN,
		arrayVal("active", "pending"),
	))
	if len(names) != 1 {
		t.Fatalf("expected 1 result, got %d: %v", len(names), names)
	}
}

func TestQueryARRAY_CONTAINS(t *testing.T) {
	client := queryTestClient(t)
	// tags ARRAY_CONTAINS "go" → doc1, doc3
	names := runQuery(t, client, fieldFilter("tags",
		firestorepb.StructuredQuery_FieldFilter_ARRAY_CONTAINS,
		strVal("go"),
	))
	if len(names) != 2 {
		t.Fatalf("expected 2 results, got %d: %v", len(names), names)
	}
}

func TestQueryARRAY_CONTAINS_ANY(t *testing.T) {
	client := queryTestClient(t)
	// tags ARRAY_CONTAINS_ANY ["python", "java"] → doc2, doc4
	names := runQuery(t, client, fieldFilter("tags",
		firestorepb.StructuredQuery_FieldFilter_ARRAY_CONTAINS_ANY,
		arrayVal("python", "java"),
	))
	if len(names) != 2 {
		t.Fatalf("expected 2 results, got %d: %v", len(names), names)
	}
}

func TestQueryARRAY_CONTAINS_NoMatch(t *testing.T) {
	client := queryTestClient(t)
	// tags ARRAY_CONTAINS "rust" → none
	names := runQuery(t, client, fieldFilter("tags",
		firestorepb.StructuredQuery_FieldFilter_ARRAY_CONTAINS,
		strVal("rust"),
	))
	if len(names) != 0 {
		t.Fatalf("expected 0 results, got %d: %v", len(names), names)
	}
}

func TestQueryIN_Empty(t *testing.T) {
	client := queryTestClient(t)
	// status IN [] → none
	names := runQuery(t, client, fieldFilter("status",
		firestorepb.StructuredQuery_FieldFilter_IN,
		arrayVal(),
	))
	if len(names) != 0 {
		t.Fatalf("expected 0 results, got %d: %v", len(names), names)
	}
}

func TestQueryARRAY_CONTAINS_NonArrayField(t *testing.T) {
	client := queryTestClient(t)
	// score ARRAY_CONTAINS "go" → none (score is integer, not array)
	names := runQuery(t, client, fieldFilter("score",
		firestorepb.StructuredQuery_FieldFilter_ARRAY_CONTAINS,
		strVal("go"),
	))
	if len(names) != 0 {
		t.Fatalf("expected 0 results for non-array field, got %d: %v", len(names), names)
	}
}

func TestQueryARRAY_CONTAINS_ANY_NoOverlap(t *testing.T) {
	client := queryTestClient(t)
	// tags ARRAY_CONTAINS_ANY ["rust", "c++"] → none
	names := runQuery(t, client, fieldFilter("tags",
		firestorepb.StructuredQuery_FieldFilter_ARRAY_CONTAINS_ANY,
		arrayVal("rust", "c++"),
	))
	if len(names) != 0 {
		t.Fatalf("expected 0 results, got %d: %v", len(names), names)
	}
}
