package phased

import (
	"strings"
	"testing"
)

// Phase D1: workspace files through the API.
func TestFilesAPI(t *testing.T) {
	e := newEnv(t)
	ada, grace := e.register("ada"), e.register("grace")

	f := e.must(e.upload(ada.token, ada.workspace, `C:\docs\Plan <v2>.md`, []byte("# Plan\nShip it")), 201)
	id := f["id"].(string)
	if f["name"] != "Plan <v2>.md" || f["content_type"] != "text/markdown" || f["size"] != 14.0 || f["workspace_id"] != ada.workspace {
		t.Fatalf("uploaded: %v", f)
	}
	html := e.must(e.upload(ada.token, ada.workspace, "page.html", []byte("<script>alert(1)</script>")), 201)

	list := e.must(e.call(ada.token, "GET", "/api/v1/files?workspace_id="+ada.workspace, nil), 200)["items"].([]any)
	if len(list) != 2 || list[0].(map[string]any)["id"] != html["id"] { // newest first
		t.Fatalf("list: %v", list)
	}
	if got := e.must(e.call(ada.token, "GET", "/api/v1/files/"+id, nil), 200); got["name"] != "Plan <v2>.md" {
		t.Fatalf("get: %v", got)
	}
	d := e.call(ada.token, "GET", "/api/v1/files/"+id+"/content", nil)
	if d.status != 200 || string(d.body) != "# Plan\nShip it" || d.header.Get("Content-Type") != "text/markdown" ||
		d.header.Get("Content-Disposition") != "attachment; filename*=UTF-8''Plan%20%3Cv2%3E.md" || d.header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("download: %d %q %v", d.status, d.body, d.header)
	}
	// Uploaded HTML is never served as a page of this site.
	h := e.call(ada.token, "GET", "/api/v1/files/"+html["id"].(string)+"/content", nil)
	if h.header.Get("Content-Type") != "application/octet-stream" || !strings.Contains(h.header.Get("Content-Security-Policy"), "sandbox") {
		t.Fatalf("html download: %v", h.header)
	}

	// Another workspace's member sees nothing.
	for _, path := range []string{"/api/v1/files/" + id, "/api/v1/files/" + id + "/content"} {
		if r := e.call(grace.token, "GET", path, nil); r.status != 404 {
			t.Fatalf("%s for another workspace: %d", path, r.status)
		}
	}
	if r := e.call(grace.token, "DELETE", "/api/v1/files/"+id, nil); r.status != 404 {
		t.Fatalf("delete by another workspace: %d", r.status)
	}
	if r := e.call(grace.token, "GET", "/api/v1/files?workspace_id="+ada.workspace, nil); r.status != 404 {
		t.Fatalf("list another workspace: %d", r.status)
	}
	if r := e.upload(grace.token, ada.workspace, "x.txt", []byte("x")); r.status != 404 {
		t.Fatalf("upload into another workspace: %d", r.status)
	}

	// Bad uploads.
	for name, r := range map[string]resp{
		"no file":      e.upload(ada.token, ada.workspace, "", nil),
		"no workspace": e.upload(ada.token, "", "a.txt", []byte("a")),
		"empty":        e.upload(ada.token, ada.workspace, "a.txt", nil),
		"no session":   e.upload("", ada.workspace, "a.txt", []byte("a")),
	} {
		if r.status != 400 && !(name == "no session" && r.status == 401) {
			t.Fatalf("%s: %d %s", name, r.status, r.body)
		}
	}
	if r := e.upload(ada.token, ada.workspace, "big.bin", make([]byte, 20<<20+1)); r.status != 413 {
		t.Fatalf("too large: %d %s", r.status, r.body)
	}

	if r := e.call(ada.token, "DELETE", "/api/v1/files/"+id, nil); r.status != 204 {
		t.Fatalf("delete: %d", r.status)
	}
	if r := e.call(ada.token, "GET", "/api/v1/files/"+id, nil); r.status != 404 {
		t.Fatalf("after delete: %d", r.status)
	}
	// The node catalog offers Extract Text with a file picker.
	for _, n := range e.must(e.call(ada.token, "GET", "/api/v1/nodes", nil), 200)["items"].([]any) {
		if d := n.(map[string]any); d["type"] == "extract_text" {
			if f := d["config"].([]any)[0].(map[string]any); f["options_source"] != "files" {
				t.Fatalf("extract_text file field: %v", f)
			}
			return
		}
	}
	t.Fatal("extract_text is not in the catalog")
}
