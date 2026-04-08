package firestore

import (
	"sync"
	"testing"

	"cloud.google.com/go/firestore/apiv1/firestorepb"
)

const testDocName = "projects/test/databases/(default)/documents/col/doc1"

func TestStoreCreateDocument(t *testing.T) {
	s := NewStore("")

	fields := map[string]*firestorepb.Value{"name": strVal("alice")}
	doc, ok := s.CreateDocument(testDocName, fields)
	if !ok {
		t.Fatal("expected create to succeed")
	}
	if doc.Name != testDocName {
		t.Fatalf("got name %q, want %q", doc.Name, testDocName)
	}
	if doc.Fields["name"].GetStringValue() != "alice" {
		t.Fatal("field mismatch")
	}
	if doc.CreateTime == nil || doc.UpdateTime == nil {
		t.Fatal("timestamps should be set")
	}
}

func TestStoreCreateDuplicateReturnsNotOk(t *testing.T) {
	s := NewStore("")

	fields := map[string]*firestorepb.Value{"x": strVal("1")}
	_, ok := s.CreateDocument(testDocName, fields)
	if !ok {
		t.Fatal("first create should succeed")
	}
	_, ok = s.CreateDocument(testDocName, fields)
	if ok {
		t.Fatal("duplicate create should return false")
	}
}

func TestStoreCreateConcurrentRace(t *testing.T) {
	s := NewStore("")
	fields := map[string]*firestorepb.Value{"x": strVal("1")}

	const goroutines = 50
	var wg sync.WaitGroup
	successes := make(chan bool, goroutines)

	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			_, ok := s.CreateDocument(testDocName, fields)
			successes <- ok
		}()
	}
	wg.Wait()
	close(successes)

	count := 0
	for ok := range successes {
		if ok {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected exactly 1 successful create, got %d", count)
	}
}

func TestStoreGetDocument(t *testing.T) {
	s := NewStore("")
	s.CreateDocument(testDocName, map[string]*firestorepb.Value{"k": strVal("v")})

	doc := s.GetDocument(testDocName)
	if doc == nil {
		t.Fatal("expected document, got nil")
	}
	if doc.Fields["k"].GetStringValue() != "v" {
		t.Fatal("field mismatch")
	}
}

func TestStoreGetDocumentReturnsCopy(t *testing.T) {
	s := NewStore("")
	s.CreateDocument(testDocName, map[string]*firestorepb.Value{"k": strVal("v")})

	doc1 := s.GetDocument(testDocName)
	doc1.Fields["k"] = strVal("mutated")

	doc2 := s.GetDocument(testDocName)
	if doc2.Fields["k"].GetStringValue() != "v" {
		t.Fatal("store internal state was mutated via returned pointer")
	}
}

func TestStoreGetNonexistent(t *testing.T) {
	s := NewStore("")
	if s.GetDocument("projects/test/databases/(default)/documents/nope/nope") != nil {
		t.Fatal("expected nil for nonexistent document")
	}
}

func TestStoreUpdateDocumentFullReplace(t *testing.T) {
	s := NewStore("")
	s.CreateDocument(testDocName, map[string]*firestorepb.Value{
		"a": strVal("1"),
		"b": strVal("2"),
	})

	doc := s.UpdateDocument(testDocName, map[string]*firestorepb.Value{
		"c": strVal("3"),
	}, nil)

	if _, ok := doc.Fields["a"]; ok {
		t.Fatal("field 'a' should have been replaced")
	}
	if doc.Fields["c"].GetStringValue() != "3" {
		t.Fatal("field 'c' should be '3'")
	}
}

func TestStoreUpdateDocumentWithMask(t *testing.T) {
	s := NewStore("")
	s.CreateDocument(testDocName, map[string]*firestorepb.Value{
		"a": strVal("1"),
		"b": strVal("2"),
	})

	doc := s.UpdateDocument(testDocName, map[string]*firestorepb.Value{
		"b": strVal("updated"),
	}, []string{"b"})

	if doc.Fields["a"].GetStringValue() != "1" {
		t.Fatal("field 'a' should be unchanged")
	}
	if doc.Fields["b"].GetStringValue() != "updated" {
		t.Fatal("field 'b' should be 'updated'")
	}
}

