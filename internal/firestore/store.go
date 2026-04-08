package firestore

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"cloud.google.com/go/firestore/apiv1/firestorepb"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// storedDocument holds a Firestore document in memory.
type storedDocument struct {
	Fields     map[string]*firestorepb.Value
	CreateTime *timestamppb.Timestamp
	UpdateTime *timestamppb.Timestamp
}

// Store is an in-memory hierarchical document store with optional JSON persistence.
type Store struct {
	mu   sync.RWMutex
	docs map[string]*storedDocument // full document path -> document
	dir  string                     // persistence directory; empty = in-memory only
}

// NewStore creates a new document store. If dir is non-empty, state is loaded
// from disk and flushed on every write.
func NewStore(dir string) *Store {
	s := &Store{
		docs: make(map[string]*storedDocument),
		dir:  dir,
	}
	if dir != "" {
		s.load()
	}
	return s
}

// --- Document CRUD ---

// GetDocument returns a document by its full resource name, or nil if not found.
func (s *Store) GetDocument(name string) *firestorepb.Document {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.getDocLocked(name)
}

func (s *Store) getDocLocked(name string) *firestorepb.Document {
	sd, ok := s.docs[name]
	if !ok {
		return nil
	}
	return &firestorepb.Document{
		Name:       name,
		Fields:     copyFields(sd.Fields),
		CreateTime: sd.CreateTime,
		UpdateTime: sd.UpdateTime,
	}
}

