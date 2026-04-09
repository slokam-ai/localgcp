package cloudrun

import (
	"context"
	"net"
	"testing"

	"cloud.google.com/go/run/apiv2/runpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func testClient(t *testing.T) (runpb.ServicesClient, func()) {
	t.Helper()
	svc := New("", true)
	ln, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := grpc.NewServer()
	runpb.RegisterServicesServer(srv, svc)
	go srv.Serve(ln)

	conn, err := grpc.NewClient(ln.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		srv.Stop()
		t.Fatalf("dial: %v", err)
	}
	client := runpb.NewServicesClient(conn)
	return client, func() { conn.Close(); srv.Stop() }
}

func TestServiceCRUD(t *testing.T) {
	client, cleanup := testClient(t)
	defer cleanup()
	ctx := context.Background()

	parent := "projects/test/locations/us-central1"

	// Create.
	op, err := client.CreateService(ctx, &runpb.CreateServiceRequest{
		Parent:    parent,
		ServiceId: "my-svc",
		Service: &runpb.Service{
			Template: &runpb.RevisionTemplate{
				Containers: []*runpb.Container{{
					Image: "gcr.io/test/myimage:latest",
				}},
			},
		},
	})
	if err != nil {
		t.Fatalf("CreateService: %v", err)
	}
	if !op.Done {
		t.Fatal("expected completed operation")
	}

	// Extract service from operation.
	var created runpb.Service
	if err := op.GetResponse().UnmarshalTo(&created); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if created.Name != parent+"/services/my-svc" {
		t.Fatalf("wrong name: %s", created.Name)
	}
	if created.Uri == "" {
		t.Fatal("expected URI")
	}

	// Get.
	svc, err := client.GetService(ctx, &runpb.GetServiceRequest{Name: created.Name})
	if err != nil {
		t.Fatalf("GetService: %v", err)
	}
	if svc.Name != created.Name {
		t.Fatal("get returned wrong name")
	}

	// List.
	listResp, err := client.ListServices(ctx, &runpb.ListServicesRequest{Parent: parent})
	if err != nil {
		t.Fatalf("ListServices: %v", err)
	}
	if len(listResp.Services) != 1 {
		t.Fatalf("expected 1 service, got %d", len(listResp.Services))
	}

	// Delete.
	delOp, err := client.DeleteService(ctx, &runpb.DeleteServiceRequest{Name: created.Name})
	if err != nil {
		t.Fatalf("DeleteService: %v", err)
	}
	if !delOp.Done {
		t.Fatal("expected completed delete operation")
	}

	// Verify gone.
	_, err = client.GetService(ctx, &runpb.GetServiceRequest{Name: created.Name})
	if err == nil {
		t.Fatal("expected not found after delete")
	}
}

func TestDuplicateServiceFails(t *testing.T) {
	client, cleanup := testClient(t)
	defer cleanup()
	ctx := context.Background()

	parent := "projects/test/locations/us-central1"
	req := &runpb.CreateServiceRequest{
		Parent: parent, ServiceId: "dup",
		Service: &runpb.Service{},
	}

	_, err := client.CreateService(ctx, req)
	if err != nil {
		t.Fatalf("first create: %v", err)
	}

	_, err = client.CreateService(ctx, req)
	if err == nil {
		t.Fatal("expected error on duplicate create")
	}
}
