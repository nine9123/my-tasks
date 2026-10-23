package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed web
var webFS embed.FS

const sessionCookie = "mt_session"

// sessionTTL is how long a sign-in lasts without use; using the tracker
// renews it (see auth).
const sessionTTL = 30 * 24 * time.Hour

type Server struct {
	store   *Store
	apiKey  string
	keyHash [sha256.Size]byte
	limiter *failLimiter
	now     func() time.Time
	mux     *http.ServeMux
}

func NewServer(store *Store, apiKey string) *Server {
	s := &Server{store: store, apiKey: apiKey, keyHash: sha256.Sum256([]byte(apiKey)), now: time.Now, mux: http.NewServeMux()}
	s.limiter = newFailLimiter(10, 6*time.Second, func() time.Time { return s.now() })
	s.routes()
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	securityHeaders(w, r)
	s.mux.ServeHTTP(w, r)
}

func (s *Server) routes() {
	m := s.mux
	static, _ := fs.Sub(webFS, "web")
	m.Handle("GET /", http.FileServer(http.FS(static)))
	m.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") })
	m.HandleFunc("POST /api/login", s.login)
	m.HandleFunc("POST /api/logout", s.logout)

	api := func(pattern string, h func(*http.Request) (any, error)) {
		m.HandleFunc(pattern, s.auth(func(w http.ResponseWriter, r *http.Request) {
			v, err := h(r)
			s.saveHeaders(w)
			if err != nil {
				writeError(w, err)
				return
			}
			if v == nil {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			writeJSON(w, http.StatusOK, v)
		}))
	}

	api("GET /api/meta", func(r *http.Request) (any, error) {
		return map[string]any{"types": IssueTypes, "priorities": Priorities, "linkTypes": LinkTypes}, nil
	})

	api("GET /api/search", func(r *http.Request) (any, error) {
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		if limit <= 0 || limit > 200 {
			limit = 20
		}
		return s.store.Search(r.URL.Query().Get("q"), limit), nil
	})

	api("GET /api/projects", func(r *http.Request) (any, error) { return s.store.Projects(), nil })
	api("POST /api/projects", func(r *http.Request) (any, error) {
		var in struct{ Key, Name, Description string }
		if err := decode(r, &in); err != nil {
			return nil, err
		}
		return s.store.CreateProject(in.Key, in.Name, in.Description)
	})
	api("GET /api/projects/{key}", func(r *http.Request) (any, error) { return s.store.Project(r.PathValue("key")) })
	api("PATCH /api/projects/{key}", func(r *http.Request) (any, error) {
		var in struct{ Name, Description *string }
		if err := decode(r, &in); err != nil {
			return nil, err
		}
		return s.store.UpdateProject(r.PathValue("key"), in.Name, in.Description)
	})
	api("POST /api/projects/{key}/hide-done", func(r *http.Request) (any, error) {
		return s.store.HideDone(r.PathValue("key"), true)
	})
	api("DELETE /api/projects/{key}/hide-done", func(r *http.Request) (any, error) {
		return s.store.HideDone(r.PathValue("key"), false)
	})
	api("DELETE /api/projects/{key}", func(r *http.Request) (any, error) {
		return nil, s.store.DeleteProject(r.PathValue("key"))
	})
	api("PUT /api/projects/{key}/statuses", func(r *http.Request) (any, error) {
		var in []Status
		if err := decode(r, &in); err != nil {
			return nil, err
		}
		return s.store.SetStatuses(r.PathValue("key"), in)
	})

	api("GET /api/projects/{key}/releases", func(r *http.Request) (any, error) {
		return s.store.Releases(r.PathValue("key"))
	})
	api("POST /api/projects/{key}/releases", func(r *http.Request) (any, error) {
		var in ReleaseInput
		if err := decode(r, &in); err != nil {
			return nil, err
		}
		return s.store.CreateRelease(r.PathValue("key"), in)
	})
	api("PATCH /api/releases/{id}", func(r *http.Request) (any, error) {
		var in ReleaseInput
		if err := decode(r, &in); err != nil {
			return nil, err
		}
		return s.store.UpdateRelease(pathID(r), in)
	})
	api("DELETE /api/releases/{id}", func(r *http.Request) (any, error) {
		return nil, s.store.DeleteRelease(pathID(r))
	})

	api("GET /api/projects/{key}/issues", func(r *http.Request) (any, error) {
		q := r.URL.Query()
		f := IssueFilter{Type: q.Get("type"), Text: q.Get("q")}
		f.Status, _ = strconv.Atoi(q.Get("status"))
		f.Release, _ = strconv.Atoi(q.Get("release"))
		f.Epic, _ = strconv.Atoi(q.Get("epic"))
		is, err := s.store.Issues(r.PathValue("key"), f)
		if err != nil {
			return nil, err
		}
		// Lists don't need descriptions; the issue page loads them.
		for k := range is {
			is[k].Description = ""
		}
		return is, nil
	})
	api("POST /api/projects/{key}/issues", func(r *http.Request) (any, error) {
		var in IssueInput
		if err := decode(r, &in); err != nil {
			return nil, err
		}
		return s.store.CreateIssue(r.PathValue("key"), in)
	})
	api("GET /api/issues/{key}", func(r *http.Request) (any, error) { return s.store.Issue(r.PathValue("key")) })
	api("PATCH /api/issues/{key}", func(r *http.Request) (any, error) {
		var in IssueInput
		if err := decode(r, &in); err != nil {
			return nil, err
		}
		return s.store.UpdateIssue(r.PathValue("key"), in)
	})
	api("DELETE /api/issues/{key}", func(r *http.Request) (any, error) {
		return nil, s.store.DeleteIssue(r.PathValue("key"))
	})
	api("POST /api/issues/{key}/rank", func(r *http.Request) (any, error) {
		var in struct{ Before, After string }
		if err := decode(r, &in); err != nil {
			return nil, err
		}
		return s.store.RankIssue(r.PathValue("key"), in.Before, in.After)
	})

	api("GET /api/issues/{key}/comments", func(r *http.Request) (any, error) {
		return s.store.Comments(r.PathValue("key"))
	})
	api("POST /api/issues/{key}/comments", func(r *http.Request) (any, error) {
		var in struct{ Body string }
		if err := decode(r, &in); err != nil {
			return nil, err
		}
		return s.store.AddComment(r.PathValue("key"), in.Body)
	})
	api("PATCH /api/comments/{id}", func(r *http.Request) (any, error) {
		var in struct{ Body string }
		if err := decode(r, &in); err != nil {
			return nil, err
		}
		return s.store.UpdateComment(pathID(r), in.Body)
	})
	api("DELETE /api/comments/{id}", func(r *http.Request) (any, error) {
		return nil, s.store.DeleteComment(pathID(r))
	})

	api("GET /api/issues/{key}/links", func(r *http.Request) (any, error) {
		return s.store.Links(r.PathValue("key"))
	})
	api("POST /api/issues/{key}/links", func(r *http.Request) (any, error) {
		var in struct {
			Type   string
			Issue  string
			Inward bool
		}
		if err := decode(r, &in); err != nil {
			return nil, err
		}
		return s.store.AddLink(r.PathValue("key"), in.Type, strings.ToUpper(strings.TrimSpace(in.Issue)), in.Inward)
	})
	api("DELETE /api/links/{id}", func(r *http.Request) (any, error) {
		return nil, s.store.DeleteLink(pathID(r))
	})

	api("POST /api/import", s.importJira)

	// The header's save button: POST writes what the save delay holds back
	// now, GET only asks. Every API response carries the save state in
	// X-Saved and X-Save-In (see saveHeaders); these too, after the write.
	api("POST /api/save", func(r *http.Request) (any, error) { return nil, s.store.Flush() })
	api("GET /api/save", func(r *http.Request) (any, error) { return nil, nil })
}

// saveHeaders tells the client whether everything is on disk (X-Saved:
// true) or changes are still waiting for the save delay (X-Saved: false,
// X-Save-In: milliseconds until they are written).
func (s *Server) saveHeaders(w http.ResponseWriter) {
	unsaved, in := s.store.Unsaved()
	w.Header().Set("X-Saved", strconv.FormatBool(!unsaved))
	if unsaved {
		w.Header().Set("X-Save-In", strconv.FormatInt(in.Milliseconds(), 10))
	}
}

// importJira takes one or more Jira XML exports as multipart "file" fields.
func (s *Server) importJira(r *http.Request) (any, error) {
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		return nil, invalid("expected a multipart upload: %v", err)
	}
	defer r.MultipartForm.RemoveAll()
	var files [][]jiraItem
	for _, fh := range r.MultipartForm.File["file"] {
		f, err := fh.Open()
		if err != nil {
			return nil, err
		}
		items, err := ParseJiraXML(f)
		f.Close()
		if err != nil {
			return nil, invalid("%s: %v", fh.Filename, err)
		}
		files = append(files, items)
	}
	if len(files) == 0 {
		return nil, invalid("no files uploaded")
	}
	return s.store.ImportJira(files)
}

