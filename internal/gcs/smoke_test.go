package gcs

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

// TestSmokeEndToEnd runs a full lifecycle: create bucket, upload, download,
// copy, delete, list, large upload, error cases.
func TestSmokeEndToEnd(t *testing.T) {
	base := testServer(t)

	// 1. Create bucket.
	resp := postJSON(t, base+"/storage/v1/b?project=test", `{"name":"smoke"}`)
	assertStatus(t, resp, 200)
	resp.Body.Close()

	// 2. Upload object.
	obj := simpleUpload(t, base, "smoke", "hello.txt", "Hello from localgcp!")
	if obj.Size != "20" {
		t.Fatalf("expected size 20, got %s", obj.Size)
	}

	// 3. Download and verify content.
	resp, _ = http.Get(base + "/storage/v1/b/smoke/o/hello.txt?alt=media")
	assertStatus(t, resp, 200)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "Hello from localgcp!" {
		t.Fatalf("download mismatch: %q", string(body))
	}

	// 4. Copy object.
	resp = postJSON(t, base+"/storage/v1/b/smoke/o/hello.txt/copyTo/b/smoke/o/hello-copy.txt", "{}")
	assertStatus(t, resp, 200)
	resp.Body.Close()

	// 5. List objects — should have 2.
	resp, _ = http.Get(base + "/storage/v1/b/smoke/o")
	assertStatus(t, resp, 200)
	var list ObjectList
	decodeBody(t, resp, &list)
	if len(list.Items) != 2 {
		t.Fatalf("expected 2 objects, got %d", len(list.Items))
	}

	// 6. Delete original.
	req, _ := http.NewRequest(http.MethodDelete, base+"/storage/v1/b/smoke/o/hello.txt", nil)
	resp, _ = http.DefaultClient.Do(req)
	assertStatus(t, resp, 204)
	resp.Body.Close()

	// 7. Verify deleted — 404.
	resp, _ = http.Get(base + "/storage/v1/b/smoke/o/hello.txt?alt=media")
	assertStatus(t, resp, 404)
	resp.Body.Close()

	// 8. Copy still exists.
	resp, _ = http.Get(base + "/storage/v1/b/smoke/o/hello-copy.txt?alt=media")
	assertStatus(t, resp, 200)
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "Hello from localgcp!" {
		t.Fatalf("copy content mismatch: %q", string(body))
	}

	// 9. Large upload (1MB).
	bigContent := bytes.Repeat([]byte("X"), 1<<20)
	req, _ = http.NewRequest(http.MethodPost,
		fmt.Sprintf("%s/upload/storage/v1/b/smoke/o?uploadType=media&name=big.bin", base),
		bytes.NewReader(bigContent))
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, _ = http.DefaultClient.Do(req)
	assertStatus(t, resp, 200)
	resp.Body.Close()

	// Download large object and verify size.
	resp, _ = http.Get(base + "/storage/v1/b/smoke/o/big.bin?alt=media")
	assertStatus(t, resp, 200)
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if len(data) != 1<<20 {
		t.Fatalf("large object: expected %d bytes, got %d", 1<<20, len(data))
	}

	// 10. Upload to non-existent bucket — 404.
	req, _ = http.NewRequest(http.MethodPost,
		base+"/upload/storage/v1/b/no-such-bucket/o?uploadType=media&name=fail.txt",
		strings.NewReader("fail"))
	req.Header.Set("Content-Type", "text/plain")
	resp, _ = http.DefaultClient.Do(req)
	assertStatus(t, resp, 404)
	resp.Body.Close()

	// 11. Create bucket with no name — 400.
	resp = postJSON(t, base+"/storage/v1/b?project=test", `{}`)
	assertStatus(t, resp, 400)
	resp.Body.Close()
}