func TestStoreUpdateDocumentMaskDeletesField(t *testing.T) {
	s := NewStore("")
	s.CreateDocument(testDocName, map[string]*firestorepb.Value{
		"a": strVal("1"),
		"b": strVal("2"),
	})

	// Mask includes "b" but input doesn't have it => delete.
	doc := s.UpdateDocument(testDocName, map[string]*firestorepb.Value{}, []string{"b"})
	if _, ok := doc.Fields["b"]; ok {
		t.Fatal("field 'b' should have been deleted")
	}
	if doc.Fields["a"].GetStringValue() != "1" {
		t.Fatal("field 'a' should be unchanged")
	}
}

func TestStoreUpdateDocumentUpsert(t *testing.T) {
	s := NewStore("")
	doc := s.UpdateDocument(testDocName, map[string]*firestorepb.Value{
		"x": strVal("new"),
	}, nil)

	if doc.Name != testDocName {
		t.Fatalf("got name %q, want %q", doc.Name, testDocName)
	}
	if doc.Fields["x"].GetStringValue() != "new" {
		t.Fatal("upserted field mismatch")
	}
}

func TestStoreDeleteDocument(t *testing.T) {
	s := NewStore("")
	s.CreateDocument(testDocName, map[string]*firestorepb.Value{"k": strVal("v")})

	if !s.DeleteDocument(testDocName) {
		t.Fatal("expected delete to succeed")
	}
	if s.GetDocument(testDocName) != nil {
		t.Fatal("document should be gone after delete")
	}
}

func TestStoreDeleteNonexistent(t *testing.T) {
	s := NewStore("")
	if s.DeleteDocument(testDocName) {
		t.Fatal("deleting nonexistent document should return false")
	}
}

func TestStoreListDocuments(t *testing.T) {
	s := NewStore("")
	parent := "projects/test/databases/(default)/documents"
	s.CreateDocument(parent+"/users/alice", map[string]*firestorepb.Value{"name": strVal("alice")})
	s.CreateDocument(parent+"/users/bob", map[string]*firestorepb.Value{"name": strVal("bob")})
	s.CreateDocument(parent+"/posts/post1", map[string]*firestorepb.Value{"title": strVal("hi")})

	docs := s.ListDocuments(parent, "users")
	if len(docs) != 2 {
		t.Fatalf("expected 2 users, got %d", len(docs))
	}
	// Should be sorted by name.
	if docs[0].Fields["name"].GetStringValue() != "alice" {
		t.Fatal("first doc should be alice")
	}
	if docs[1].Fields["name"].GetStringValue() != "bob" {
		t.Fatal("second doc should be bob")
	}
}

func TestStoreListDocumentsExcludesSubcollections(t *testing.T) {
	s := NewStore("")
	parent := "projects/test/databases/(default)/documents"
	s.CreateDocument(parent+"/users/alice", map[string]*firestorepb.Value{})
	s.CreateDocument(parent+"/users/alice/posts/post1", map[string]*firestorepb.Value{})

	docs := s.ListDocuments(parent, "users")
	if len(docs) != 1 {
		t.Fatalf("expected 1 document (not subcollection docs), got %d", len(docs))
	}
}

func TestStoreConcurrentReadWrite(t *testing.T) {
	s := NewStore("")
	parent := "projects/test/databases/(default)/documents"

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(2)
		name := parent + "/col/" + string(rune('a'+i))
		go func() {
			defer wg.Done()
			s.CreateDocument(name, map[string]*firestorepb.Value{"i": intVal(int64(i))})
		}()
		go func() {
			defer wg.Done()
			s.ListDocuments(parent, "col")
		}()
	}
	wg.Wait()
}