// keyMatches compares in constant time. Hashing both sides first keeps the
// comparison from returning early on a length mismatch, which would tell an
// attacker how long the key is.
func (s *Server) keyMatches(key string) bool {
	h := sha256.Sum256([]byte(key))
	return subtle.ConstantTimeCompare(h[:], s.keyHash[:]) == 1
}

// requestKey is the API key a request presents, if any.
func requestKey(r *http.Request) string {
	if key := r.Header.Get("X-Api-Key"); key != "" {
		return key
	}
	if a := r.Header.Get("Authorization"); strings.HasPrefix(a, "Bearer ") {
		return strings.TrimPrefix(a, "Bearer ")
	}
	return ""
}

// The session cookie is "<expiry>.<HMAC of the expiry under the API key>":
// nothing is kept server-side, so a session survives a restart (idlegate),
// yet a cookie stops working when it expires or the API key changes.
func (s *Server) sessionValue(exp time.Time) string {
	e := strconv.FormatInt(exp.Unix(), 10)
	mac := hmac.New(sha256.New, []byte(s.apiKey))
	mac.Write([]byte("my-tasks-session|" + e))
	return e + "." + hex.EncodeToString(mac.Sum(nil))
}

// sessionExpiry returns when a valid session cookie value expires, or false.
func (s *Server) sessionExpiry(v string) (time.Time, bool) {
	e, _, ok := strings.Cut(v, ".")
	n, err := strconv.ParseInt(e, 10, 64)
	if !ok || err != nil {
		return time.Time{}, false
	}
	exp := time.Unix(n, 0)
	if !s.now().Before(exp) || !hmac.Equal([]byte(v), []byte(s.sessionValue(exp))) {
		return time.Time{}, false
	}
	return exp, true
}

