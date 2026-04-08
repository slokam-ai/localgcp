package firestore

import (
	"context"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"cloud.google.com/go/firestore/apiv1/firestorepb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// listenTestClient starts a Firestore service and returns a connected client + cleanup.
func listenTestClient(t *testing.T) (firestorepb.FirestoreClient, *Service, func()) {
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

	client := firestorepb.NewFirestoreClient(conn)
	cleanup := func() {
		conn.Close()
		srv.Stop()
	}
	return client, svc, cleanup
}

func TestListenCollectionInitialSnapshot(t *testing.T) {
	client, svc, cleanup := listenTestClient(t)
	defer cleanup()

	parent := "projects/test/databases/(default)/documents"

	// Pre-populate two documents.
	svc.store.CreateDocument(parent+"/users/alice", map[string]*firestorepb.Value{
		"name": strVal("alice"),
	})
	svc.store.CreateDocument(parent+"/users/bob", map[string]*firestorepb.Value{
		"name": strVal("bob"),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stream, err := client.Listen(ctx)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}

	// Send add_target for the "users" collection.
	err = stream.Send(&firestorepb.ListenRequest{
		Database: "projects/test/databases/(default)",
		TargetChange: &firestorepb.ListenRequest_AddTarget{
			AddTarget: &firestorepb.Target{
				TargetId: 1,
				TargetType: &firestorepb.Target_Query{
					Query: &firestorepb.Target_QueryTarget{
						Parent: parent,
						QueryType: &firestorepb.Target_QueryTarget_StructuredQuery{
							StructuredQuery: &firestorepb.StructuredQuery{
								From: []*firestorepb.StructuredQuery_CollectionSelector{
									{CollectionId: "users"},
								},
							},
						},
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}

	// Expect: ADD target change, 2 document changes, CURRENT target change.
	responses := recvN(t, stream, 4, 3*time.Second)

	// First: TargetChange ADD.
	tc := responses[0].GetTargetChange()
	if tc == nil || tc.TargetChangeType != firestorepb.TargetChange_ADD {
		t.Fatalf("expected TargetChange ADD, got %v", responses[0])
	}

	// Middle two: DocumentChange (alice, bob sorted).
	dc1 := responses[1].GetDocumentChange()
	dc2 := responses[2].GetDocumentChange()
	if dc1 == nil || dc2 == nil {
		t.Fatalf("expected DocumentChange, got %v and %v", responses[1], responses[2])
	}
	if dc1.Document.Fields["name"].GetStringValue() != "alice" {
		t.Fatalf("expected alice first, got %s", dc1.Document.Fields["name"].GetStringValue())
	}
	if dc2.Document.Fields["name"].GetStringValue() != "bob" {
		t.Fatalf("expected bob second, got %s", dc2.Document.Fields["name"].GetStringValue())
	}

	// Last: TargetChange CURRENT.
	tc = responses[3].GetTargetChange()
	if tc == nil || tc.TargetChangeType != firestorepb.TargetChange_CURRENT {
		t.Fatalf("expected TargetChange CURRENT, got %v", responses[3])
	}
}

func TestListenCollectionRealTimeUpdates(t *testing.T) {
	client, svc, cleanup := listenTestClient(t)
	defer cleanup()

	parent := "projects/test/databases/(default)/documents"

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stream, err := client.Listen(ctx)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}

	// Add target for empty "items" collection.
	err = stream.Send(&firestorepb.ListenRequest{
		Database: "projects/test/databases/(default)",
		TargetChange: &firestorepb.ListenRequest_AddTarget{
			AddTarget: &firestorepb.Target{
				TargetId: 1,
				TargetType: &firestorepb.Target_Query{
					Query: &firestorepb.Target_QueryTarget{
						Parent: parent,
						QueryType: &firestorepb.Target_QueryTarget_StructuredQuery{
							StructuredQuery: &firestorepb.StructuredQuery{
								From: []*firestorepb.StructuredQuery_CollectionSelector{
									{CollectionId: "items"},
								},
							},
						},
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}

	// Expect ADD + CURRENT (empty collection).
	responses := recvN(t, stream, 2, 3*time.Second)
	if responses[0].GetTargetChange().GetTargetChangeType() != firestorepb.TargetChange_ADD {
		t.Fatal("expected ADD")
	}
	if responses[1].GetTargetChange().GetTargetChangeType() != firestorepb.TargetChange_CURRENT {
		t.Fatal("expected CURRENT")
	}

	// Now create a document — should trigger a real-time event.
	svc.store.CreateDocument(parent+"/items/item1", map[string]*firestorepb.Value{
		"title": strVal("first item"),
	})

	// Expect DocumentChange for the new document.
	responses = recvN(t, stream, 1, 3*time.Second)
	dc := responses[0].GetDocumentChange()
	if dc == nil {
		t.Fatalf("expected DocumentChange, got %v", responses[0])
	}
	if dc.Document.Fields["title"].GetStringValue() != "first item" {
		t.Fatal("wrong document content")
	}

	// Update the document — should trigger MODIFIED.
	svc.store.UpdateDocument(parent+"/items/item1", map[string]*firestorepb.Value{
		"title": strVal("updated item"),
	}, nil)

	responses = recvN(t, stream, 1, 3*time.Second)
	dc = responses[0].GetDocumentChange()
	if dc == nil {
		t.Fatalf("expected DocumentChange, got %v", responses[0])
	}
	if dc.Document.Fields["title"].GetStringValue() != "updated item" {
		t.Fatal("wrong updated content")
	}

	// Delete the document — should trigger DocumentDelete.
	svc.store.DeleteDocument(parent + "/items/item1")

	responses = recvN(t, stream, 1, 3*time.Second)
	dd := responses[0].GetDocumentDelete()
	if dd == nil {
		t.Fatalf("expected DocumentDelete, got %v", responses[0])
	}
	if dd.Document != parent+"/items/item1" {
		t.Fatalf("wrong deleted document: %s", dd.Document)
	}
}

func TestListenDocumentTarget(t *testing.T) {
	client, svc, cleanup := listenTestClient(t)
	defer cleanup()

	parent := "projects/test/databases/(default)/documents"
	docPath := parent + "/config/settings"

	// Pre-create document.
	svc.store.CreateDocument(docPath, map[string]*firestorepb.Value{
		"theme": strVal("dark"),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stream, err := client.Listen(ctx)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}

	// Add target for specific document.
	err = stream.Send(&firestorepb.ListenRequest{
		Database: "projects/test/databases/(default)",
		TargetChange: &firestorepb.ListenRequest_AddTarget{
			AddTarget: &firestorepb.Target{
				TargetId: 42,
				TargetType: &firestorepb.Target_Documents{
					Documents: &firestorepb.Target_DocumentsTarget{
						Documents: []string{docPath},
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}

	// Expect: ADD, DocumentChange, CURRENT.
	responses := recvN(t, stream, 3, 3*time.Second)
	if responses[0].GetTargetChange().GetTargetChangeType() != firestorepb.TargetChange_ADD {
		t.Fatal("expected ADD")
	}
	dc := responses[1].GetDocumentChange()
	if dc == nil || dc.Document.Fields["theme"].GetStringValue() != "dark" {
		t.Fatal("expected document with theme=dark")
	}
	if responses[2].GetTargetChange().GetTargetChangeType() != firestorepb.TargetChange_CURRENT {
		t.Fatal("expected CURRENT")
	}
}

func TestListenRemoveTarget(t *testing.T) {
	client, _, cleanup := listenTestClient(t)
	defer cleanup()

	parent := "projects/test/databases/(default)/documents"

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stream, err := client.Listen(ctx)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}

	// Add target.
	err = stream.Send(&firestorepb.ListenRequest{
		Database: "projects/test/databases/(default)",
		TargetChange: &firestorepb.ListenRequest_AddTarget{
			AddTarget: &firestorepb.Target{
				TargetId: 1,
				TargetType: &firestorepb.Target_Query{
					Query: &firestorepb.Target_QueryTarget{
						Parent: parent,
						QueryType: &firestorepb.Target_QueryTarget_StructuredQuery{
							StructuredQuery: &firestorepb.StructuredQuery{
								From: []*firestorepb.StructuredQuery_CollectionSelector{
									{CollectionId: "col"},
								},
							},
						},
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}

	// Wait for ADD + CURRENT.
	recvN(t, stream, 2, 3*time.Second)

	// Remove target.
	err = stream.Send(&firestorepb.ListenRequest{
		Database: "projects/test/databases/(default)",
		TargetChange: &firestorepb.ListenRequest_RemoveTarget{
			RemoveTarget: 1,
		},
	})
	if err != nil {
		t.Fatalf("Send remove: %v", err)
	}

	// Expect REMOVE.
	responses := recvN(t, stream, 1, 3*time.Second)
	tc := responses[0].GetTargetChange()
	if tc == nil || tc.TargetChangeType != firestorepb.TargetChange_REMOVE {
		t.Fatalf("expected TargetChange REMOVE, got %v", responses[0])
	}
}

func TestListenIgnoresUnrelatedCollections(t *testing.T) {
	client, svc, cleanup := listenTestClient(t)
	defer cleanup()

	parent := "projects/test/databases/(default)/documents"

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stream, err := client.Listen(ctx)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}

	// Listen on "users" collection.
	err = stream.Send(&firestorepb.ListenRequest{
		Database: "projects/test/databases/(default)",
		TargetChange: &firestorepb.ListenRequest_AddTarget{
			AddTarget: &firestorepb.Target{
				TargetId: 1,
				TargetType: &firestorepb.Target_Query{
					Query: &firestorepb.Target_QueryTarget{
						Parent: parent,
						QueryType: &firestorepb.Target_QueryTarget_StructuredQuery{
							StructuredQuery: &firestorepb.StructuredQuery{
								From: []*firestorepb.StructuredQuery_CollectionSelector{
									{CollectionId: "users"},
								},
							},
						},
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}

	recvN(t, stream, 2, 3*time.Second) // ADD + CURRENT

	// Create document in a DIFFERENT collection.
	svc.store.CreateDocument(parent+"/posts/post1", map[string]*firestorepb.Value{
		"title": strVal("hello"),
	})

	// Should NOT receive any event. Wait briefly and check.
	time.Sleep(200 * time.Millisecond)
	resp, err := recvWithTimeout(stream, 300*time.Millisecond)
	if err == nil {
		t.Fatalf("expected no event for unrelated collection, got %v", resp)
	}
}

func TestListenMultipleClients(t *testing.T) {
	client, svc, cleanup := listenTestClient(t)
	defer cleanup()

	parent := "projects/test/databases/(default)/documents"

	// Start two listeners on the same collection.
	ctx1, cancel1 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel1()
	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()

	stream1, _ := client.Listen(ctx1)
	stream2, _ := client.Listen(ctx2)

	addTarget := func(s firestorepb.Firestore_ListenClient, tid int32) {
		s.Send(&firestorepb.ListenRequest{
			Database: "projects/test/databases/(default)",
			TargetChange: &firestorepb.ListenRequest_AddTarget{
				AddTarget: &firestorepb.Target{
					TargetId: tid,
					TargetType: &firestorepb.Target_Query{
						Query: &firestorepb.Target_QueryTarget{
							Parent: parent,
							QueryType: &firestorepb.Target_QueryTarget_StructuredQuery{
								StructuredQuery: &firestorepb.StructuredQuery{
									From: []*firestorepb.StructuredQuery_CollectionSelector{
										{CollectionId: "shared"},
									},
								},
							},
						},
					},
				},
			},
		})
	}

	addTarget(stream1, 1)
	addTarget(stream2, 2)

	recvN(t, stream1, 2, 3*time.Second) // ADD + CURRENT
	recvN(t, stream2, 2, 3*time.Second)

	// Create a document. Both listeners should get it.
	svc.store.CreateDocument(parent+"/shared/doc1", map[string]*firestorepb.Value{
		"v": strVal("hello"),
	})

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		r := recvN(t, stream1, 1, 3*time.Second)
		if r[0].GetDocumentChange() == nil {
			t.Error("stream1: expected DocumentChange")
		}
	}()
	go func() {
		defer wg.Done()
		r := recvN(t, stream2, 1, 3*time.Second)
		if r[0].GetDocumentChange() == nil {
			t.Error("stream2: expected DocumentChange")
		}
	}()
	wg.Wait()
}

// --- helpers ---

func recvN(t *testing.T, stream firestorepb.Firestore_ListenClient, n int, timeout time.Duration) []*firestorepb.ListenResponse {
	t.Helper()
	var responses []*firestorepb.ListenResponse
	deadline := time.After(timeout)
	for i := 0; i < n; i++ {
		type result struct {
			resp *firestorepb.ListenResponse
			err  error
		}
		ch := make(chan result, 1)
		go func() {
			resp, err := stream.Recv()
			ch <- result{resp, err}
		}()
		select {
		case r := <-ch:
			if r.err != nil {
				t.Fatalf("Recv %d/%d: %v", i+1, n, r.err)
			}
			responses = append(responses, r.resp)
		case <-deadline:
			t.Fatalf("timeout waiting for response %d/%d", i+1, n)
		}
	}
	return responses
}

func recvWithTimeout(stream firestorepb.Firestore_ListenClient, timeout time.Duration) (*firestorepb.ListenResponse, error) {
	type result struct {
		resp *firestorepb.ListenResponse
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		resp, err := stream.Recv()
		ch <- result{resp, err}
	}()
	select {
	case r := <-ch:
		return r.resp, r.err
	case <-time.After(timeout):
		return nil, io.ErrUnexpectedEOF
	}
}

// Suppress unused import warning for wrapperspb in this file.
var _ = wrapperspb.Int32Value{}
