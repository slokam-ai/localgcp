package firestore

import (
	"context"
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	"cloud.google.com/go/firestore/apiv1/firestorepb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

const testDB = "projects/test-project/databases/(default)"
const testParent = testDB + "/documents"

// testServer starts a Firestore gRPC service on an ephemeral port and returns a connected client.
func testClient(t *testing.T) firestorepb.FirestoreClient {
	t.Helper()

	svc := New("", true) // in-memory, quiet
	ln, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	addr := fmt.Sprintf("localhost:%d", port)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	go svc.Start(ctx, addr)

	// Wait for server to be ready.
	var conn *grpc.ClientConn
	for i := 0; i < 50; i++ {
		conn, err = grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err == nil {
			// Try a quick call to see if server is up.
			c := firestorepb.NewFirestoreClient(conn)
			_, err := c.GetDocument(ctx, &firestorepb.GetDocumentRequest{Name: "projects/x/databases/x/documents/x/x"})
			if err != nil {
				st, ok := status.FromError(err)
				if ok && st.Code() != codes.Unavailable {
					break // server is up (NotFound is expected)
				}
			}
			conn.Close()
		}
		time.Sleep(20 * time.Millisecond)
	}
	if conn == nil {
		t.Fatal("server did not start")
	}
	t.Cleanup(func() { conn.Close() })
	return firestorepb.NewFirestoreClient(conn)
}

func strVal(s string) *firestorepb.Value {
	return &firestorepb.Value{ValueType: &firestorepb.Value_StringValue{StringValue: s}}
}

func intVal(n int64) *firestorepb.Value {
	return &firestorepb.Value{ValueType: &firestorepb.Value_IntegerValue{IntegerValue: n}}
}

func TestCreateAndGetDocument(t *testing.T) {
	client := testClient(t)
	ctx := context.Background()

	// Create a document.
	created, err := client.CreateDocument(ctx, &firestorepb.CreateDocumentRequest{
		Parent:       testParent,
		CollectionId: "users",
		DocumentId:   "alice",
		Document: &firestorepb.Document{
			Fields: map[string]*firestorepb.Value{
				"name": strVal("Alice"),
				"age":  intVal(30),
			},
		},
	})
	if err != nil {
		t.Fatalf("CreateDocument: %v", err)
	}

	wantName := testParent + "/users/alice"
	if created.Name != wantName {
		t.Errorf("name = %q, want %q", created.Name, wantName)
	}
	if created.Fields["name"].GetStringValue() != "Alice" {
		t.Errorf("name field = %q, want %q", created.Fields["name"].GetStringValue(), "Alice")
	}

	// Get the document.
	got, err := client.GetDocument(ctx, &firestorepb.GetDocumentRequest{Name: wantName})
	if err != nil {
		t.Fatalf("GetDocument: %v", err)
	}
	if got.Fields["age"].GetIntegerValue() != 30 {
		t.Errorf("age = %d, want 30", got.Fields["age"].GetIntegerValue())
	}
}

func TestCreateDocumentAutoID(t *testing.T) {
	client := testClient(t)
	ctx := context.Background()

	created, err := client.CreateDocument(ctx, &firestorepb.CreateDocumentRequest{
		Parent:       testParent,
		CollectionId: "items",
		Document: &firestorepb.Document{
			Fields: map[string]*firestorepb.Value{
				"title": strVal("Widget"),
			},
		},
	})
	if err != nil {
		t.Fatalf("CreateDocument: %v", err)
	}
	if created.Name == "" {
		t.Error("expected auto-generated document name")
	}
	if created.Fields["title"].GetStringValue() != "Widget" {
		t.Error("field not preserved")
	}
}

func TestUpdateDocument(t *testing.T) {
	client := testClient(t)
	ctx := context.Background()

	name := testParent + "/users/bob"

	// Create.
	_, err := client.CreateDocument(ctx, &firestorepb.CreateDocumentRequest{
		Parent:       testParent,
		CollectionId: "users",
		DocumentId:   "bob",
		Document: &firestorepb.Document{
			Fields: map[string]*firestorepb.Value{
				"name":  strVal("Bob"),
				"age":   intVal(25),
				"email": strVal("bob@example.com"),
			},
		},
	})
	if err != nil {
		t.Fatalf("CreateDocument: %v", err)
	}

	// Partial update: change age only.
	updated, err := client.UpdateDocument(ctx, &firestorepb.UpdateDocumentRequest{
		Document: &firestorepb.Document{
			Name: name,
			Fields: map[string]*firestorepb.Value{
				"age": intVal(26),
			},
		},
		UpdateMask: &firestorepb.DocumentMask{FieldPaths: []string{"age"}},
	})
	if err != nil {
		t.Fatalf("UpdateDocument: %v", err)
	}

	if updated.Fields["age"].GetIntegerValue() != 26 {
		t.Errorf("age = %d, want 26", updated.Fields["age"].GetIntegerValue())
	}
	// name and email should be preserved.
	if updated.Fields["name"].GetStringValue() != "Bob" {
		t.Errorf("name was modified unexpectedly")
	}
	if updated.Fields["email"].GetStringValue() != "bob@example.com" {
		t.Errorf("email was modified unexpectedly")
	}
}

func TestDeleteDocument(t *testing.T) {
	client := testClient(t)
	ctx := context.Background()

	name := testParent + "/users/charlie"

	// Create.
	_, err := client.CreateDocument(ctx, &firestorepb.CreateDocumentRequest{
		Parent:       testParent,
		CollectionId: "users",
		DocumentId:   "charlie",
		Document: &firestorepb.Document{
			Fields: map[string]*firestorepb.Value{"name": strVal("Charlie")},
		},
	})
	if err != nil {
		t.Fatalf("CreateDocument: %v", err)
	}

	// Delete.
	_, err = client.DeleteDocument(ctx, &firestorepb.DeleteDocumentRequest{Name: name})
	if err != nil {
		t.Fatalf("DeleteDocument: %v", err)
	}

	// Get should return NotFound.
	_, err = client.GetDocument(ctx, &firestorepb.GetDocumentRequest{Name: name})
	if status.Code(err) != codes.NotFound {
		t.Errorf("GetDocument after delete: got %v, want NotFound", err)
	}
}

func TestGetNonexistent(t *testing.T) {
	client := testClient(t)
	ctx := context.Background()

	_, err := client.GetDocument(ctx, &firestorepb.GetDocumentRequest{
		Name: testParent + "/users/nonexistent",
	})
	if status.Code(err) != codes.NotFound {
		t.Errorf("got %v, want NotFound", err)
	}
}

func TestDeleteNonexistent(t *testing.T) {
	client := testClient(t)
	ctx := context.Background()

	_, err := client.DeleteDocument(ctx, &firestorepb.DeleteDocumentRequest{
		Name: testParent + "/users/nonexistent",
	})
	if status.Code(err) != codes.NotFound {
		t.Errorf("got %v, want NotFound", err)
	}
}

func TestListDocuments(t *testing.T) {
	client := testClient(t)
	ctx := context.Background()

	// Create several documents.
	for _, id := range []string{"d1", "d2", "d3"} {
		_, err := client.CreateDocument(ctx, &firestorepb.CreateDocumentRequest{
			Parent:       testParent,
			CollectionId: "items",
			DocumentId:   id,
			Document: &firestorepb.Document{
				Fields: map[string]*firestorepb.Value{"id": strVal(id)},
			},
		})
		if err != nil {
			t.Fatalf("CreateDocument(%s): %v", id, err)
		}
	}

	resp, err := client.ListDocuments(ctx, &firestorepb.ListDocumentsRequest{
		Parent:       testParent,
		CollectionId: "items",
	})
	if err != nil {
		t.Fatalf("ListDocuments: %v", err)
	}
	if len(resp.Documents) != 3 {
		t.Errorf("got %d documents, want 3", len(resp.Documents))
	}
}

func TestRunQueryEquality(t *testing.T) {
	client := testClient(t)
	ctx := context.Background()

	// Create documents.
	for _, u := range []struct {
		id, city string
	}{
		{"u1", "NYC"},
		{"u2", "LA"},
		{"u3", "NYC"},
	} {
		_, err := client.CreateDocument(ctx, &firestorepb.CreateDocumentRequest{
			Parent:       testParent,
			CollectionId: "people",
			DocumentId:   u.id,
			Document: &firestorepb.Document{
				Fields: map[string]*firestorepb.Value{
					"city": strVal(u.city),
				},
			},
		})
		if err != nil {
			t.Fatalf("CreateDocument: %v", err)
		}
	}

	stream, err := client.RunQuery(ctx, &firestorepb.RunQueryRequest{
		Parent: testParent,
		QueryType: &firestorepb.RunQueryRequest_StructuredQuery{
			StructuredQuery: &firestorepb.StructuredQuery{
				From: []*firestorepb.StructuredQuery_CollectionSelector{
					{CollectionId: "people"},
				},
				Where: &firestorepb.StructuredQuery_Filter{
					FilterType: &firestorepb.StructuredQuery_Filter_FieldFilter{
						FieldFilter: &firestorepb.StructuredQuery_FieldFilter{
							Field: &firestorepb.StructuredQuery_FieldReference{FieldPath: "city"},
							Op:    firestorepb.StructuredQuery_FieldFilter_EQUAL,
							Value: strVal("NYC"),
						},
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("RunQuery: %v", err)
	}

	var results []*firestorepb.Document
	for {
		resp, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Recv: %v", err)
		}
		if resp.Document != nil {
			results = append(results, resp.Document)
		}
	}

	if len(results) != 2 {
		t.Errorf("got %d results, want 2", len(results))
	}
}

func TestRunQueryRangeFilter(t *testing.T) {
	client := testClient(t)
	ctx := context.Background()

	// Create documents with numeric scores.
	for i := int64(1); i <= 5; i++ {
		_, err := client.CreateDocument(ctx, &firestorepb.CreateDocumentRequest{
			Parent:       testParent,
			CollectionId: "scores",
			DocumentId:   fmt.Sprintf("s%d", i),
			Document: &firestorepb.Document{
				Fields: map[string]*firestorepb.Value{
					"score": intVal(i * 10),
				},
			},
		})
		if err != nil {
			t.Fatalf("CreateDocument: %v", err)
		}
	}

	// Query: score >= 30
	stream, err := client.RunQuery(ctx, &firestorepb.RunQueryRequest{
		Parent: testParent,
		QueryType: &firestorepb.RunQueryRequest_StructuredQuery{
			StructuredQuery: &firestorepb.StructuredQuery{
				From: []*firestorepb.StructuredQuery_CollectionSelector{
					{CollectionId: "scores"},
				},
				Where: &firestorepb.StructuredQuery_Filter{
					FilterType: &firestorepb.StructuredQuery_Filter_FieldFilter{
						FieldFilter: &firestorepb.StructuredQuery_FieldFilter{
							Field: &firestorepb.StructuredQuery_FieldReference{FieldPath: "score"},
							Op:    firestorepb.StructuredQuery_FieldFilter_GREATER_THAN_OR_EQUAL,
							Value: intVal(30),
						},
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("RunQuery: %v", err)
	}

	var results []*firestorepb.Document
	for {
		resp, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Recv: %v", err)
		}
		if resp.Document != nil {
			results = append(results, resp.Document)
		}
	}

	if len(results) != 3 {
		t.Errorf("got %d results, want 3 (scores 30, 40, 50)", len(results))
	}
}

func TestRunQueryOrderByAndLimit(t *testing.T) {
	client := testClient(t)
	ctx := context.Background()

	// Create documents.
	for _, item := range []struct {
		id    string
		price int64
	}{
		{"p1", 100},
		{"p2", 50},
		{"p3", 200},
		{"p4", 75},
	} {
		_, err := client.CreateDocument(ctx, &firestorepb.CreateDocumentRequest{
			Parent:       testParent,
			CollectionId: "products",
			DocumentId:   item.id,
			Document: &firestorepb.Document{
				Fields: map[string]*firestorepb.Value{
					"price": intVal(item.price),
				},
			},
		})
		if err != nil {
			t.Fatalf("CreateDocument: %v", err)
		}
	}

	// Query: ORDER BY price ASC, LIMIT 2
	stream, err := client.RunQuery(ctx, &firestorepb.RunQueryRequest{
		Parent: testParent,
		QueryType: &firestorepb.RunQueryRequest_StructuredQuery{
			StructuredQuery: &firestorepb.StructuredQuery{
				From: []*firestorepb.StructuredQuery_CollectionSelector{
					{CollectionId: "products"},
				},
				OrderBy: []*firestorepb.StructuredQuery_Order{
					{
						Field:     &firestorepb.StructuredQuery_FieldReference{FieldPath: "price"},
						Direction: firestorepb.StructuredQuery_ASCENDING,
					},
				},
				Limit: &wrapperspb.Int32Value{Value: 2},
			},
		},
	})
	if err != nil {
		t.Fatalf("RunQuery: %v", err)
	}

	var results []*firestorepb.Document
	for {
		resp, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Recv: %v", err)
		}
		if resp.Document != nil {
			results = append(results, resp.Document)
		}
	}

	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}
	// First should be price=50, second price=75.
	if results[0].Fields["price"].GetIntegerValue() != 50 {
		t.Errorf("first price = %d, want 50", results[0].Fields["price"].GetIntegerValue())
	}
	if results[1].Fields["price"].GetIntegerValue() != 75 {
		t.Errorf("second price = %d, want 75", results[1].Fields["price"].GetIntegerValue())
	}
}

func TestBeginTransactionAndCommit(t *testing.T) {
	client := testClient(t)
	ctx := context.Background()

	// Begin transaction.
	txResp, err := client.BeginTransaction(ctx, &firestorepb.BeginTransactionRequest{
		Database: testDB,
	})
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}
	if len(txResp.Transaction) == 0 {
		t.Fatal("empty transaction ID")
	}

	// Commit with writes.
	docName := testParent + "/tx_items/item1"
	commitResp, err := client.Commit(ctx, &firestorepb.CommitRequest{
		Database:    testDB,
		Transaction: txResp.Transaction,
		Writes: []*firestorepb.Write{
			{
				Operation: &firestorepb.Write_Update{
					Update: &firestorepb.Document{
						Name: docName,
						Fields: map[string]*firestorepb.Value{
							"status": strVal("created"),
						},
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if len(commitResp.WriteResults) != 1 {
		t.Errorf("got %d write results, want 1", len(commitResp.WriteResults))
	}
	if commitResp.CommitTime == nil {
		t.Error("missing commit time")
	}

	// Verify the document was created.
	got, err := client.GetDocument(ctx, &firestorepb.GetDocumentRequest{Name: docName})
	if err != nil {
		t.Fatalf("GetDocument: %v", err)
	}
	if got.Fields["status"].GetStringValue() != "created" {
		t.Error("document not created by commit")
	}
}

func TestRollback(t *testing.T) {
	client := testClient(t)
	ctx := context.Background()

	txResp, err := client.BeginTransaction(ctx, &firestorepb.BeginTransactionRequest{
		Database: testDB,
	})
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}

	_, err = client.Rollback(ctx, &firestorepb.RollbackRequest{
		Database:    testDB,
		Transaction: txResp.Transaction,
	})
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	// Using the same transaction for commit should fail.
	_, err = client.Commit(ctx, &firestorepb.CommitRequest{
		Database:    testDB,
		Transaction: txResp.Transaction,
	})
	if err == nil {
		t.Error("expected error committing rolled-back transaction")
	}
}

func TestNestedCollections(t *testing.T) {
	client := testClient(t)
	ctx := context.Background()

	// Create parent document.
	_, err := client.CreateDocument(ctx, &firestorepb.CreateDocumentRequest{
		Parent:       testParent,
		CollectionId: "chats",
		DocumentId:   "chat1",
		Document: &firestorepb.Document{
			Fields: map[string]*firestorepb.Value{
				"title": strVal("General"),
			},
		},
	})
	if err != nil {
		t.Fatalf("CreateDocument (parent): %v", err)
	}

	// Create nested document: chats/chat1/messages/msg1
	nestedParent := testParent + "/chats/chat1"
	_, err = client.CreateDocument(ctx, &firestorepb.CreateDocumentRequest{
		Parent:       nestedParent,
		CollectionId: "messages",
		DocumentId:   "msg1",
		Document: &firestorepb.Document{
			Fields: map[string]*firestorepb.Value{
				"text": strVal("Hello, world!"),
			},
		},
	})
	if err != nil {
		t.Fatalf("CreateDocument (nested): %v", err)
	}

	// Get the nested document.
	nestedName := nestedParent + "/messages/msg1"
	got, err := client.GetDocument(ctx, &firestorepb.GetDocumentRequest{Name: nestedName})
	if err != nil {
		t.Fatalf("GetDocument (nested): %v", err)
	}
	if got.Fields["text"].GetStringValue() != "Hello, world!" {
		t.Error("nested document field mismatch")
	}

	// List nested collection.
	listResp, err := client.ListDocuments(ctx, &firestorepb.ListDocumentsRequest{
		Parent:       nestedParent,
		CollectionId: "messages",
	})
	if err != nil {
		t.Fatalf("ListDocuments (nested): %v", err)
	}
	if len(listResp.Documents) != 1 {
		t.Errorf("got %d nested docs, want 1", len(listResp.Documents))
	}
}

func TestCommitDeleteWrite(t *testing.T) {
	client := testClient(t)
	ctx := context.Background()

	docName := testParent + "/del_test/doc1"

	// Create via commit.
	_, err := client.Commit(ctx, &firestorepb.CommitRequest{
		Database: testDB,
		Writes: []*firestorepb.Write{
			{
				Operation: &firestorepb.Write_Update{
					Update: &firestorepb.Document{
						Name:   docName,
						Fields: map[string]*firestorepb.Value{"v": intVal(1)},
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("Commit (create): %v", err)
	}

	// Verify exists.
	_, err = client.GetDocument(ctx, &firestorepb.GetDocumentRequest{Name: docName})
	if err != nil {
		t.Fatalf("GetDocument: %v", err)
	}

	// Delete via commit.
	_, err = client.Commit(ctx, &firestorepb.CommitRequest{
		Database: testDB,
		Writes: []*firestorepb.Write{
			{
				Operation: &firestorepb.Write_Delete{Delete: docName},
			},
		},
	})
	if err != nil {
		t.Fatalf("Commit (delete): %v", err)
	}

	// Verify gone.
	_, err = client.GetDocument(ctx, &firestorepb.GetDocumentRequest{Name: docName})
	if status.Code(err) != codes.NotFound {
		t.Errorf("got %v, want NotFound", err)
	}
}

func TestCreateDuplicate(t *testing.T) {
	client := testClient(t)
	ctx := context.Background()

	req := &firestorepb.CreateDocumentRequest{
		Parent:       testParent,
		CollectionId: "dupes",
		DocumentId:   "same",
		Document: &firestorepb.Document{
			Fields: map[string]*firestorepb.Value{"x": intVal(1)},
		},
	}

	_, err := client.CreateDocument(ctx, req)
	if err != nil {
		t.Fatalf("first create: %v", err)
	}

	_, err = client.CreateDocument(ctx, req)
	if status.Code(err) != codes.AlreadyExists {
		t.Errorf("second create: got %v, want AlreadyExists", err)
	}
}
