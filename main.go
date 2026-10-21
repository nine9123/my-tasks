// my-tasks: a small single-user issue tracker (projects, backlog, board,
// releases) with an importer for Jira XML exports.
package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

const usage = `usage:
  my-tasks                      run the server
  my-tasks import FILE.xml...   import Jira XML exports into the data file
  my-tasks healthcheck          exit 0 if the local server answers

environment:
  API_KEY    required for the server; sent as X-Api-Key or "Authorization: Bearer"
  DATA_DIR   where tasks.json is kept (default ./data)
  PORT       listen port (default 8099)
`

func main() {
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	dataDir := env("DATA_DIR", "data")
	port := env("PORT", "8099")

	args := os.Args[1:]
	if len(args) > 0 {
		switch args[0] {
		case "import":
			os.Exit(runImport(dataDir, args[1:]))
		case "healthcheck":
			os.Exit(healthcheck(port))
		default:
			fmt.Fprint(os.Stderr, usage)
			os.Exit(2)
		}
	}

	apiKey := os.Getenv("API_KEY")
	if apiKey == "" {
		log.Fatal("API_KEY is not set")
	}
	store, err := openData(dataDir)
	if err != nil {
		log.Fatal(err)
	}
	srv := &http.Server{Addr: ":" + port, Handler: logRequests(NewServer(store, apiKey)),
		ReadHeaderTimeout: 10 * time.Second}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	// ListenAndServe returns as soon as Shutdown starts; wait for Shutdown to
	// finish so requests in flight (and their writes) complete before exit.
	// docker stop (and idlegate) send SIGTERM and allow 10s by default.
	shutdown := make(chan struct{})
	go func() {
		defer close(shutdown)
		<-ctx.Done()
		shut, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shut); err != nil {
			log.Printf("shutdown: %v", err)
		}
	}()
	log.Printf("listening on :%s, data in %s", port, dataDir)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
	<-shutdown
}

func env(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

func openData(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return OpenStore(filepath.Join(dir, "tasks.json"))
}

// runImport imports files straight into the data file. Stop the server
// first: it holds the data in memory and would overwrite the import.
func runImport(dataDir string, files []string) int {
	if len(files) == 0 {
		fmt.Fprint(os.Stderr, usage)
		return 2
	}
	store, err := openData(dataDir)
	if err != nil {
		log.Print(err)
		return 1
	}
	var all [][]jiraItem
	for _, name := range files {
		f, err := os.Open(name)
		if err != nil {
			log.Print(err)
			return 1
		}
		items, err := ParseJiraXML(f)
		f.Close()
		if err != nil {
			log.Printf("%s: %v", name, err)
			return 1
		}
		all = append(all, items)
	}
	res, err := store.ImportJira(all)
	if err != nil {
		log.Print(err)
		return 1
	}
	for _, w := range res.Warnings {
		fmt.Println("warning:", w)
	}
	fmt.Printf("%d files: %d issues created, %d updated, %d comments, %d links\n",
		res.Files, res.IssuesCreated, res.IssuesUpdated, res.Comments, res.Links)
	fmt.Printf("projects created: %v\nreleases created: %v\nstatuses added: %v\n",
		res.ProjectsCreated, res.ReleasesCreated, res.StatusesAdded)
	return 0
}

func healthcheck(port string) int {
	c := http.Client{Timeout: 3 * time.Second}
	resp, err := c.Get("http://127.0.0.1:" + port + "/healthz")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}

type statusRecorder struct {
	http.ResponseWriter
	code int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.code = code
	r.ResponseWriter.WriteHeader(code)
}

func logRequests(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, code: 200}
		h.ServeHTTP(rec, r)
		if r.URL.Path != "/healthz" && (r.Method != http.MethodGet || rec.code >= 400) {
			log.Printf("%s %s %d %s", r.Method, r.URL.Path, rec.code, time.Since(start).Round(time.Millisecond))
		}
	})
}
