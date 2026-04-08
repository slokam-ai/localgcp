package gcs

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// Service implements the Cloud Storage emulator.
type Service struct {
	dataDir string
	quiet   bool
	logger  *log.Logger
	store   *Store

	// resumable uploads in progress: upload ID -> pending upload state
	resumableMu sync.Mutex
	resumables   map[string]*resumableUpload
}

type resumableUpload struct {
	Bucket      string
	Name        string
	ContentType string
	Data        bytes.Buffer
}

// New creates a new GCS service.
func New(dataDir string, quiet bool) *Service {
	logger := log.New(os.Stderr, "[gcs] ", log.LstdFlags)
	return &Service{
		dataDir:    dataDir,
		quiet:      quiet,
		logger:     logger,
		store:      NewStore(dataDir),
		resumables: make(map[string]*resumableUpload),
	}
}

func (s *Service) Name() string { return "Cloud Storage" }

func (s *Service) Start(ctx context.Context, addr string) error {
	mux := http.NewServeMux()
	s.registerRoutes(mux)

	srv := &http.Server{
		Addr:    addr,
		Handler: s.loggingMiddleware(mux),
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", addr, err)
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		srv.Shutdown(shutdownCtx)
	}()

	if err := srv.Serve(ln); err != http.ErrServerClosed {
		return err
	}
	return nil
}

// registerRoutes sets up all GCS JSON API routes.
//
// GCS JSON API path structure:
//   /storage/v1/b                          — list/create buckets
//   /storage/v1/b/{bucket}                 — get/delete bucket
//   /storage/v1/b/{bucket}/o               — list objects
//   /storage/v1/b/{bucket}/o/{object...}   — get/delete object (object can contain /)
//   /upload/storage/v1/b/{bucket}/o        — upload objects
//
// Object names can contain slashes, so we can't use simple path params.
// We route by prefix and parse manually.
func (s *Service) registerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/storage/v1/b", s.route)
	mux.HandleFunc("/storage/v1/b/", s.route)
	mux.HandleFunc("/upload/storage/v1/b/", s.handleUpload)
	mux.HandleFunc("/download/storage/v1/b/", s.handleDownload)
	mux.HandleFunc("/", s.handleDefault)
}

// handleDownload serves object content via the /download/ path prefix.
// The Go storage client uses this path for NewReader: GET /download/storage/v1/b/{bucket}/o/{object}?alt=media
func (s *Service) handleDownload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "methodNotAllowed", "Method not allowed")
		return
	}

	// Path: /download/storage/v1/b/{bucket}/o/{object...}
	rest := strings.TrimPrefix(r.URL.Path, "/download/storage/v1/b/")
	oIdx := strings.Index(rest, "/o/")
	if oIdx < 0 {
		writeNotFound(w, "Invalid download path")
		return
	}
	bucket := rest[:oIdx]
	objectName := rest[oIdx+3:]

	meta, content, ok := s.store.GetObject(bucket, objectName)
	if !ok {
		writeNotFound(w, fmt.Sprintf("No such object: %s/%s", bucket, objectName))
		return
	}

	w.Header().Set("Content-Type", meta.ContentType)
	w.Header().Set("Content-Length", meta.Size)
	w.Header().Set("X-Goog-Hash", fmt.Sprintf("md5=%s", meta.Md5Hash))
	w.WriteHeader(http.StatusOK)
	w.Write(content)
}

