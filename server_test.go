package main

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
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

func sessionCookieOf(t *testing.T, r *http.Response) *http.Cookie {
	t.Helper()
	for _, c := range r.Cookies() {
		if c.Name == sessionCookie {
			return c
		}
	}
	t.Fatal("no session cookie")
	return nil
}

func TestWrongKeysAreLimited(t *testing.T) {
	ts := newTestServer(t)
	r, _ := call(t, ts, "POST", "/api/login", `{"key":"secret"}`, nil)
	cookie := map[string]string{"Cookie": sessionCookie + "=" + sessionCookieOf(t, r).Value}
	for i := 0; i < 10; i++ {
		if r, _ := call(t, ts, "GET", "/api/projects", "", map[string]string{"X-Api-Key": "guess"}); r.StatusCode != 401 {
			t.Fatalf("guess %d: %d", i, r.StatusCode)
		}
	}
	// Over the limit even the right key is refused, at login too...
	if r, _ := call(t, ts, "GET", "/api/projects", "", key); r.StatusCode != 429 || r.Header.Get("Retry-After") == "" {
		t.Fatalf("locked key: %d", r.StatusCode)
	}
	if r, _ := call(t, ts, "POST", "/api/login", `{"key":"secret"}`, nil); r.StatusCode != 429 {
		t.Fatalf("locked login: %d", r.StatusCode)
	}
	// ...but a signed-in browser keeps working.
	if r, _ := call(t, ts, "GET", "/api/projects", "", cookie); r.StatusCode != 200 {
		t.Fatalf("cookie while locked: %d", r.StatusCode)
	}
}

func TestFailLimiterCoolsDown(t *testing.T) {
	now := time.Unix(1e9, 0)
	l := newFailLimiter(3, time.Second, func() time.Time { return now })
	for i := 0; i < 3; i++ {
		l.fail("a")
	}
	if l.allow("a") || !l.allow("b") {
		t.Fatal("a should be locked, b not")
	}
	now = now.Add(time.Second)
	if !l.allow("a") {
		t.Fatal("one more try after a second")
	}
	l.fail("a")
	if l.allow("a") {
		t.Fatal("locked again")
	}
}

func TestSessionCookie(t *testing.T) {
	srv := NewServer(newTestStore(t), "secret")
	now := time.Unix(1e9, 0)
	srv.now = func() time.Time { return now }
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	r, _ := call(t, ts, "POST", "/api/login", `{"key":"secret"}`, nil)
	c := sessionCookieOf(t, r)
	if c.Secure || c.SameSite != http.SameSiteStrictMode {
		t.Fatalf("cookie flags: %+v", c)
	}
	cookie := map[string]string{"Cookie": sessionCookie + "=" + c.Value}

	// Not renewed while fresh; renewed once half the TTL is gone.
	if r, _ := call(t, ts, "GET", "/api/projects", "", cookie); r.StatusCode != 200 || len(r.Cookies()) != 0 {
		t.Fatalf("fresh: %d %v", r.StatusCode, r.Cookies())
	}
	now = now.Add(sessionTTL/2 + time.Hour)
	r, _ = call(t, ts, "GET", "/api/projects", "", cookie)
	if r.StatusCode != 200 {
		t.Fatalf("half way: %d", r.StatusCode)
	}
	renewed := sessionCookieOf(t, r)
	now = now.Add(sessionTTL / 2)
	if r, _ := call(t, ts, "GET", "/api/projects", "", cookie); r.StatusCode != 401 {
		t.Fatalf("expired: %d", r.StatusCode)
	}
	if r, _ := call(t, ts, "GET", "/api/projects", "", map[string]string{"Cookie": sessionCookie + "=" + renewed.Value}); r.StatusCode != 200 {
		t.Fatalf("renewed: %d", r.StatusCode)
	}

	// A cookie with a later expiry but the old MAC is forged.
	exp, mac, _ := strings.Cut(renewed.Value, ".")
	n, _ := strconv.ParseInt(exp, 10, 64)
	forged := strconv.FormatInt(n+3600, 10) + "." + mac
	if r, _ := call(t, ts, "GET", "/api/projects", "", map[string]string{"Cookie": sessionCookie + "=" + forged}); r.StatusCode != 401 {
		t.Fatalf("forged expiry: %d", r.StatusCode)
	}
	// Behind a TLS proxy the cookie is Secure.
	r, _ = call(t, ts, "POST", "/api/login", `{"key":"secret"}`, map[string]string{"X-Forwarded-Proto": "https"})
	if !sessionCookieOf(t, r).Secure {
		t.Fatal("not Secure behind https")
	}
}