// copyFields returns a shallow copy of a Firestore fields map so that callers
// cannot mutate the store's internal state after the lock is released.
func copyFields(src map[string]*firestorepb.Value) map[string]*firestorepb.Value {
	if src == nil {
		return nil
	}
	dst := make(map[string]*firestorepb.Value, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

// CreateDocument creates a new document. Returns the document and true on
// success, or nil and false if a document with that name already exists.
func (s *Store) CreateDocument(name string, fields map[string]*firestorepb.Value) (*firestorepb.Document, bool) {
	s.mu.Lock()
	if _, exists := s.docs[name]; exists {
		s.mu.Unlock()
		return nil, false
	}
	now := timestamppb.Now()
	sd := &storedDocument{
		Fields:     fields,
		CreateTime: now,
		UpdateTime: now,
	}
	s.docs[name] = sd
	doc := s.getDocLocked(name)
	s.mu.Unlock()
	s.persist()
	return doc, true
}

// UpdateDocument applies a full or partial update. If updateMask is nil or empty,
// the entire document is replaced. Otherwise only the listed fields are updated.
// Creates the document if it does not exist (upsert).
func (s *Store) UpdateDocument(name string, fields map[string]*firestorepb.Value, updateMask []string) *firestorepb.Document {
	s.mu.Lock()

	now := timestamppb.Now()
	existing, ok := s.docs[name]
	if !ok {
		// Upsert: create new.
		sd := &storedDocument{
			Fields:     fields,
			CreateTime: now,
			UpdateTime: now,
		}
		s.docs[name] = sd
		doc := s.getDocLocked(name)
		s.mu.Unlock()
		s.persist()
		return doc
	}

	if len(updateMask) == 0 {
		// Full replace.
		existing.Fields = fields
	} else {
		if existing.Fields == nil {
			existing.Fields = make(map[string]*firestorepb.Value)
		}
		for _, fp := range updateMask {
			if v, ok := fields[fp]; ok {
				existing.Fields[fp] = v
			} else {
				// Field in mask but not in input: delete it.
				delete(existing.Fields, fp)
			}
		}
	}
	existing.UpdateTime = now
	doc := s.getDocLocked(name)
	s.mu.Unlock()
	s.persist()
	return doc
}

// DeleteDocument removes a document. Returns false if it did not exist.
func (s *Store) DeleteDocument(name string) bool {
	s.mu.Lock()
	if _, ok := s.docs[name]; !ok {
		s.mu.Unlock()
		return false
	}
	delete(s.docs, name)
	s.mu.Unlock()
	s.persist()
	return true
}

// ListDocuments returns all documents that are direct children of the given
// parent path in the specified collection. parent is a resource path like
// "projects/p/databases/d/documents" or "projects/p/databases/d/documents/col/doc".
// collectionID is the collection name (e.g. "users").
func (s *Store) ListDocuments(parent, collectionID string) []*firestorepb.Document {
	s.mu.RLock()
	defer s.mu.RUnlock()

	prefix := parent + "/" + collectionID + "/"

	var docs []*firestorepb.Document
	for name, sd := range s.docs {
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		// Only direct children: the remaining part after prefix should have no '/'.
		rest := name[len(prefix):]
		if strings.Contains(rest, "/") {
			continue
		}
		docs = append(docs, &firestorepb.Document{
			Name:       name,
			Fields:     sd.Fields,
			CreateTime: sd.CreateTime,
			UpdateTime: sd.UpdateTime,
		})
	}

	sort.Slice(docs, func(i, j int) bool { return docs[i].Name < docs[j].Name })
	return docs
}

// CollectionDocuments returns all documents whose name starts with the given
// collection prefix (parent + "/" + collectionID + "/"), including only direct
// children (no subcollection docs).
func (s *Store) CollectionDocuments(parent, collectionID string) []*firestorepb.Document {
	return s.ListDocuments(parent, collectionID)
}

// --- Persistence ---

type persistedDoc struct {
	Name       string          `json:"name"`
	Fields     json.RawMessage `json:"fields"`
	CreateTime string          `json:"createTime"`
	UpdateTime string          `json:"updateTime"`
}

type persistedState struct {
	Documents []persistedDoc `json:"documents"`
}

func (s *Store) persist() {
	if s.dir == "" {
		return
	}

	dir := filepath.Join(s.dir, "firestore")
	os.MkdirAll(dir, 0o755)

	var state persistedState
	for name, sd := range s.docs {
		fieldsJSON, _ := marshalFields(sd.Fields)
		pd := persistedDoc{
			Name:   name,
			Fields: fieldsJSON,
		}
		if sd.CreateTime != nil {
			pd.CreateTime = sd.CreateTime.AsTime().Format(time.RFC3339Nano)
		}
		if sd.UpdateTime != nil {
			pd.UpdateTime = sd.UpdateTime.AsTime().Format(time.RFC3339Nano)
		}
		state.Documents = append(state.Documents, pd)
	}

	data, _ := json.MarshalIndent(state, "", "  ")
	os.WriteFile(filepath.Join(dir, "state.json"), data, 0o644)
}

func (s *Store) load() {
	path := filepath.Join(s.dir, "firestore", "state.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}

	var state persistedState
	if err := json.Unmarshal(data, &state); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: corrupt data in %s, starting with empty state\n", path)
		return
	}

	for _, pd := range state.Documents {
		fields, err := unmarshalFields(pd.Fields)
		if err != nil {
			continue
		}
		sd := &storedDocument{Fields: fields}
		if pd.CreateTime != "" {
			if t, err := time.Parse(time.RFC3339Nano, pd.CreateTime); err == nil {
				sd.CreateTime = timestamppb.New(t)
			}
		}
		if pd.UpdateTime != "" {
			if t, err := time.Parse(time.RFC3339Nano, pd.UpdateTime); err == nil {
				sd.UpdateTime = timestamppb.New(t)
			}
		}
		s.docs[pd.Name] = sd
	}
}

// marshalFields serialises a Firestore fields map to JSON using protojson.
func marshalFields(fields map[string]*firestorepb.Value) (json.RawMessage, error) {
	// Wrap in a Document so protojson can handle it.
	doc := &firestorepb.Document{Fields: fields}
	b, err := protojson.Marshal(doc)
	if err != nil {
		return nil, err
	}
	// Extract just the "fields" key.
	var m map[string]json.RawMessage
	json.Unmarshal(b, &m)
	if f, ok := m["fields"]; ok {
		return f, nil
	}
	return []byte("{}"), nil
}

// unmarshalFields deserialises JSON back into a Firestore fields map.
func unmarshalFields(data json.RawMessage) (map[string]*firestorepb.Value, error) {
	if len(data) == 0 {
		return nil, nil
	}
	// Wrap in a Document-like JSON envelope.
	envelope := fmt.Sprintf(`{"fields":%s}`, string(data))
	var doc firestorepb.Document
	if err := protojson.Unmarshal([]byte(envelope), &doc); err != nil {
		return nil, err
	}
	return doc.Fields, nil
}