func (s *Server) setSession(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: s.sessionValue(s.now().Add(sessionTTL)), Path: "/",
		HttpOnly: true, Secure: isHTTPS(r), SameSite: http.SameSiteStrictMode, MaxAge: int(sessionTTL / time.Second)})
}

// isHTTPS: served over TLS, directly or by a proxy in front. Over plain HTTP
// (the usual NAS setup) a Secure cookie would never be sent back.
func isHTTPS(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// sameOrigin rejects a cookie-authenticated change sent from another site.
// SameSite=Strict alone doesn't cover that: another app on the same NAS
// host (any port) counts as the same site. Browsers send Sec-Fetch-Site;
// older ones only Origin. A request with neither isn't from a browser page.
func sameOrigin(r *http.Request) bool {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" {
		return site == "same-origin" || site == "none"
	}
	if o := r.Header.Get("Origin"); o != "" {
		u, err := url.Parse(o)
		if err != nil {
			return false
		}
		return u.Host == r.Host || u.Host == r.Header.Get("X-Forwarded-Host")
	}
	return true
}

func (s *Server) auth(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if key := requestKey(r); key != "" {
			if !s.checkKey(w, r, key) {
				return
			}
			h(w, r)
			return
		}
		// The cookie needs no attempt limit: it is a 256-bit MAC, not
		// guessable, and leaving it out keeps the owner signed in while
		// someone hammers the key.
		c, err := r.Cookie(sessionCookie)
		exp, ok := time.Time{}, false
		if err == nil {
			exp, ok = s.sessionExpiry(c.Value)
		}
		if !ok {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		if !sameOrigin(r) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "cross-origin request refused"})
			return
		}
		// Sliding expiry: a session in use is renewed once half of it is gone.
		if exp.Sub(s.now()) < sessionTTL/2 {
			s.setSession(w, r)
		}
		h(w, r)
	}
}

