package firestore

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"cloud.google.com/go/firestore/apiv1/firestorepb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/reflection"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Service implements the Firestore emulator.
type Service struct {
	dataDir string
	quiet   bool
	logger  *log.Logger
	store   *Store

	txMu         sync.Mutex
	transactions map[string]bool // active transaction IDs
}

// New creates a new Firestore service.
func New(dataDir string, quiet bool) *Service {
	logger := log.New(os.Stderr, "[firestore] ", log.LstdFlags)
	return &Service{
		dataDir:      dataDir,
		quiet:        quiet,
		logger:       logger,
		store:        NewStore(dataDir),
		transactions: make(map[string]bool),
	}
}

func (s *Service) Name() string { return "Firestore" }

func (s *Service) Start(ctx context.Context, addr string) error {
	srv := grpc.NewServer(
		grpc.UnaryInterceptor(s.loggingInterceptor),
		grpc.StreamInterceptor(s.streamLoggingInterceptor),
	)
	firestorepb.RegisterFirestoreServer(srv, &firestoreServer{svc: s})
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

func (s *Service) streamLoggingInterceptor(
	srv interface{},
	ss grpc.ServerStream,
	info *grpc.StreamServerInfo,
	handler grpc.StreamHandler,
) error {
	err := handler(srv, ss)
	if !s.quiet {
		code := codes.OK
		if err != nil {
			code = status.Code(err)
		}
		s.logger.Printf("%s %s", info.FullMethod, code)
	}
	return err
}

// --- gRPC server implementation ---

type firestoreServer struct {
	firestorepb.UnimplementedFirestoreServer
	svc *Service
}

// GetDocument returns a single document by name.
func (fs *firestoreServer) GetDocument(_ context.Context, req *firestorepb.GetDocumentRequest) (*firestorepb.Document, error) {
	name := req.GetName()
	if name == "" {
		return nil, status.Errorf(codes.InvalidArgument, "document name is required")
	}

	doc := fs.svc.store.GetDocument(name)
	if doc == nil {
		return nil, status.Errorf(codes.NotFound, "document %q not found", name)
	}
	return doc, nil
}

// CreateDocument creates a new document in a collection.
func (fs *firestoreServer) CreateDocument(_ context.Context, req *firestorepb.CreateDocumentRequest) (*firestorepb.Document, error) {
	parent := req.GetParent()
	collectionID := req.GetCollectionId()
	docID := req.GetDocumentId()

	if parent == "" || collectionID == "" {
		return nil, status.Errorf(codes.InvalidArgument, "parent and collection_id are required")
	}

	if docID == "" {
		docID = generateID()
	}

	name := parent + "/" + collectionID + "/" + docID

	// Check if document already exists.
	if fs.svc.store.GetDocument(name) != nil {
		return nil, status.Errorf(codes.AlreadyExists, "document %q already exists", name)
	}

	var fields map[string]*firestorepb.Value
	if req.GetDocument() != nil {
		fields = req.GetDocument().GetFields()
	}

	doc := fs.svc.store.CreateDocument(name, fields)
	return doc, nil
}

// UpdateDocument updates (or creates) a document.
func (fs *firestoreServer) UpdateDocument(_ context.Context, req *firestorepb.UpdateDocumentRequest) (*firestorepb.Document, error) {
	docPB := req.GetDocument()
	if docPB == nil || docPB.GetName() == "" {
		return nil, status.Errorf(codes.InvalidArgument, "document with name is required")
	}

	var mask []string
	if req.GetUpdateMask() != nil {
		mask = req.GetUpdateMask().GetFieldPaths()
	}

	doc := fs.svc.store.UpdateDocument(docPB.GetName(), docPB.GetFields(), mask)
	return doc, nil
}

// DeleteDocument removes a document.
func (fs *firestoreServer) DeleteDocument(_ context.Context, req *firestorepb.DeleteDocumentRequest) (*emptypb.Empty, error) {
	name := req.GetName()
	if name == "" {
		return nil, status.Errorf(codes.InvalidArgument, "document name is required")
	}

	if !fs.svc.store.DeleteDocument(name) {
		return nil, status.Errorf(codes.NotFound, "document %q not found", name)
	}
	return &emptypb.Empty{}, nil
}

// ListDocuments lists documents in a collection.
func (fs *firestoreServer) ListDocuments(_ context.Context, req *firestorepb.ListDocumentsRequest) (*firestorepb.ListDocumentsResponse, error) {
	parent := req.GetParent()
	collectionID := req.GetCollectionId()

	if parent == "" || collectionID == "" {
		return nil, status.Errorf(codes.InvalidArgument, "parent and collection_id are required")
	}

	docs := fs.svc.store.ListDocuments(parent, collectionID)

	// Apply page_size.
	if req.GetPageSize() > 0 && int(req.GetPageSize()) < len(docs) {
		docs = docs[:req.GetPageSize()]
	}

	return &firestorepb.ListDocumentsResponse{
		Documents: docs,
	}, nil
}

// RunQuery executes a structured query (server-streaming RPC).
func (fs *firestoreServer) RunQuery(req *firestorepb.RunQueryRequest, stream firestorepb.Firestore_RunQueryServer) error {
	parent := req.GetParent()
	sq := req.GetStructuredQuery()
	if sq == nil {
		return status.Errorf(codes.InvalidArgument, "structured_query is required")
	}

	docs := executeQuery(fs.svc.store, parent, sq)

	now := timestamppb.Now()
	for _, doc := range docs {
		resp := &firestorepb.RunQueryResponse{
			Document: doc,
			ReadTime: now,
		}
		if err := stream.Send(resp); err != nil {
			return err
		}
	}

	// If no results, send an empty response with read_time per Firestore protocol.
	if len(docs) == 0 {
		if err := stream.Send(&firestorepb.RunQueryResponse{ReadTime: now}); err != nil {
			return err
		}
	}

	return nil
}

// BeginTransaction starts a new transaction.
func (fs *firestoreServer) BeginTransaction(_ context.Context, _ *firestorepb.BeginTransactionRequest) (*firestorepb.BeginTransactionResponse, error) {
	txID := generateTxID()

	fs.svc.txMu.Lock()
	fs.svc.transactions[txID] = true
	fs.svc.txMu.Unlock()

	return &firestorepb.BeginTransactionResponse{
		Transaction: []byte(txID),
	}, nil
}

// Commit applies writes atomically.
func (fs *firestoreServer) Commit(_ context.Context, req *firestorepb.CommitRequest) (*firestorepb.CommitResponse, error) {
	// If a transaction is specified, validate and remove it.
	if len(req.GetTransaction()) > 0 {
		txID := string(req.GetTransaction())
		fs.svc.txMu.Lock()
		_, ok := fs.svc.transactions[txID]
		if ok {
			delete(fs.svc.transactions, txID)
		}
		fs.svc.txMu.Unlock()

		if !ok {
			return nil, status.Errorf(codes.InvalidArgument, "transaction %q not found or already committed", txID)
		}
	}

	now := timestamppb.Now()
	var results []*firestorepb.WriteResult

	for _, w := range req.GetWrites() {
		wr, err := fs.applyWrite(w, now)
		if err != nil {
			return nil, err
		}
		results = append(results, wr)
	}

	return &firestorepb.CommitResponse{
		WriteResults: results,
		CommitTime:   now,
	}, nil
}

// Rollback aborts a transaction.
func (fs *firestoreServer) Rollback(_ context.Context, req *firestorepb.RollbackRequest) (*emptypb.Empty, error) {
	txID := string(req.GetTransaction())
	if txID == "" {
		return nil, status.Errorf(codes.InvalidArgument, "transaction is required")
	}

	fs.svc.txMu.Lock()
	_, ok := fs.svc.transactions[txID]
	if ok {
		delete(fs.svc.transactions, txID)
	}
	fs.svc.txMu.Unlock()

	if !ok {
		return nil, status.Errorf(codes.InvalidArgument, "transaction %q not found or already rolled back", txID)
	}

	return &emptypb.Empty{}, nil
}

// --- Write application ---

func (fs *firestoreServer) applyWrite(w *firestorepb.Write, now *timestamppb.Timestamp) (*firestorepb.WriteResult, error) {
	switch op := w.GetOperation().(type) {
	case *firestorepb.Write_Update:
		doc := op.Update
		var mask []string
		if w.GetUpdateMask() != nil {
			mask = w.GetUpdateMask().GetFieldPaths()
		}
		fs.svc.store.UpdateDocument(doc.GetName(), doc.GetFields(), mask)
		return &firestorepb.WriteResult{UpdateTime: now}, nil

	case *firestorepb.Write_Delete:
		name := op.Delete
		fs.svc.store.DeleteDocument(name)
		return &firestorepb.WriteResult{}, nil

	case *firestorepb.Write_Transform:
		// For MVP, transforms are acknowledged but not deeply implemented.
		return &firestorepb.WriteResult{UpdateTime: now}, nil

	default:
		return &firestorepb.WriteResult{UpdateTime: now}, nil
	}
}

// --- Unimplemented streaming RPCs ---

func (fs *firestoreServer) BatchGetDocuments(req *firestorepb.BatchGetDocumentsRequest, stream firestorepb.Firestore_BatchGetDocumentsServer) error {
	now := timestamppb.Now()

	for _, name := range req.GetDocuments() {
		doc := fs.svc.store.GetDocument(name)
		if doc != nil {
			if err := stream.Send(&firestorepb.BatchGetDocumentsResponse{
				Result:   &firestorepb.BatchGetDocumentsResponse_Found{Found: doc},
				ReadTime: now,
			}); err != nil {
				return err
			}
		} else {
			if err := stream.Send(&firestorepb.BatchGetDocumentsResponse{
				Result:   &firestorepb.BatchGetDocumentsResponse_Missing{Missing: name},
				ReadTime: now,
			}); err != nil {
				return err
			}
		}
	}
	return nil
}

func (fs *firestoreServer) Listen(_ firestorepb.Firestore_ListenServer) error {
	return status.Errorf(codes.Unimplemented, "localgcp: Listen not yet supported")
}

func (fs *firestoreServer) Write(_ firestorepb.Firestore_WriteServer) error {
	return status.Errorf(codes.Unimplemented, "localgcp: Write not yet supported")
}

func (fs *firestoreServer) ExecutePipeline(_ *firestorepb.ExecutePipelineRequest, _ firestorepb.Firestore_ExecutePipelineServer) error {
	return status.Errorf(codes.Unimplemented, "localgcp: ExecutePipeline not yet supported")
}

func (fs *firestoreServer) RunAggregationQuery(_ *firestorepb.RunAggregationQueryRequest, _ firestorepb.Firestore_RunAggregationQueryServer) error {
	return status.Errorf(codes.Unimplemented, "localgcp: RunAggregationQuery not yet supported")
}

// --- Helpers ---

func generateID() string {
	b := make([]byte, 10)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func generateTxID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// parseDocumentPath extracts the document path portion after the database prefix.
// For example, from "projects/p/databases/d/documents/col/doc" it returns "col/doc".
func parseDocumentPath(name string) string {
	const marker = "/documents/"
	idx := strings.Index(name, marker)
	if idx < 0 {
		return ""
	}
	return name[idx+len(marker):]
}

// nowTimestamp returns the current time as a protobuf Timestamp.
func nowTimestamp() *timestamppb.Timestamp {
	return timestamppb.New(time.Now())
}