func TestCrossOriginRefused(t *testing.T) {
	ts := newTestServer(t)
	r, _ := call(t, ts, "POST", "/api/login", `{"key":"secret"}`, nil)
	cookie := sessionCookie + "=" + sessionCookieOf(t, r).Value
	body := `{"key":"APP","name":"App"}`
	if r, _ := call(t, ts, "POST", "/api/projects", body, map[string]string{"Cookie": cookie, "Sec-Fetch-Site": "same-site"}); r.StatusCode != 403 {
		t.Fatalf("same-site, other origin: %d", r.StatusCode)
	}
	if r, _ := call(t, ts, "POST", "/api/projects", body, map[string]string{"Cookie": cookie, "Origin": "http://evil.example"}); r.StatusCode != 403 {
		t.Fatalf("foreign origin: %d", r.StatusCode)
	}
	if r, _ := call(t, ts, "GET", "/api/projects", "", map[string]string{"Cookie": cookie, "Sec-Fetch-Site": "cross-site"}); r.StatusCode != 200 {
		t.Fatalf("cross-site read: %d", r.StatusCode)
	}
	if r, b := call(t, ts, "POST", "/api/projects", body, map[string]string{"Cookie": cookie, "Sec-Fetch-Site": "same-origin", "Origin": ts.URL}); r.StatusCode != 200 {
		t.Fatalf("same origin: %d %s", r.StatusCode, b)
	}
	// The API key is not sent by browsers on their own, so it needs no check.
	if r, _ := call(t, ts, "DELETE", "/api/projects/APP", "", map[string]string{"X-Api-Key": "secret", "Origin": "http://evil.example"}); r.StatusCode != 204 {
		t.Fatalf("key from elsewhere: %d", r.StatusCode)
	}
}

func TestSecurityHeaders(t *testing.T) {
	ts := newTestServer(t)
	r, _ := call(t, ts, "GET", "/", "", nil)
	if !strings.Contains(r.Header.Get("Content-Security-Policy"), "frame-ancestors 'none'") || r.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("headers: %v", r.Header)
	}
	if r, _ := call(t, ts, "GET", "/api/projects", "", key); r.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("api cache: %q", r.Header.Get("Cache-Control"))
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
	_, b = call(t, ts, "GET", "/api/search?q=do+it", "", key)
	var hits []SearchHit
	json.Unmarshal([]byte(b), &hits)
	if len(hits) != 1 || hits[0].Key != "APP-1" || hits[0].Project != "APP" {
		t.Fatalf("search: %s", b)
	}
	if r, _ := call(t, ts, "GET", "/api/issues/APP-404", "", key); r.StatusCode != 404 {
		t.Fatalf("missing: %d", r.StatusCode)
	}
	if r, _ := call(t, ts, "DELETE", "/api/issues/APP-1", "", key); r.StatusCode != 204 {
		t.Fatalf("delete: %d", r.StatusCode)
	}
}

func TestSaveOverHTTP(t *testing.T) {
	store := newTestStore(t)
	store.SetSaveDelay(time.Hour)
	ts := httptest.NewServer(NewServer(store, "secret"))
	t.Cleanup(ts.Close)
	if r, _ := call(t, ts, "GET", "/api/projects", "", key); r.Header.Get("X-Saved") != "true" || r.Header.Get("X-Save-In") != "" {
		t.Fatalf("nothing changed: X-Saved %q, X-Save-In %q", r.Header.Get("X-Saved"), r.Header.Get("X-Save-In"))
	}
	// The change waits for the save delay; the response says so.
	r, _ := call(t, ts, "POST", "/api/projects", `{"key":"APP","name":"App"}`, key)
	if ms, err := strconv.Atoi(r.Header.Get("X-Save-In")); r.Header.Get("X-Saved") != "false" || err != nil || ms <= 0 || ms > 3600000 {
		t.Fatalf("pending: X-Saved %q, X-Save-In %q", r.Header.Get("X-Saved"), r.Header.Get("X-Save-In"))
	}
	if r, _ := call(t, ts, "GET", "/api/save", "", key); r.StatusCode != 204 || r.Header.Get("X-Saved") != "false" {
		t.Fatalf("ask: %d %q", r.StatusCode, r.Header.Get("X-Saved"))
	}
	if r, _ := call(t, ts, "POST", "/api/save", "", nil); r.StatusCode != 401 {
		t.Fatalf("save without key: %d", r.StatusCode)
	}
	if r, b := call(t, ts, "POST", "/api/save", "", key); r.StatusCode != 204 || r.Header.Get("X-Saved") != "true" {
		t.Fatalf("save: %d %q %s", r.StatusCode, r.Header.Get("X-Saved"), b)
	}
	if _, err := os.Stat(store.path); err != nil {
		t.Fatalf("not written: %v", err)
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
