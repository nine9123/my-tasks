package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log"
	"net/http"
	"strconv"
	"strings"
)

//go:embed web
var webFS embed.FS

const sessionCookie = "mt_session"

type Server struct {
	store        *Store
	apiKey       string
	sessionToken string
	mux          *http.ServeMux
}

func NewServer(store *Store, apiKey string) *Server {
	mac := hmac.New(sha256.New, []byte(apiKey))
	mac.Write([]byte("my-tasks-session"))
	s := &Server{store: store, apiKey: apiKey, sessionToken: hex.EncodeToString(mac.Sum(nil)), mux: http.NewServeMux()}
	s.routes()
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.mux.ServeHTTP(w, r) }

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

func (s *Server) authorized(r *http.Request) bool {
	key := r.Header.Get("X-Api-Key")
	if key == "" {
		if a := r.Header.Get("Authorization"); strings.HasPrefix(a, "Bearer ") {
			key = strings.TrimPrefix(a, "Bearer ")
		}
	}
	if key != "" {
		return hmac.Equal([]byte(key), []byte(s.apiKey))
	}
	if c, err := r.Cookie(sessionCookie); err == nil {
		return hmac.Equal([]byte(c.Value), []byte(s.sessionToken))
	}
	return false
}

func (s *Server) auth(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.authorized(r) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		h(w, r)
	}
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var in struct{ Key string }
	if err := decode(r, &in); err != nil {
		writeError(w, err)
		return
	}
	if !hmac.Equal([]byte(in.Key), []byte(s.apiKey)) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "wrong API key"})
		return
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: s.sessionToken, Path: "/",
		HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: 365 * 24 * 3600})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", HttpOnly: true, MaxAge: -1})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
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
