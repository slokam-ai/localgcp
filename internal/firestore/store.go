package firestore

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
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

// ChangeType identifies the kind of document mutation.
type ChangeType int

const (
	ChangeAdded    ChangeType = iota
	ChangeModified
	ChangeRemoved
)

// changeEvent is sent to watchers when a document is mutated.
type changeEvent struct {
	DocName    string
	ChangeType ChangeType
	Doc        *firestorepb.Document // nil for ChangeRemoved
	Seq        uint64                // global sequence number for resume tokens
}

// watcher represents a registered listener on a collection or document.
type watcher struct {
	targetID int32
	prefix   string // collection prefix (e.g. ".../documents/col/") for collection targets
	docName  string // specific document name for document targets
	ch       chan *changeEvent
}

// matches returns true if a document mutation at name should be delivered to this watcher.
func (w *watcher) matches(name string) bool {
	if w.docName != "" {
		return name == w.docName
	}
	if w.prefix != "" {
		if !strings.HasPrefix(name, w.prefix) {
			return false
		}
		// Only direct children: no "/" in the rest after prefix.
		rest := name[len(w.prefix):]
		return !strings.Contains(rest, "/")
	}
	return false
}

// changeLogSize is the maximum number of recent change events retained for
// resume token support. Clients that reconnect within this window get
// incremental updates instead of a full snapshot resend.
const changeLogSize = 1024

// Store is an in-memory hierarchical document store with optional JSON persistence.
type Store struct {
	mu   sync.RWMutex
	docs map[string]*storedDocument // full document path -> document
	dir  string                     // persistence directory; empty = in-memory only

	watchMu   sync.Mutex
	watchers  map[int32]*watcher
	seq       atomic.Uint64          // global mutation sequence counter
	changeLog []*changeEvent         // bounded ring buffer of recent events
	logStart  int                    // ring buffer start index
	logLen    int                    // number of valid entries
}

// NewStore creates a new document store. If dir is non-empty, state is loaded
// from disk and flushed on every write.
func NewStore(dir string) *Store {
	s := &Store{
		docs:      make(map[string]*storedDocument),
		dir:       dir,
		watchers:  make(map[int32]*watcher),
		changeLog: make([]*changeEvent, changeLogSize),
	}
	if dir != "" {
		s.load()
	}
	return s
}

// AddWatcher registers a listener and returns a channel for change events.
// For collection targets, prefix should be "parent/collectionID/" and docName empty.
// For document targets, docName should be the full document path and prefix empty.
func (s *Store) AddWatcher(targetID int32, prefix, docName string) <-chan *changeEvent {
	ch := make(chan *changeEvent, 64)
	s.watchMu.Lock()
	s.watchers[targetID] = &watcher{
		targetID: targetID,
		prefix:   prefix,
		docName:  docName,
		ch:       ch,
	}
	s.watchMu.Unlock()
	return ch
}

// RemoveWatcher deregisters a listener and closes its channel.
func (s *Store) RemoveWatcher(targetID int32) {
	s.watchMu.Lock()
	if w, ok := s.watchers[targetID]; ok {
		close(w.ch)
		delete(s.watchers, targetID)
	}
	s.watchMu.Unlock()
}

// notifyWatchers sends a change event to all matching watchers and appends
// the event to the bounded change log for resume token support.
// Must be called AFTER releasing mu.
func (s *Store) notifyWatchers(name string, ct ChangeType, doc *firestorepb.Document) {
	seq := s.seq.Add(1)
	evt := &changeEvent{DocName: name, ChangeType: ct, Doc: doc, Seq: seq}
	s.watchMu.Lock()
	// Append to ring buffer.
	idx := (s.logStart + s.logLen) % changeLogSize
	s.changeLog[idx] = evt
	if s.logLen < changeLogSize {
		s.logLen++
	} else {
		s.logStart = (s.logStart + 1) % changeLogSize
	}
	for _, w := range s.watchers {
		if w.matches(name) {
			select {
			case w.ch <- evt:
			default: // non-blocking: drop if consumer is too slow
			}
		}
	}
	s.watchMu.Unlock()
}

// EncodeResumeToken encodes a sequence number into a resume token.
func EncodeResumeToken(seq uint64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, seq)
	return b
}

// DecodeResumeToken decodes a resume token back to a sequence number.
// Returns 0, false if the token is invalid.
func DecodeResumeToken(token []byte) (uint64, bool) {
	if len(token) != 8 {
		return 0, false
	}
	return binary.BigEndian.Uint64(token), true
}

// CurrentSeq returns the current sequence number (for generating resume tokens
// when no changes have occurred, e.g. initial CURRENT response).
func (s *Store) CurrentSeq() uint64 {
	return s.seq.Load()
}

// ChangesSince returns all change events with sequence > afterSeq that match
// the given prefix/docName filter. Returns nil, false if the requested sequence
// has been evicted from the change log (client must do a full snapshot).
func (s *Store) ChangesSince(afterSeq uint64, prefix, docName string) ([]*changeEvent, bool) {
	w := &watcher{prefix: prefix, docName: docName}
	s.watchMu.Lock()
	defer s.watchMu.Unlock()

	if s.logLen == 0 {
		return nil, true
	}

	// Check if the oldest entry in the log is still <= afterSeq+1.
	oldest := s.changeLog[s.logStart]
	if oldest.Seq > afterSeq+1 {
		// Gap: events have been evicted. Caller must full-snapshot.
		return nil, false
	}

	var events []*changeEvent
	for i := 0; i < s.logLen; i++ {
		evt := s.changeLog[(s.logStart+i)%changeLogSize]
		if evt.Seq > afterSeq && w.matches(evt.DocName) {
			events = append(events, evt)
		}
	}
	return events, true
}

// SnapshotDocuments returns copies of all documents matching either a
// collection prefix or a specific document name, while holding the read lock.
// The watcher should be registered BEFORE calling this to avoid missing events.
func (s *Store) SnapshotDocuments(prefix, docName string) []*firestorepb.Document {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if docName != "" {
		doc := s.getDocLocked(docName)
		if doc != nil {
			return []*firestorepb.Document{doc}
		}
		return nil
	}

	var docs []*firestorepb.Document
	for name := range s.docs {
		if strings.HasPrefix(name, prefix) && !strings.Contains(name[len(prefix):], "/") {
			docs = append(docs, s.getDocLocked(name))
		}
	}
	sort.Slice(docs, func(i, j int) bool { return docs[i].Name < docs[j].Name })
	return docs
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
	s.notifyWatchers(name, ChangeAdded, doc)
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
		s.notifyWatchers(name, ChangeAdded, doc)
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
	s.notifyWatchers(name, ChangeModified, doc)
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
	s.notifyWatchers(name, ChangeRemoved, nil)
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
