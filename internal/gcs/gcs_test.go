package gcs

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// testServer starts a GCS service on an ephemeral port and returns the base URL.
func testServer(t *testing.T) string {
	t.Helper()

	svc := New("", true) // in-memory, quiet
	ln, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	addr := fmt.Sprintf(":%d", port)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	go svc.Start(ctx, addr)

	// Wait for server to be ready.
	base := fmt.Sprintf("http://localhost:%d", port)
	for i := 0; i < 50; i++ {
		resp, err := http.Get(base + "/")
		if err == nil {
			resp.Body.Close()
			return base
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("server did not start")
	return ""
}

// --- Bucket tests ---

func TestCreateBucket(t *testing.T) {
	base := testServer(t)

	resp := postJSON(t, base+"/storage/v1/b?project=test", `{"name":"my-bucket"}`)
	assertStatus(t, resp, 200)

	var b Bucket
	decodeBody(t, resp, &b)

	if b.Name != "my-bucket" {
		t.Fatalf("expected name 'my-bucket', got %q", b.Name)
	}
	if b.Kind != "storage#bucket" {
		t.Fatalf("expected kind 'storage#bucket', got %q", b.Kind)
	}
}

func TestCreateDuplicateBucket(t *testing.T) {
	base := testServer(t)

	postJSON(t, base+"/storage/v1/b?project=test", `{"name":"dup"}`)
	resp := postJSON(t, base+"/storage/v1/b?project=test", `{"name":"dup"}`)
	assertStatus(t, resp, 409)
}

func TestGetBucket(t *testing.T) {
	base := testServer(t)

	postJSON(t, base+"/storage/v1/b?project=test", `{"name":"get-me"}`)

	resp, err := http.Get(base + "/storage/v1/b/get-me")
	if err != nil {
		t.Fatal(err)
	}
	assertStatus(t, resp, 200)

	var b Bucket
	decodeBody(t, resp, &b)
	if b.Name != "get-me" {
		t.Fatalf("expected 'get-me', got %q", b.Name)
	}
}

func TestGetBucketNotFound(t *testing.T) {
	base := testServer(t)

	resp, err := http.Get(base + "/storage/v1/b/nope")
	if err != nil {
		t.Fatal(err)
	}
	assertStatus(t, resp, 404)

	// Verify error envelope format.
	var errResp gcpError
	decodeBody(t, resp, &errResp)
	if errResp.Error.Code != 404 {
		t.Fatalf("expected error code 404, got %d", errResp.Error.Code)
	}
}

func TestListBuckets(t *testing.T) {
	base := testServer(t)

	postJSON(t, base+"/storage/v1/b?project=test", `{"name":"alpha"}`)
	postJSON(t, base+"/storage/v1/b?project=test", `{"name":"beta"}`)

	resp, err := http.Get(base + "/storage/v1/b?project=test")
	if err != nil {
		t.Fatal(err)
	}
	assertStatus(t, resp, 200)

	var list BucketList
	decodeBody(t, resp, &list)
	if len(list.Items) != 2 {
		t.Fatalf("expected 2 buckets, got %d", len(list.Items))
	}
	if list.Items[0].Name != "alpha" || list.Items[1].Name != "beta" {
		t.Fatalf("expected [alpha, beta], got [%s, %s]", list.Items[0].Name, list.Items[1].Name)
	}
}

func TestDeleteBucket(t *testing.T) {
	base := testServer(t)

	postJSON(t, base+"/storage/v1/b?project=test", `{"name":"del-me"}`)

	req, _ := http.NewRequest(http.MethodDelete, base+"/storage/v1/b/del-me", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	assertStatus(t, resp, 204)

	// Verify it's gone.
	resp2, _ := http.Get(base + "/storage/v1/b/del-me")
	assertStatus(t, resp2, 404)
}

func TestDeleteNonEmptyBucket(t *testing.T) {
	base := testServer(t)

	postJSON(t, base+"/storage/v1/b?project=test", `{"name":"has-stuff"}`)
	simpleUpload(t, base, "has-stuff", "file.txt", "hello")

	req, _ := http.NewRequest(http.MethodDelete, base+"/storage/v1/b/has-stuff", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	assertStatus(t, resp, 409)
}

// --- Object tests ---

func TestSimpleUploadAndDownload(t *testing.T) {
	base := testServer(t)

	postJSON(t, base+"/storage/v1/b?project=test", `{"name":"upload-test"}`)

	content := "hello, localgcp!"
	obj := simpleUpload(t, base, "upload-test", "greeting.txt", content)

	if obj.Name != "greeting.txt" {
		t.Fatalf("expected name 'greeting.txt', got %q", obj.Name)
	}
	if obj.Bucket != "upload-test" {
		t.Fatalf("expected bucket 'upload-test', got %q", obj.Bucket)
	}
	if obj.Size != fmt.Sprintf("%d", len(content)) {
		t.Fatalf("expected size %d, got %s", len(content), obj.Size)
	}

	// Download.
	resp, err := http.Get(base + "/storage/v1/b/upload-test/o/greeting.txt?alt=media")
	if err != nil {
		t.Fatal(err)
	}
	assertStatus(t, resp, 200)

	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != content {
		t.Fatalf("expected %q, got %q", content, string(body))
	}
}

func TestGetObjectMetadata(t *testing.T) {
	base := testServer(t)

	postJSON(t, base+"/storage/v1/b?project=test", `{"name":"meta-test"}`)
	simpleUpload(t, base, "meta-test", "doc.txt", "content")

	// Without alt=media, get metadata.
	resp, err := http.Get(base + "/storage/v1/b/meta-test/o/doc.txt")
	if err != nil {
		t.Fatal(err)
	}
	assertStatus(t, resp, 200)

	var obj Object
	decodeBody(t, resp, &obj)
	if obj.Kind != "storage#object" {
		t.Fatalf("expected kind 'storage#object', got %q", obj.Kind)
	}
	if obj.Md5Hash == "" {
		t.Fatal("expected non-empty md5Hash")
	}
}

func TestGetObjectNotFound(t *testing.T) {
	base := testServer(t)

	postJSON(t, base+"/storage/v1/b?project=test", `{"name":"empty-bucket"}`)

	resp, err := http.Get(base + "/storage/v1/b/empty-bucket/o/nope.txt?alt=media")
	if err != nil {
		t.Fatal(err)
	}
	assertStatus(t, resp, 404)
}

func TestDeleteObject(t *testing.T) {
	base := testServer(t)

	postJSON(t, base+"/storage/v1/b?project=test", `{"name":"del-obj"}`)
	simpleUpload(t, base, "del-obj", "gone.txt", "bye")

	req, _ := http.NewRequest(http.MethodDelete, base+"/storage/v1/b/del-obj/o/gone.txt", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	assertStatus(t, resp, 204)

	// Verify it's gone.
	resp2, _ := http.Get(base + "/storage/v1/b/del-obj/o/gone.txt?alt=media")
	assertStatus(t, resp2, 404)
}

func TestListObjects(t *testing.T) {
	base := testServer(t)

	postJSON(t, base+"/storage/v1/b?project=test", `{"name":"list-test"}`)
	simpleUpload(t, base, "list-test", "a.txt", "a")
	simpleUpload(t, base, "list-test", "b.txt", "b")
	simpleUpload(t, base, "list-test", "dir/c.txt", "c")

	resp, err := http.Get(base + "/storage/v1/b/list-test/o")
	if err != nil {
		t.Fatal(err)
	}
	assertStatus(t, resp, 200)

	var list ObjectList
	decodeBody(t, resp, &list)
	if len(list.Items) != 3 {
		t.Fatalf("expected 3 objects, got %d", len(list.Items))
	}
}

func TestListObjectsWithPrefixAndDelimiter(t *testing.T) {
	base := testServer(t)

	postJSON(t, base+"/storage/v1/b?project=test", `{"name":"prefix-test"}`)
	simpleUpload(t, base, "prefix-test", "photos/2024/jan.jpg", "j")
	simpleUpload(t, base, "prefix-test", "photos/2024/feb.jpg", "f")
	simpleUpload(t, base, "prefix-test", "photos/2025/mar.jpg", "m")
	simpleUpload(t, base, "prefix-test", "photos/root.jpg", "r")

	// List with prefix="photos/" and delimiter="/"
	// Should return root.jpg as item and "photos/2024/", "photos/2025/" as prefixes.
	resp, err := http.Get(base + "/storage/v1/b/prefix-test/o?prefix=photos/&delimiter=/")
	if err != nil {
		t.Fatal(err)
	}
	assertStatus(t, resp, 200)

	var list ObjectList
	decodeBody(t, resp, &list)

	if len(list.Items) != 1 {
		t.Fatalf("expected 1 item, got %d: %+v", len(list.Items), list.Items)
	}
	if list.Items[0].Name != "photos/root.jpg" {
		t.Fatalf("expected 'photos/root.jpg', got %q", list.Items[0].Name)
	}

	if len(list.Prefixes) != 2 {
		t.Fatalf("expected 2 prefixes, got %d: %v", len(list.Prefixes), list.Prefixes)
	}
	if list.Prefixes[0] != "photos/2024/" || list.Prefixes[1] != "photos/2025/" {
		t.Fatalf("unexpected prefixes: %v", list.Prefixes)
	}
}

func TestCopyObject(t *testing.T) {
	base := testServer(t)

	postJSON(t, base+"/storage/v1/b?project=test", `{"name":"src-bucket"}`)
	postJSON(t, base+"/storage/v1/b?project=test", `{"name":"dst-bucket"}`)
	simpleUpload(t, base, "src-bucket", "original.txt", "copy me")

	url := base + "/storage/v1/b/src-bucket/o/original.txt/copyTo/b/dst-bucket/o/copy.txt"
	resp := postJSON(t, url, "{}")
	assertStatus(t, resp, 200)

	var obj Object
	decodeBody(t, resp, &obj)
	if obj.Name != "copy.txt" || obj.Bucket != "dst-bucket" {
		t.Fatalf("unexpected copy result: %+v", obj)
	}

	// Verify content was copied.
	resp2, _ := http.Get(base + "/storage/v1/b/dst-bucket/o/copy.txt?alt=media")
	body, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()
	if string(body) != "copy me" {
		t.Fatalf("expected 'copy me', got %q", string(body))
	}
}

func TestMultipartUpload(t *testing.T) {
	base := testServer(t)

	postJSON(t, base+"/storage/v1/b?project=test", `{"name":"mp-test"}`)

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)

	// Part 1: JSON metadata.
	metaHeader := make(map[string][]string)
	metaHeader["Content-Type"] = []string{"application/json"}
	metaPart, _ := writer.CreatePart(metaHeader)
	metaPart.Write([]byte(`{"name":"multipart.txt","contentType":"text/plain"}`))

	// Part 2: file content.
	dataHeader := make(map[string][]string)
	dataHeader["Content-Type"] = []string{"text/plain"}
	dataPart, _ := writer.CreatePart(dataHeader)
	dataPart.Write([]byte("multipart content here"))

	writer.Close()

	req, _ := http.NewRequest(http.MethodPost,
		base+"/upload/storage/v1/b/mp-test/o?uploadType=multipart", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	assertStatus(t, resp, 200)

	var obj Object
	decodeBody(t, resp, &obj)
	if obj.Name != "multipart.txt" {
		t.Fatalf("expected 'multipart.txt', got %q", obj.Name)
	}

	// Verify download.
	resp2, _ := http.Get(base + "/storage/v1/b/mp-test/o/multipart.txt?alt=media")
	data, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()
	if string(data) != "multipart content here" {
		t.Fatalf("expected 'multipart content here', got %q", string(data))
	}
}

func TestResumableUpload(t *testing.T) {
	base := testServer(t)

	postJSON(t, base+"/storage/v1/b?project=test", `{"name":"resumable-test"}`)

	// Step 1: Initiate.
	initReq, _ := http.NewRequest(http.MethodPost,
		base+"/upload/storage/v1/b/resumable-test/o?uploadType=resumable&name=big.bin",
		strings.NewReader("{}"))
	initReq.Header.Set("Content-Type", "application/json")
	initReq.Header.Set("X-Upload-Content-Type", "application/octet-stream")

	initResp, err := http.DefaultClient.Do(initReq)
	if err != nil {
		t.Fatal(err)
	}
	assertStatus(t, initResp, 200)

	location := initResp.Header.Get("Location")
	if location == "" {
		t.Fatal("expected Location header")
	}

	// Step 2: Upload content.
	content := []byte("resumable upload content")
	putReq, _ := http.NewRequest(http.MethodPut, base+location, bytes.NewReader(content))
	putReq.Header.Set("Content-Type", "application/octet-stream")

	putResp, err := http.DefaultClient.Do(putReq)
	if err != nil {
		t.Fatal(err)
	}
	assertStatus(t, putResp, 200)

	var obj Object
	decodeBody(t, putResp, &obj)
	if obj.Name != "big.bin" {
		t.Fatalf("expected 'big.bin', got %q", obj.Name)
	}

	// Verify download.
	resp, _ := http.Get(base + "/storage/v1/b/resumable-test/o/big.bin?alt=media")
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(data) != "resumable upload content" {
		t.Fatalf("expected 'resumable upload content', got %q", string(data))
	}
}

func TestObjectWithSlashesInName(t *testing.T) {
	base := testServer(t)

	postJSON(t, base+"/storage/v1/b?project=test", `{"name":"slash-test"}`)
	simpleUpload(t, base, "slash-test", "path/to/deep/file.txt", "deep content")

	// Download using path with slashes.
	resp, err := http.Get(base + "/storage/v1/b/slash-test/o/path/to/deep/file.txt?alt=media")
	if err != nil {
		t.Fatal(err)
	}
	assertStatus(t, resp, 200)

	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(data) != "deep content" {
		t.Fatalf("expected 'deep content', got %q", string(data))
	}
}

func TestLargeObject(t *testing.T) {
	base := testServer(t)

	postJSON(t, base+"/storage/v1/b?project=test", `{"name":"large-test"}`)

	// 1MB object.
	content := bytes.Repeat([]byte("x"), 1<<20)
	req, _ := http.NewRequest(http.MethodPost,
		base+"/upload/storage/v1/b/large-test/o?uploadType=media&name=big.bin",
		bytes.NewReader(content))
	req.Header.Set("Content-Type", "application/octet-stream")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	assertStatus(t, resp, 200)

	// Download and verify size.
	resp2, _ := http.Get(base + "/storage/v1/b/large-test/o/big.bin?alt=media")
	data, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()
	if len(data) != 1<<20 {
		t.Fatalf("expected %d bytes, got %d", 1<<20, len(data))
	}
}

func TestErrorEnvelopeFormat(t *testing.T) {
	base := testServer(t)

	resp, err := http.Get(base + "/storage/v1/b/nonexistent")
	if err != nil {
		t.Fatal(err)
	}
	assertStatus(t, resp, 404)

	var errResp gcpError
	decodeBody(t, resp, &errResp)

	if errResp.Error.Code != 404 {
		t.Fatalf("expected code 404, got %d", errResp.Error.Code)
	}
	if len(errResp.Error.Errors) != 1 {
		t.Fatalf("expected 1 error detail, got %d", len(errResp.Error.Errors))
	}
	if errResp.Error.Errors[0].Reason != "notFound" {
		t.Fatalf("expected reason 'notFound', got %q", errResp.Error.Errors[0].Reason)
	}
	if errResp.Error.Errors[0].Domain != "global" {
		t.Fatalf("expected domain 'global', got %q", errResp.Error.Errors[0].Domain)
	}
}

// --- Helpers ---

func postJSON(t *testing.T, url, body string) *http.Response {
	t.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	return resp
}

func simpleUpload(t *testing.T, base, bucket, name, content string) Object {
	t.Helper()

	url := fmt.Sprintf("%s/upload/storage/v1/b/%s/o?uploadType=media&name=%s",
		base, bucket, name)

	req, _ := http.NewRequest(http.MethodPost, url, strings.NewReader(content))
	req.Header.Set("Content-Type", "text/plain")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("upload %s/%s: %v", bucket, name, err)
	}
	assertStatus(t, resp, 200)

	var obj Object
	decodeBody(t, resp, &obj)
	return obj
}

func assertStatus(t *testing.T, resp *http.Response, want int) {
	t.Helper()
	if resp.StatusCode != want {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected status %d, got %d: %s", want, resp.StatusCode, string(body))
	}
}

func decodeBody(t *testing.T, resp *http.Response, v interface{}) {
	t.Helper()
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		t.Fatalf("decode body: %v", err)
	}
}
