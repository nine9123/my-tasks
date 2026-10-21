package main

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(NewServer(newTestStore(t), "secret"))
	t.Cleanup(ts.Close)
	return ts
}

func call(t *testing.T, ts *httptest.Server, method, path, body string, hdr map[string]string) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest(method, ts.URL+path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, string(b)
}

var key = map[string]string{"X-Api-Key": "secret"}

func TestAuth(t *testing.T) {
	ts := newTestServer(t)
	if r, _ := call(t, ts, "GET", "/api/projects", "", nil); r.StatusCode != 401 {
		t.Fatalf("no key: %d", r.StatusCode)
	}
	if r, _ := call(t, ts, "GET", "/api/projects", "", map[string]string{"X-Api-Key": "wrong"}); r.StatusCode != 401 {
		t.Fatalf("wrong key: %d", r.StatusCode)
	}
	if r, _ := call(t, ts, "GET", "/api/projects", "", key); r.StatusCode != 200 {
		t.Fatalf("header key: %d", r.StatusCode)
	}
	if r, _ := call(t, ts, "GET", "/api/projects", "", map[string]string{"Authorization": "Bearer secret"}); r.StatusCode != 200 {
		t.Fatalf("bearer: %d", r.StatusCode)
	}
	// Static files and the health check need no key.
	if r, _ := call(t, ts, "GET", "/healthz", "", nil); r.StatusCode != 200 {
		t.Fatalf("healthz: %d", r.StatusCode)
	}
	if r, body := call(t, ts, "GET", "/", "", nil); r.StatusCode != 200 || !strings.Contains(body, "app.js") {
		t.Fatalf("index: %d", r.StatusCode)
	}

	// Login sets a cookie that works instead of the key.
	if r, _ := call(t, ts, "POST", "/api/login", `{"key":"nope"}`, nil); r.StatusCode != 401 {
		t.Fatalf("bad login: %d", r.StatusCode)
	}
	r, _ := call(t, ts, "POST", "/api/login", `{"key":"secret"}`, nil)
	var cookie *http.Cookie
	for _, c := range r.Cookies() {
		if c.Name == sessionCookie {
			cookie = c
		}
	}
	if cookie == nil || !cookie.HttpOnly {
		t.Fatal("no session cookie")
	}
	if r, _ := call(t, ts, "GET", "/api/projects", "", map[string]string{"Cookie": cookie.Name + "=" + cookie.Value}); r.StatusCode != 200 {
		t.Fatalf("cookie: %d", r.StatusCode)
	}
	if r, _ := call(t, ts, "GET", "/api/projects", "", map[string]string{"Cookie": cookie.Name + "=forged"}); r.StatusCode != 401 {
		t.Fatalf("forged cookie: %d", r.StatusCode)
	}
}

func TestIssueFlowOverHTTP(t *testing.T) {
	ts := newTestServer(t)
	if r, b := call(t, ts, "POST", "/api/projects", `{"key":"APP","name":"App"}`, key); r.StatusCode != 200 {
		t.Fatalf("create project: %d %s", r.StatusCode, b)
	}
	r, b := call(t, ts, "POST", "/api/projects/APP/releases", `{"name":"1.0"}`, key)
	var rel Release
	json.Unmarshal([]byte(b), &rel)
	if r.StatusCode != 200 || rel.ID == 0 {
		t.Fatalf("create release: %d %s", r.StatusCode, b)
	}
	body := `{"summary":"Do it","type":"Bug","priority":"High","description":"long text","release":` + jsonInt(rel.ID) + `}`
	if r, b := call(t, ts, "POST", "/api/projects/APP/issues", body, key); r.StatusCode != 200 {
		t.Fatalf("create issue: %d %s", r.StatusCode, b)
	}
	if r, _ := call(t, ts, "POST", "/api/projects/APP/issues", `{"summary":" "}`, key); r.StatusCode != 400 {
		t.Fatalf("empty summary: %d", r.StatusCode)
	}
	if r, _ := call(t, ts, "PATCH", "/api/issues/APP-1", `{"status":2}`, key); r.StatusCode != 200 {
		t.Fatalf("patch: %d", r.StatusCode)
	}
	_, b = call(t, ts, "GET", "/api/projects/APP/issues?release="+jsonInt(rel.ID), "", key)
	var list []Issue
	json.Unmarshal([]byte(b), &list)
	if len(list) != 1 || list[0].StatusID != 2 || list[0].Description != "" {
		t.Fatalf("list: %s", b)
	}
	_, b = call(t, ts, "GET", "/api/issues/APP-1", "", key)
	if !strings.Contains(b, "long text") {
		t.Fatalf("issue: %s", b)
	}
	if r, _ := call(t, ts, "POST", "/api/issues/APP-1/comments", `{"body":"hi"}`, key); r.StatusCode != 200 {
		t.Fatalf("comment: %d", r.StatusCode)
	}
	if r, _ := call(t, ts, "GET", "/api/issues/APP-404", "", key); r.StatusCode != 404 {
		t.Fatalf("missing: %d", r.StatusCode)
	}
	if r, _ := call(t, ts, "DELETE", "/api/issues/APP-1", "", key); r.StatusCode != 204 {
		t.Fatalf("delete: %d", r.StatusCode)
	}
}

func jsonInt(n int) string { b, _ := json.Marshal(n); return string(b) }

func TestImportOverHTTP(t *testing.T) {
	ts := newTestServer(t)
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for name, src := range map[string]string{"a.xml": exportA, "b.xml": exportB} {
		w, _ := mw.CreateFormFile("file", name)
		io.WriteString(w, src)
	}
	mw.Close()
	req, _ := http.NewRequest("POST", ts.URL+"/api/import", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("X-Api-Key", "secret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var res ImportResult
	json.NewDecoder(resp.Body).Decode(&res)
	if resp.StatusCode != 200 || res.Files != 2 || res.IssuesCreated != 4 {
		t.Fatalf("import: %d %+v", resp.StatusCode, res)
	}
	// Both files in one go: the epic link resolves without a warning.
	for _, w := range res.Warnings {
		if strings.Contains(w, "epic") {
			t.Fatalf("unexpected warning: %s", w)
		}
	}
}
