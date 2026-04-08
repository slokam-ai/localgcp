package gcs

import (
	"crypto/md5"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Bucket represents a GCS bucket.
type Bucket struct {
	Kind         string `json:"kind"`
	ID           string `json:"id"`
	Name         string `json:"name"`
	Location     string `json:"location"`
	StorageClass string `json:"storageClass"`
	TimeCreated  string `json:"timeCreated"`
	Updated      string `json:"updated"`
	Etag         string `json:"etag"`
}

// Object represents a GCS object (metadata only, content stored separately).
type Object struct {
	Kind            string `json:"kind"`
	ID              string `json:"id"`
	Name            string `json:"name"`
	Bucket          string `json:"bucket"`
	Size            string `json:"size"`
	ContentType     string `json:"contentType"`
	TimeCreated     string `json:"timeCreated"`
	Updated         string `json:"updated"`
	Md5Hash         string `json:"md5Hash"`
	Crc32c          string `json:"crc32c"`
	Etag            string `json:"etag"`
	ContentEncoding string `json:"contentEncoding,omitempty"`
}

// BucketList is the response for listing buckets.
type BucketList struct {
	Kind  string   `json:"kind"`
	Items []Bucket `json:"items"`
}

// ObjectList is the response for listing objects.
type ObjectList struct {
	Kind     string   `json:"kind"`
	Items    []Object `json:"items"`
	Prefixes []string `json:"prefixes,omitempty"`
}

// Store is the storage backend for the GCS emulator.
type Store struct {
	mu      sync.RWMutex
	buckets map[string]*Bucket
	objects map[string]map[string]*storedObject // bucket -> object name -> object
	dataDir string
}

type storedObject struct {
	Meta    Object
	Content []byte
}

// NewStore creates a new GCS store. If dataDir is non-empty, it loads
// persisted state and flushes on writes.
func NewStore(dataDir string) *Store {
	s := &Store{
		buckets: make(map[string]*Bucket),
		objects: make(map[string]map[string]*storedObject),
		dataDir: dataDir,
	}
	if dataDir != "" {
		s.load()
	}
	return s
}

// --- Bucket operations ---

func (s *Store) CreateBucket(name, project string) (*Bucket, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.buckets[name]; exists {
		return nil, fmt.Errorf("conflict: bucket %q already exists", name)
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	b := &Bucket{
		Kind:         "storage#bucket",
		ID:           name,
		Name:         name,
		Location:     "US",
		StorageClass: "STANDARD",
		TimeCreated:  now,
		Updated:      now,
		Etag:         generateEtag(),
	}
	s.buckets[name] = b
	s.objects[name] = make(map[string]*storedObject)
	s.persist()
	return b, nil
}

func (s *Store) GetBucket(name string) (*Bucket, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	b, ok := s.buckets[name]
	return b, ok
}

func (s *Store) ListBuckets() []Bucket {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]Bucket, 0, len(s.buckets))
	for _, b := range s.buckets {
		result = append(result, *b)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

func (s *Store) DeleteBucket(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.buckets[name]; !exists {
		return fmt.Errorf("not found: bucket %q", name)
	}
	if objs, ok := s.objects[name]; ok && len(objs) > 0 {
		return fmt.Errorf("conflict: bucket %q is not empty", name)
	}

	delete(s.buckets, name)
	delete(s.objects, name)
	s.persist()
	return nil
}

// --- Object operations ---

func (s *Store) PutObject(bucket, name, contentType string, content []byte) (*Object, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.buckets[bucket]; !exists {
		return nil, fmt.Errorf("not found: bucket %q", bucket)
	}

	if contentType == "" {
		contentType = "application/octet-stream"
	}

	md5sum := md5.Sum(content)
	sha256sum := sha256.Sum256(content)
	now := time.Now().UTC().Format(time.RFC3339Nano)

	obj := &Object{
		Kind:        "storage#object",
		ID:          fmt.Sprintf("%s/%s", bucket, name),
		Name:        name,
		Bucket:      bucket,
		Size:        fmt.Sprintf("%d", len(content)),
		ContentType: contentType,
		TimeCreated: now,
		Updated:     now,
		Md5Hash:     base64.StdEncoding.EncodeToString(md5sum[:]),
		Crc32c:      "AAAAAA==",
		Etag:        hex.EncodeToString(sha256sum[:8]),
	}

	s.objects[bucket][name] = &storedObject{Meta: *obj, Content: content}
	s.persist()
	return obj, nil
}

func (s *Store) GetObject(bucket, name string) (*Object, []byte, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	objs, ok := s.objects[bucket]
	if !ok {
		return nil, nil, false
	}
	so, ok := objs[name]
	if !ok {
		return nil, nil, false
	}
	return &so.Meta, so.Content, true
}

func (s *Store) DeleteObject(bucket, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	objs, ok := s.objects[bucket]
	if !ok {
		return fmt.Errorf("not found: bucket %q", bucket)
	}
	if _, ok := objs[name]; !ok {
		return fmt.Errorf("not found: object %q in bucket %q", name, bucket)
	}

	delete(objs, name)
	s.persist()
	return nil
}

func (s *Store) CopyObject(srcBucket, srcName, dstBucket, dstName string) (*Object, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.buckets[dstBucket]; !exists {
		return nil, fmt.Errorf("not found: bucket %q", dstBucket)
	}

	srcObjs, ok := s.objects[srcBucket]
	if !ok {
		return nil, fmt.Errorf("not found: bucket %q", srcBucket)
	}
	src, ok := srcObjs[srcName]
	if !ok {
		return nil, fmt.Errorf("not found: object %q in bucket %q", srcName, srcBucket)
	}

	// Make an independent copy of the content.
	content := make([]byte, len(src.Content))
	copy(content, src.Content)

	now := time.Now().UTC().Format(time.RFC3339Nano)
	obj := src.Meta
	obj.ID = fmt.Sprintf("%s/%s", dstBucket, dstName)
	obj.Name = dstName
	obj.Bucket = dstBucket
	obj.TimeCreated = now
	obj.Updated = now

	s.objects[dstBucket][dstName] = &storedObject{Meta: obj, Content: content}
	s.persist()
	return &obj, nil
}

// ListObjects lists objects in a bucket with optional prefix and delimiter filtering.
func (s *Store) ListObjects(bucket, prefix, delimiter string, maxResults int) ([]Object, []string) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	objs, ok := s.objects[bucket]
	if !ok {
		return nil, nil
	}

	var items []Object
	prefixSet := make(map[string]struct{})

	for name, so := range objs {
		if prefix != "" && !strings.HasPrefix(name, prefix) {
			continue
		}

		if delimiter != "" {
			// Check if there's a delimiter after the prefix.
			rest := name[len(prefix):]
			idx := strings.Index(rest, delimiter)
			if idx >= 0 {
				// This object is "inside" a pseudo-directory. Add the prefix, skip the object.
				commonPrefix := prefix + rest[:idx+len(delimiter)]
				prefixSet[commonPrefix] = struct{}{}
				continue
			}
		}

		items = append(items, so.Meta)
	}

	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })

	if maxResults > 0 && len(items) > maxResults {
		items = items[:maxResults]
	}

	var prefixes []string
	for p := range prefixSet {
		prefixes = append(prefixes, p)
	}
	sort.Strings(prefixes)

	return items, prefixes
}