// checkKey checks an API key against the failed-attempt limit, writing the
// error response if it is refused.
func (s *Server) checkKey(w http.ResponseWriter, r *http.Request, key string) bool {
	ip := clientIP(r)
	if !s.limiter.allow(ip) {
		w.Header().Set("Retry-After", strconv.Itoa(int(s.limiter.every/time.Second)))
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "too many wrong API keys, try again later"})
		return false
	}
	if !s.keyMatches(key) {
		s.limiter.fail(ip)
		log.Printf("auth: wrong API key from %s", ip)
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "wrong API key"})
		return false
	}
	return true
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var in struct{ Key string }
	if err := decode(r, &in); err != nil {
		writeError(w, err)
		return
	}
	if !s.checkKey(w, r, in.Key) {
		return
	}
	s.setSession(w, r)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", HttpOnly: true,
		Secure: isHTTPS(r), SameSite: http.SameSiteStrictMode, MaxAge: -1})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// clientIP is the peer address. X-Forwarded-For is not trusted: anyone can
// send it. Behind a proxy (idlegate) every request shares the proxy's address,
// so the attempt limit is effectively global there.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// failLimiter allows burst wrong keys per client, then one more per every.
// A client over the limit is refused without its key being checked, so
// guessing a key runs at a few hundred tries an hour at most.
type failLimiter struct {
	mu    sync.Mutex
	burst float64
	every time.Duration
	now   func() time.Time
	fails map[string]failCount
}

type failCount struct {
	n  float64 // failures, decaying by one per every
	at time.Time
}

func newFailLimiter(burst int, every time.Duration, now func() time.Time) *failLimiter {
	return &failLimiter{burst: float64(burst), every: every, now: now, fails: map[string]failCount{}}
}

func (l *failLimiter) level(ip string, now time.Time) float64 {
	f, ok := l.fails[ip]
	if !ok {
		return 0
	}
	return max(0, f.n-float64(now.Sub(f.at))/float64(l.every))
}

func (l *failLimiter) allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.level(ip, l.now()) <= l.burst-1
}

func (l *failLimiter) fail(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if len(l.fails) >= 1024 { // forget clients that have cooled down
		for k := range l.fails {
			if l.level(k, now) == 0 {
				delete(l.fails, k)
			}
		}
	}
	l.fails[ip] = failCount{n: l.level(ip, now) + 1, at: now}
}

// securityHeaders: no framing (clickjacking), no MIME sniffing, scripts only
// from the app itself. Inline style attributes are used by the UI, so
// style-src allows them; nothing else inline is needed.
func securityHeaders(w http.ResponseWriter, r *http.Request) {
	hd := w.Header()
	hd.Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; "+
		"img-src 'self' data:; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
	hd.Set("X-Content-Type-Options", "nosniff")
	hd.Set("X-Frame-Options", "DENY")
	hd.Set("Referrer-Policy", "same-origin")
	if strings.HasPrefix(r.URL.Path, "/api/") {
		hd.Set("Cache-Control", "no-store")
	}
}

func pathID(r *http.Request) int {
	id, _ := strconv.Atoi(r.PathValue("id"))
	return id
}

func decode(r *http.Request, v any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, 8<<20))
	if err := dec.Decode(v); err != nil {
		return invalid("bad JSON: %v", err)
	}
	return nil
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, err error) {
	var e *Error
	if errors.As(err, &e) {
		writeJSON(w, e.Status, map[string]string{"error": e.Msg})
		return
	}
	log.Printf("error: %v", err)
	writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
}