func (s *Service) route(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path

	// /storage/v1/b — bucket list or create
	if path == "/storage/v1/b" || path == "/storage/v1/b/" {
		switch r.Method {
		case http.MethodGet:
			s.handleListBuckets(w, r)
		case http.MethodPost:
			s.handleCreateBucket(w, r)
		default:
			writeError(w, http.StatusMethodNotAllowed, "methodNotAllowed", "Method not allowed")
		}
		return
	}

	// Strip prefix to get: {bucket} or {bucket}/o or {bucket}/o/{object...}
	rest := strings.TrimPrefix(path, "/storage/v1/b/")

	// Check for copy: {bucket}/o/{src}/copyTo/b/{dstBucket}/o/{dstObj}
	if strings.Contains(rest, "/copyTo/b/") {
		if r.Method == http.MethodPost {
			s.handleCopyObject(w, r, rest)
		} else {
			writeError(w, http.StatusMethodNotAllowed, "methodNotAllowed", "Method not allowed")
		}
		return
	}

	// Check for /o/ (object operations)
	oIdx := strings.Index(rest, "/o/")
	if oIdx >= 0 {
		bucket := rest[:oIdx]
		objectName := rest[oIdx+3:] // everything after /o/
		switch r.Method {
		case http.MethodGet:
			s.handleGetObject(w, r, bucket, objectName)
		case http.MethodDelete:
			s.handleDeleteObject(w, r, bucket, objectName)
		default:
			writeError(w, http.StatusMethodNotAllowed, "methodNotAllowed", "Method not allowed")
		}
		return
	}

	// Check for /o (object list, no trailing object name)
	if strings.HasSuffix(rest, "/o") {
		bucket := strings.TrimSuffix(rest, "/o")
		if r.Method == http.MethodGet {
			s.handleListObjects(w, r, bucket)
		} else {
			writeError(w, http.StatusMethodNotAllowed, "methodNotAllowed", "Method not allowed")
		}
		return
	}

	// Otherwise it's a bucket operation: {bucket}
	bucket := rest
	switch r.Method {
	case http.MethodGet:
		s.handleGetBucket(w, r, bucket)
	case http.MethodDelete:
		s.handleDeleteBucket(w, r, bucket)
	default:
		writeError(w, http.StatusMethodNotAllowed, "methodNotAllowed", "Method not allowed")
	}
}

// --- Bucket handlers ---