// --- Persistence ---

type persistedState struct {
	Buckets []Bucket                   `json:"buckets"`
	Objects map[string][]persistedObj  `json:"objects"`
}

type persistedObj struct {
	Meta    Object `json:"meta"`
	Content string `json:"content"` // base64 encoded
}

func (s *Store) persist() {
	if s.dataDir == "" {
		return
	}

	dir := filepath.Join(s.dataDir, "gcs")
	os.MkdirAll(dir, 0o755)

	state := persistedState{
		Buckets: make([]Bucket, 0, len(s.buckets)),
		Objects: make(map[string][]persistedObj),
	}

	for _, b := range s.buckets {
		state.Buckets = append(state.Buckets, *b)
	}

	for bucket, objs := range s.objects {
		var pObjs []persistedObj
		for _, so := range objs {
			pObjs = append(pObjs, persistedObj{
				Meta:    so.Meta,
				Content: base64.StdEncoding.EncodeToString(so.Content),
			})
		}
		state.Objects[bucket] = pObjs
	}

	data, _ := json.MarshalIndent(state, "", "  ")
	os.WriteFile(filepath.Join(dir, "state.json"), data, 0o644)
}

func (s *Store) load() {
	path := filepath.Join(s.dataDir, "gcs", "state.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return // No persisted state, start fresh.
	}

	var state persistedState
	if err := json.Unmarshal(data, &state); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: corrupt data in %s, starting with empty state\n", path)
		return
	}

	for i := range state.Buckets {
		b := state.Buckets[i]
		s.buckets[b.Name] = &b
		if _, ok := s.objects[b.Name]; !ok {
			s.objects[b.Name] = make(map[string]*storedObject)
		}
	}

	for bucket, pObjs := range state.Objects {
		if _, ok := s.objects[bucket]; !ok {
			s.objects[bucket] = make(map[string]*storedObject)
		}
		for _, po := range pObjs {
			content, err := base64.StdEncoding.DecodeString(po.Content)
			if err != nil {
				continue
			}
			s.objects[bucket][po.Meta.Name] = &storedObject{Meta: po.Meta, Content: content}
		}
	}
}

// --- Helpers ---

var etagCounter uint64
var etagMu sync.Mutex

func generateEtag() string {
	etagMu.Lock()
	defer etagMu.Unlock()
	etagCounter++
	return fmt.Sprintf("CL%d=", etagCounter)
}