func (s *Service) handleCreateBucket(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Name == "" {
		writeBadRequest(w, "Bucket name is required")
		return
	}

	project := r.URL.Query().Get("project")
	if project == "" {
		project = "localgcp"
	}

	b, err := s.store.CreateBucket(body.Name, project)
	if err != nil {
		if strings.Contains(err.Error(), "conflict") {
			writeConflict(w, fmt.Sprintf("You already own this bucket: %s", body.Name))
			return
		}
		writeError(w, http.StatusInternalServerError, "internalError", err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(b)
}

func (s *Service) handleGetBucket(w http.ResponseWriter, r *http.Request, name string) {
	b, ok := s.store.GetBucket(name)
	if !ok {
		writeNotFound(w, fmt.Sprintf("The specified bucket does not exist: %s", name))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(b)
}

func (s *Service) handleListBuckets(w http.ResponseWriter, r *http.Request) {
	buckets := s.store.ListBuckets()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(BucketList{Kind: "storage#buckets", Items: buckets})
}

func (s *Service) handleDeleteBucket(w http.ResponseWriter, r *http.Request, name string) {
	err := s.store.DeleteBucket(name)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			writeNotFound(w, fmt.Sprintf("The specified bucket does not exist: %s", name))
		} else if strings.Contains(err.Error(), "not empty") {
			writeConflict(w, "The bucket you tried to delete is not empty.")
		} else {
			writeError(w, http.StatusInternalServerError, "internalError", err.Error())
		}
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- Object handlers ---

func (s *Service) handleGetObject(w http.ResponseWriter, r *http.Request, bucket, name string) {
	meta, content, ok := s.store.GetObject(bucket, name)
	if !ok {
		writeNotFound(w, fmt.Sprintf("No such object: %s/%s", bucket, name))
		return
	}

	// alt=media means download the content, otherwise return metadata.
	if r.URL.Query().Get("alt") == "media" {
		w.Header().Set("Content-Type", meta.ContentType)
		w.Header().Set("Content-Length", meta.Size)
		w.Header().Set("X-Goog-Hash", fmt.Sprintf("md5=%s", meta.Md5Hash))
		w.WriteHeader(http.StatusOK)
		w.Write(content)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(meta)
}

func (s *Service) handleDeleteObject(w http.ResponseWriter, r *http.Request, bucket, name string) {
	if err := s.store.DeleteObject(bucket, name); err != nil {
		if strings.Contains(err.Error(), "not found") {
			writeNotFound(w, fmt.Sprintf("No such object: %s/%s", bucket, name))
		} else {
			writeError(w, http.StatusInternalServerError, "internalError", err.Error())
		}
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) handleListObjects(w http.ResponseWriter, r *http.Request, bucket string) {
	if _, ok := s.store.GetBucket(bucket); !ok {
		writeNotFound(w, fmt.Sprintf("The specified bucket does not exist: %s", bucket))
		return
	}

	prefix := r.URL.Query().Get("prefix")
	delimiter := r.URL.Query().Get("delimiter")
	items, prefixes := s.store.ListObjects(bucket, prefix, delimiter, 0)

	if items == nil {
		items = []Object{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(ObjectList{
		Kind:     "storage#objects",
		Items:    items,
		Prefixes: prefixes,
	})
}

func (s *Service) handleCopyObject(w http.ResponseWriter, r *http.Request, rest string) {
	// Format: {srcBucket}/o/{srcObject}/copyTo/b/{dstBucket}/o/{dstObject}
	parts := strings.SplitN(rest, "/o/", 2)
	if len(parts) != 2 {
		writeBadRequest(w, "Invalid copy path")
		return
	}
	srcBucket := parts[0]

	copyIdx := strings.Index(parts[1], "/copyTo/b/")
	if copyIdx < 0 {
		writeBadRequest(w, "Invalid copy path")
		return
	}
	srcObject := parts[1][:copyIdx]

	dstPart := parts[1][copyIdx+len("/copyTo/b/"):]
	dstParts := strings.SplitN(dstPart, "/o/", 2)
	if len(dstParts) != 2 {
		writeBadRequest(w, "Invalid copy destination path")
		return
	}
	dstBucket := dstParts[0]
	dstObject := dstParts[1]

	obj, err := s.store.CopyObject(srcBucket, srcObject, dstBucket, dstObject)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			writeNotFound(w, err.Error())
		} else {
			writeError(w, http.StatusInternalServerError, "internalError", err.Error())
		}
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(obj)
}

// --- Upload handlers ---

func (s *Service) handleUpload(w http.ResponseWriter, r *http.Request) {
	// Path: /upload/storage/v1/b/{bucket}/o
	path := strings.TrimPrefix(r.URL.Path, "/upload/storage/v1/b/")
	bucket := strings.TrimSuffix(path, "/o")

	if _, ok := s.store.GetBucket(bucket); !ok {
		writeNotFound(w, fmt.Sprintf("The specified bucket does not exist: %s", bucket))
		return
	}

	uploadType := r.URL.Query().Get("uploadType")

	switch uploadType {
	case "media":
		s.handleSimpleUpload(w, r, bucket)
	case "multipart":
		s.handleMultipartUpload(w, r, bucket)
	case "resumable":
		if r.Method == http.MethodPost {
			s.handleResumableInit(w, r, bucket)
		} else if r.Method == http.MethodPut {
			s.handleResumableChunk(w, r, bucket)
		} else {
			writeError(w, http.StatusMethodNotAllowed, "methodNotAllowed", "Method not allowed")
		}
	default:
		writeBadRequest(w, "uploadType must be media, multipart, or resumable")
	}
}

func (s *Service) handleSimpleUpload(w http.ResponseWriter, r *http.Request, bucket string) {
	name := r.URL.Query().Get("name")
	if name == "" {
		writeBadRequest(w, "Object name is required (query param 'name')")
		return
	}

	content, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internalError", "Failed to read body")
		return
	}

	contentType := r.Header.Get("Content-Type")
	obj, err := s.store.PutObject(bucket, name, contentType, content)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internalError", err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(obj)
}

func (s *Service) handleMultipartUpload(w http.ResponseWriter, r *http.Request, bucket string) {
	contentType := r.Header.Get("Content-Type")
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil || !strings.HasPrefix(mediaType, "multipart/") {
		writeBadRequest(w, "Content-Type must be multipart/related")
		return
	}

	reader := multipart.NewReader(r.Body, params["boundary"])

	// First part: JSON metadata.
	metaPart, err := reader.NextPart()
	if err != nil {
		writeBadRequest(w, "Failed to read metadata part")
		return
	}

	var meta struct {
		Name        string `json:"name"`
		ContentType string `json:"contentType"`
	}
	if err := json.NewDecoder(metaPart).Decode(&meta); err != nil {
		writeBadRequest(w, "Invalid JSON metadata")
		return
	}
	metaPart.Close()

	if meta.Name == "" {
		writeBadRequest(w, "Object name is required in metadata")
		return
	}

	// Second part: file content.
	dataPart, err := reader.NextPart()
	if err != nil {
		writeBadRequest(w, "Failed to read data part")
		return
	}
	content, err := io.ReadAll(dataPart)
	dataPart.Close()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internalError", "Failed to read data")
		return
	}

	obj, err := s.store.PutObject(bucket, meta.Name, meta.ContentType, content)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internalError", err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(obj)
}

func (s *Service) handleResumableInit(w http.ResponseWriter, r *http.Request, bucket string) {
	name := r.URL.Query().Get("name")

	// The object name can also come from the JSON body.
	if name == "" {
		var meta struct {
			Name string `json:"name"`
		}
		json.NewDecoder(r.Body).Decode(&meta)
		name = meta.Name
	}

	if name == "" {
		writeBadRequest(w, "Object name is required")
		return
	}

	uploadID := generateEtag()

	s.resumableMu.Lock()
	s.resumables[uploadID] = &resumableUpload{
		Bucket:      bucket,
		Name:        name,
		ContentType: r.Header.Get("X-Upload-Content-Type"),
	}
	s.resumableMu.Unlock()

	// Return the upload URI in the Location header.
	location := fmt.Sprintf("%s?uploadType=resumable&upload_id=%s",
		r.URL.Path, uploadID)

	w.Header().Set("Location", location)
	w.WriteHeader(http.StatusOK)
}

func (s *Service) handleResumableChunk(w http.ResponseWriter, r *http.Request, bucket string) {
	uploadID := r.URL.Query().Get("upload_id")
	if uploadID == "" {
		writeBadRequest(w, "upload_id is required")
		return
	}

	s.resumableMu.Lock()
	ru, ok := s.resumables[uploadID]
	s.resumableMu.Unlock()

	if !ok {
		writeNotFound(w, "Upload session not found")
		return
	}

	data, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internalError", "Failed to read body")
		return
	}
	ru.Data.Write(data)

	// For simplicity, we treat every PUT as a complete upload.
	// Real GCS supports chunked resumable uploads with Content-Range headers,
	// but most client libraries send the entire content in one PUT.
	contentType := ru.ContentType
	if contentType == "" {
		contentType = r.Header.Get("Content-Type")
	}

	obj, err := s.store.PutObject(ru.Bucket, ru.Name, contentType, ru.Data.Bytes())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internalError", err.Error())
		return
	}

	s.resumableMu.Lock()
	delete(s.resumables, uploadID)
	s.resumableMu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(obj)
}

// --- Middleware and default handler ---

func (s *Service) handleDefault(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/" {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"kind":    "storage#serviceAccount",
			"service": "localgcp",
		})
		return
	}

	// XML API style: GET /{bucket}/{object} — used by the Go storage client for downloads.
	// Path format: /{bucket}/{object...} where object can contain slashes.
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		path := strings.TrimPrefix(r.URL.Path, "/")
		if idx := strings.Index(path, "/"); idx > 0 {
			bucket := path[:idx]
			objectName := path[idx+1:]
			if objectName != "" {
				if _, ok := s.store.GetBucket(bucket); ok {
					meta, content, ok := s.store.GetObject(bucket, objectName)
					if ok {
						w.Header().Set("Content-Type", meta.ContentType)
						w.Header().Set("Content-Length", meta.Size)
						w.Header().Set("X-Goog-Hash", fmt.Sprintf("md5=%s", meta.Md5Hash))
						w.Header().Set("Accept-Ranges", "bytes")
						w.Header().Set("Etag", meta.Etag)
						if r.Method == http.MethodHead {
							w.WriteHeader(http.StatusOK)
							return
						}
						w.WriteHeader(http.StatusOK)
						w.Write(content)
						return
					}
				}
			}
		}
	}

	writeNotFound(w, fmt.Sprintf("Path not found: %s", r.URL.Path))
}

func (s *Service) loggingMiddleware(next http.Handler) http.Handler {
	if s.quiet {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}
		next.ServeHTTP(rw, r)
		s.logger.Printf("%s %s %d %s",
			r.Method, r.URL.Path, rw.statusCode,
			time.Since(start).Round(time.Millisecond))
	})
}

type responseWriter struct {
	http.ResponseWriter
	statusCode int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}
