package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Status categories. Every status belongs to one; "done" decides whether an
// issue counts as resolved, the board and backlog only care about the order.
const (
	CatTodo       = "todo"
	CatInProgress = "inprogress"
	CatDone       = "done"
)

var (
	IssueTypes = []string{"Task", "Story", "Bug", "Epic"}
	Priorities = []string{"Highest", "High", "Medium", "Low", "Lowest"}
)

const rankStep = 1024.0

type Status struct {
	ID       int    `json:"id"`
	Name     string `json:"name"`
	Category string `json:"category"`
}

type Project struct {
	ID           int       `json:"id"`
	Key          string    `json:"key"`
	Name         string    `json:"name"`
	Description  string    `json:"description"`
	Statuses     []Status  `json:"statuses"`
	NextNumber   int       `json:"nextNumber"`
	NextStatusID int       `json:"nextStatusId"`
	Created      time.Time `json:"created"`
	// The board's unreleased work leaves out done issues resolved before
	// this, as it does those resolved more than two weeks ago.
	DoneHiddenBefore *time.Time `json:"doneHiddenBefore"`
}

type Release struct {
	ID          int    `json:"id"`
	ProjectID   int    `json:"projectId"`
	Name        string `json:"name"`
	Description string `json:"description"`
	StartDate   string `json:"startDate"`   // YYYY-MM-DD or ""
	ReleaseDate string `json:"releaseDate"` // YYYY-MM-DD or ""
	Released    bool   `json:"released"`
}

type Issue struct {
	ID          int        `json:"id"`
	ProjectID   int        `json:"projectId"`
	Number      int        `json:"number"`
	Key         string     `json:"key"`
	Type        string     `json:"type"`
	Summary     string     `json:"summary"`
	Description string     `json:"description"`
	StatusID    int        `json:"status"`
	Priority    string     `json:"priority"`
	ReleaseID   int        `json:"release"` // 0: none
	EpicID      int        `json:"epic"`    // 0: none
	Rank        float64    `json:"rank"`
	Board       bool       `json:"board"`          // on the board; false: in the backlog
	Jira        bool       `json:"jira,omitempty"` // created or updated by an import
	JiraRank    string     `json:"jiraRank,omitempty"`
	JiraParent  string     `json:"jiraParent,omitempty"` // epic key not imported yet
	Created     time.Time  `json:"created"`
	Updated     time.Time  `json:"updated"`
	Resolved    *time.Time `json:"resolved"`
}

// fromJira reports whether an import may overwrite the issue. Data from
// before the Jira flag existed has a Jira rank on every imported issue.
func (i *Issue) fromJira() bool { return i.Jira || i.JiraRank != "" }

type Comment struct {
	ID         int       `json:"id"`
	IssueID    int       `json:"issueId"`
	Body       string    `json:"body"`
	Created    time.Time `json:"created"`
	Updated    time.Time `json:"updated"`
	ExternalID string    `json:"externalId,omitempty"`
}

// Link reads "From <Outward> To", e.g. "WAR-1 blocks WAR-2", and from the
// other side "To <Inward> From".
type Link struct {
	ID      int    `json:"id"`
	Name    string `json:"name"`
	Outward string `json:"outward"`
	Inward  string `json:"inward"`
	From    int    `json:"from"`
	To      int    `json:"to"`
}

type LinkType struct {
	Name    string `json:"name"`
	Outward string `json:"outward"`
	Inward  string `json:"inward"`
}

var LinkTypes = []LinkType{
	{"Blocks", "blocks", "is blocked by"},
	{"Relates", "relates to", "relates to"},
	{"Duplicate", "duplicates", "is duplicated by"},
}

// dataSchema is the version of the data file layout written by this build.
// Version 1 added Issue.Board.
const dataSchema = 1

type Data struct {
	Schema   int              `json:"schema"`
	NextID   int              `json:"nextId"`
	Projects map[int]*Project `json:"projects"`
	Releases map[int]*Release `json:"releases"`
	Issues   map[int]*Issue   `json:"issues"`
	Comments map[int]*Comment `json:"comments"`
	Links    map[int]*Link    `json:"links"`
}

// Error carries the HTTP status the API should answer with.
type Error struct {
	Status int
	Msg    string
}

func (e *Error) Error() string { return e.Msg }

func notFound(format string, a ...any) error { return &Error{404, fmt.Sprintf(format, a...)} }
func invalid(format string, a ...any) error  { return &Error{400, fmt.Sprintf(format, a...)} }
func conflict(format string, a ...any) error { return &Error{409, fmt.Sprintf(format, a...)} }

// Store keeps everything in memory and writes the whole state to one JSON
// file after every change. For one user and a few thousand issues that is a
// few milliseconds per write and needs no database.
//
// With a save delay (SetSaveDelay) the write is put off instead: the first
// change starts a timer, and when it fires everything changed since is
// written at once, so a session of small edits wakes a sleeping disk once
// rather than per edit. Flush writes what is pending right away.
type Store struct {
	mu    sync.RWMutex
	path  string
	d     *Data
	byKey map[string]*Issue
	now   func() time.Time

	delay time.Duration // 0: write on every change
	dirty bool          // changes not written yet
	timer *time.Timer   // pending delayed write
	due   time.Time     // when the pending delayed write runs
}

func OpenStore(path string) (*Store, error) {
	s := &Store{path: path, now: func() time.Time { return time.Now().UTC().Truncate(time.Second) }}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

func emptyData() *Data {
	return &Data{
		Schema:   dataSchema,
		NextID:   1,
		Projects: map[int]*Project{},
		Releases: map[int]*Release{},
		Issues:   map[int]*Issue{},
		Comments: map[int]*Comment{},
		Links:    map[int]*Link{},
	}
}

func (s *Store) load() error {
	d := emptyData()
	b, err := os.ReadFile(s.path)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return err
	default:
		d.Schema = 0 // a file without the field predates it
		if err := json.Unmarshal(b, d); err != nil {
			return fmt.Errorf("%s: %w", s.path, err)
		}
	}
	migrate(d)
	s.d = d
	s.byKey = map[string]*Issue{}
	for _, i := range d.Issues {
		s.byKey[i.Key] = i
	}
	return nil
}

// migrate brings data written by an older build up to dataSchema.
func migrate(d *Data) {
	if d.Schema < 1 {
		// Before the board flag every issue was on the board. Keep the
		// ones that are being worked on or done there; the ones not
		// started yet go to the backlog.
		for _, i := range d.Issues {
			p := d.Projects[i.ProjectID]
			i.Board = p != nil && !inTodo(p, i.StatusID)
		}
	}
	d.Schema = dataSchema
}

// SetSaveDelay sets how long a change may stay in memory before it is
// written; 0 writes every change immediately. Call Flush before exiting.
func (s *Store) SetSaveDelay(d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.delay = d
}

// save records a change; s.mu must be held for writing. Without a save delay
// it writes the state atomically, and if that fails the in-memory state is
// rolled back to what is on disk, so a failed request leaves no trace. With
// one it only schedules the write.
func (s *Store) save() error {
	if s.delay > 0 {
		s.dirty = true
		if s.timer == nil {
			s.schedule()
		}
		return nil
	}
	b, err := json.Marshal(s.d)
	if err == nil {
		err = writeFileAtomic(s.path, b)
	}
	if err != nil {
		if lerr := s.load(); lerr != nil {
			return fmt.Errorf("save: %v; reload: %v", err, lerr)
		}
		return fmt.Errorf("save: %w", err)
	}
	return nil
}

func (s *Store) delayedWrite() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.timer = nil
	if err := s.writePending(); err != nil {
		// The changes are still in memory (and were already confirmed to
		// the client), so keep them and try again later.
		log.Printf("save: %v; retrying in %s", err, s.delay)
		s.schedule()
	}
}

// schedule starts the delayed write; s.mu must be held for writing.
func (s *Store) schedule() {
	s.due = time.Now().Add(s.delay)
	s.timer = time.AfterFunc(s.delay, s.delayedWrite)
}

// Unsaved reports whether there are changes not written to disk yet and,
// if so, how long until the delayed write is due (0 when it is overdue).
func (s *Store) Unsaved() (bool, time.Duration) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.dirty {
		return false, 0
	}
	return true, max(time.Until(s.due), 0)
}

// writePending writes the state if it has unwritten changes; s.mu must be
// held for writing.
func (s *Store) writePending() error {
	if !s.dirty {
		return nil
	}
	b, err := json.Marshal(s.d)
	if err == nil {
		err = writeFileAtomic(s.path, b)
	}
	if err != nil {
		return err
	}
	s.dirty = false
	return nil
}

// Flush writes pending changes now and cancels the delayed write; if the
// write fails, the delayed write is scheduled again.
func (s *Store) Flush() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
	if err := s.writePending(); err != nil {
		// The save button flushes too: keep the delayed write as a retry.
		if s.delay > 0 {
			s.schedule()
		}
		return fmt.Errorf("save: %w", err)
	}
	return nil
}

func writeFileAtomic(path string, b []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	// Make the rename itself durable. Best effort: some filesystems can't
	// sync a directory, and the new file is in place either way.
	if dir, err := os.Open(filepath.Dir(path)); err == nil {
		dir.Sync()
		dir.Close()
	}
	return nil
}

func (s *Store) newID() int {
	id := s.d.NextID
	s.d.NextID++
	return id
}

// ---- projects ----

var projectKeyRe = regexp.MustCompile(`^[A-Z][A-Z0-9]{1,9}$`)

func defaultStatuses() []Status {
	return []Status{
		{1, "TODO", CatTodo},
		{2, "In Progress", CatInProgress},
		{3, "Testing", CatInProgress},
		{4, "Done", CatDone},
	}
}

func (s *Store) Projects() []Project {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Project, 0, len(s.d.Projects))
	for _, p := range s.d.Projects {
		out = append(out, *p)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Key < out[b].Key })
	return out
}

func (s *Store) projectByKey(key string) *Project {
	for _, p := range s.d.Projects {
		if p.Key == key {
			return p
		}
	}
	return nil
}

func (s *Store) Project(key string) (Project, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p := s.projectByKey(key)
	if p == nil {
		return Project{}, notFound("project %s not found", key)
	}
	return *p, nil
}

func (s *Store) createProject(key, name, desc string) (*Project, error) {
	key = strings.ToUpper(strings.TrimSpace(key))
	name = strings.TrimSpace(name)
	if !projectKeyRe.MatchString(key) {
		return nil, invalid("project key must be 2-10 letters/digits, starting with a letter")
	}
	if name == "" {
		return nil, invalid("project name is required")
	}
	if s.projectByKey(key) != nil {
		return nil, conflict("project %s already exists", key)
	}
	p := &Project{
		ID: s.newID(), Key: key, Name: name, Description: desc,
		Statuses: defaultStatuses(), NextNumber: 1, NextStatusID: 5, Created: s.now(),
	}
	s.d.Projects[p.ID] = p
	return p, nil
}

func (s *Store) CreateProject(key, name, desc string) (Project, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, err := s.createProject(key, name, desc)
	if err != nil {
		return Project{}, err
	}
	return *p, s.save()
}

func (s *Store) UpdateProject(key string, name, desc *string) (Project, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.projectByKey(key)
	if p == nil {
		return Project{}, notFound("project %s not found", key)
	}
	if name != nil {
		if strings.TrimSpace(*name) == "" {
			return Project{}, invalid("project name is required")
		}
		p.Name = strings.TrimSpace(*name)
	}
	if desc != nil {
		p.Description = *desc
	}
	return *p, s.save()
}

// HideDone hides the done issues on the project's board, the ones resolved
// until now; hide false shows them again.
func (s *Store) HideDone(key string, hide bool) (Project, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.projectByKey(key)
	if p == nil {
		return Project{}, notFound("project %s not found", key)
	}
	p.DoneHiddenBefore = nil
	if hide {
		now := s.now()
		p.DoneHiddenBefore = &now
	}
	return *p, s.save()
}

func (s *Store) DeleteProject(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.projectByKey(key)
	if p == nil {
		return notFound("project %s not found", key)
	}
	for id, i := range s.d.Issues {
		if i.ProjectID == p.ID {
			s.deleteIssue(id)
		}
	}
	for id, r := range s.d.Releases {
		if r.ProjectID == p.ID {
			delete(s.d.Releases, id)
		}
	}
	delete(s.d.Projects, p.ID)
	return s.save()
}

// SetStatuses replaces a project's workflow. Statuses keep their id when
// passed with one; a status that still has issues can't be dropped.
func (s *Store) SetStatuses(key string, in []Status) (Project, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.projectByKey(key)
	if p == nil {
		return Project{}, notFound("project %s not found", key)
	}
	if len(in) == 0 {
		return Project{}, invalid("a project needs at least one status")
	}
	old := map[int]bool{}
	for _, st := range p.Statuses {
		old[st.ID] = true
	}
	seen := map[int]bool{}
	names := map[string]bool{}
	out := make([]Status, 0, len(in))
	next := p.NextStatusID
	for _, st := range in {
		st.Name = strings.TrimSpace(st.Name)
		if st.Name == "" {
			return Project{}, invalid("status name is required")
		}
		if names[strings.ToLower(st.Name)] {
			return Project{}, invalid("duplicate status %q", st.Name)
		}
		names[strings.ToLower(st.Name)] = true
		if st.Category != CatTodo && st.Category != CatInProgress && st.Category != CatDone {
			return Project{}, invalid("status %q: category must be todo, inprogress or done", st.Name)
		}
		if st.ID == 0 {
			st.ID = next
			next++
		} else if !old[st.ID] || seen[st.ID] {
			return Project{}, invalid("unknown status id %d", st.ID)
		}
		seen[st.ID] = true
		out = append(out, st)
	}
	for _, i := range s.d.Issues {
		if i.ProjectID == p.ID && !seen[i.StatusID] {
			return Project{}, conflict("status %q still has issues, move them first", statusName(p, i.StatusID))
		}
	}
	p.Statuses = out
	p.NextStatusID = next
	// The category of a status may have changed: keep resolved in step.
	for _, i := range s.d.Issues {
		if i.ProjectID == p.ID {
			s.syncResolved(p, i)
		}
	}
	return *p, s.save()
}

func statusName(p *Project, id int) string {
	for _, st := range p.Statuses {
		if st.ID == id {
			return st.Name
		}
	}
	return strconv.Itoa(id)
}

func findStatus(p *Project, id int) *Status {
	for k := range p.Statuses {
		if p.Statuses[k].ID == id {
			return &p.Statuses[k]
		}
	}
	return nil
}

// inTodo reports whether a status is in the to do category (or unknown).
func inTodo(p *Project, statusID int) bool {
	st := findStatus(p, statusID)
	return st == nil || st.Category == CatTodo
}

func (s *Store) syncResolved(p *Project, i *Issue) {
	st := findStatus(p, i.StatusID)
	done := st != nil && st.Category == CatDone
	if done && i.Resolved == nil {
		t := s.now()
		i.Resolved = &t
	} else if !done {
		i.Resolved = nil
	}
}

// ---- releases ----

var dateRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

func (s *Store) Releases(projectKey string) ([]Release, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p := s.projectByKey(projectKey)
	if p == nil {
		return nil, notFound("project %s not found", projectKey)
	}
	out := []Release{}
	for _, r := range s.d.Releases {
		if r.ProjectID == p.ID {
			out = append(out, *r)
		}
	}
	sortReleases(out)
	return out, nil
}

// sortReleases orders by release date, undated last, then by name with
// numbers compared as numbers (2.9 before 2.10).
func sortReleases(rs []Release) {
	sort.Slice(rs, func(a, b int) bool {
		x, y := rs[a], rs[b]
		if (x.ReleaseDate == "") != (y.ReleaseDate == "") {
			return x.ReleaseDate != ""
		}
		if x.ReleaseDate != y.ReleaseDate {
			return x.ReleaseDate < y.ReleaseDate
		}
		if c := naturalCompare(x.Name, y.Name); c != 0 {
			return c < 0
		}
		return x.ID < y.ID
	})
}

func naturalCompare(a, b string) int {
	for a != "" && b != "" {
		ca, cb := a[0], b[0]
		if isDigit(ca) && isDigit(cb) {
			na, ra := splitDigits(a)
			nb, rb := splitDigits(b)
			ta, tb := strings.TrimLeft(na, "0"), strings.TrimLeft(nb, "0")
			if len(ta) != len(tb) {
				return len(ta) - len(tb)
			}
			if ta != tb {
				return strings.Compare(ta, tb)
			}
			a, b = ra, rb
			continue
		}
		la, lb := strings.ToLower(a[:1]), strings.ToLower(b[:1])
		if la != lb {
			return strings.Compare(la, lb)
		}
		a, b = a[1:], b[1:]
	}
	return len(a) - len(b)
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func splitDigits(s string) (string, string) {
	n := 0
	for n < len(s) && isDigit(s[n]) {
		n++
	}
	return s[:n], s[n:]
}

type ReleaseInput struct {
	Name        *string `json:"name"`
	Description *string `json:"description"`
	StartDate   *string `json:"startDate"`
	ReleaseDate *string `json:"releaseDate"`
	Released    *bool   `json:"released"`
}

func (s *Store) releaseByName(projectID int, name string) *Release {
	for _, r := range s.d.Releases {
		if r.ProjectID == projectID && strings.EqualFold(r.Name, name) {
			return r
		}
	}
	return nil
}

func (s *Store) applyRelease(r *Release, in ReleaseInput) error {
	if in.Name != nil {
		name := strings.TrimSpace(*in.Name)
		if name == "" {
			return invalid("release name is required")
		}
		if o := s.releaseByName(r.ProjectID, name); o != nil && o.ID != r.ID {
			return conflict("release %q already exists", name)
		}
		r.Name = name
	}
	if in.Description != nil {
		r.Description = *in.Description
	}
	for _, f := range []struct {
		in  *string
		out *string
	}{{in.StartDate, &r.StartDate}, {in.ReleaseDate, &r.ReleaseDate}} {
		if f.in == nil {
			continue
		}
		if *f.in != "" && !dateRe.MatchString(*f.in) {
			return invalid("dates must be YYYY-MM-DD")
		}
		*f.out = *f.in
	}
	if in.Released != nil {
		r.Released = *in.Released
	}
	return nil
}

func (s *Store) CreateRelease(projectKey string, in ReleaseInput) (Release, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.projectByKey(projectKey)
	if p == nil {
		return Release{}, notFound("project %s not found", projectKey)
	}
	if in.Name == nil {
		return Release{}, invalid("release name is required")
	}
	r := &Release{ID: s.newID(), ProjectID: p.ID}
	if err := s.applyRelease(r, in); err != nil {
		return Release{}, err
	}
	s.d.Releases[r.ID] = r
	return *r, s.save()
}

func (s *Store) UpdateRelease(id int, in ReleaseInput) (Release, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.d.Releases[id]
	if r == nil {
		return Release{}, notFound("release %d not found", id)
	}
	cp := *r
	if err := s.applyRelease(&cp, in); err != nil {
		return Release{}, err
	}
	*r = cp
	return *r, s.save()
}

// DeleteRelease removes the release; its issues stay, without a release.
func (s *Store) DeleteRelease(id int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.d.Releases[id] == nil {
		return notFound("release %d not found", id)
	}
	for _, i := range s.d.Issues {
		if i.ReleaseID == id {
			i.ReleaseID = 0
		}
	}
	delete(s.d.Releases, id)
	return s.save()
}

// ---- issues ----

type IssueFilter struct {
	Status  int    // 0: any
	Release int    // 0: any, -1: none
	Epic    int    // 0: any, -1: none
	Type    string // "": any
	Text    string // in key, summary, description
}

func (s *Store) Issues(projectKey string, f IssueFilter) ([]Issue, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p := s.projectByKey(projectKey)
	if p == nil {
		return nil, notFound("project %s not found", projectKey)
	}
	text := strings.ToLower(strings.TrimSpace(f.Text))
	out := []Issue{}
	for _, i := range s.d.Issues {
		if i.ProjectID != p.ID ||
			(f.Status != 0 && i.StatusID != f.Status) ||
			(f.Release > 0 && i.ReleaseID != f.Release) || (f.Release < 0 && i.ReleaseID != 0) ||
			(f.Epic > 0 && i.EpicID != f.Epic) || (f.Epic < 0 && i.EpicID != 0) ||
			(f.Type != "" && i.Type != f.Type) {
			continue
		}
		if text != "" && !strings.Contains(strings.ToLower(i.Key+" "+i.Summary+" "+i.Description), text) {
			continue
		}
		out = append(out, *i)
	}
	sortByRank(out)
	return out, nil
}

// SearchHit is an issue found by Search, with what a result list shows.
type SearchHit struct {
	Key      string `json:"key"`
	Project  string `json:"project"`
	Type     string `json:"type"`
	Summary  string `json:"summary"`
	Priority string `json:"priority"`
	Status   string `json:"status"`
	Done     bool   `json:"done"`
}

// Search finds issues in all projects by key, summary or description, best
// matches first: the issue with that exact key (or, for a bare number like
// "12", every issue with that number, whatever its project), then keys
// starting with the text, then matches in the summary, then in the
// description. Within each, the most recently updated come first.
func (s *Store) Search(text string, limit int) []SearchHit {
	s.mu.RLock()
	defer s.mu.RUnlock()
	q := strings.ToLower(strings.TrimSpace(text))
	if q == "" {
		return []SearchHit{}
	}
	type scored struct {
		i     *Issue
		score int
	}
	var found []scored
	for _, i := range s.d.Issues {
		key := strings.ToLower(i.Key)
		score := 0
		switch {
		case key == q, q == strconv.Itoa(i.Number):
			score = 4
		case strings.HasPrefix(key, q):
			score = 3
		case strings.Contains(strings.ToLower(i.Summary), q):
			score = 2
		case strings.Contains(strings.ToLower(i.Description), q):
			score = 1
		}
		if score > 0 {
			found = append(found, scored{i, score})
		}
	}
	sort.Slice(found, func(a, b int) bool {
		x, y := found[a], found[b]
		if x.score != y.score {
			return x.score > y.score
		}
		if !x.i.Updated.Equal(y.i.Updated) {
			return x.i.Updated.After(y.i.Updated)
		}
		return x.i.ID > y.i.ID
	})
	if len(found) > limit {
		found = found[:limit]
	}
	out := make([]SearchHit, 0, len(found))
	for _, f := range found {
		p := s.d.Projects[f.i.ProjectID]
		h := SearchHit{Key: f.i.Key, Project: p.Key, Type: f.i.Type, Summary: f.i.Summary, Priority: f.i.Priority}
		if st := findStatus(p, f.i.StatusID); st != nil {
			h.Status, h.Done = st.Name, st.Category == CatDone
		}
		out = append(out, h)
	}
	return out
}

func sortByRank(is []Issue) {
	sort.Slice(is, func(a, b int) bool {
		if is[a].Rank != is[b].Rank {
			return is[a].Rank < is[b].Rank
		}
		return is[a].Number < is[b].Number
	})
}

func (s *Store) Issue(key string) (Issue, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	i := s.byKey[key]
	if i == nil {
		return Issue{}, notFound("issue %s not found", key)
	}
	return *i, nil
}

type IssueInput struct {
	Type        *string `json:"type"`
	Summary     *string `json:"summary"`
	Description *string `json:"description"`
	Status      *int    `json:"status"`
	Priority    *string `json:"priority"`
	Release     *int    `json:"release"` // 0 clears
	Epic        *string `json:"epic"`    // issue key, "" clears
	Board       *bool   `json:"board"`   // false: send to the backlog
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func (s *Store) applyIssue(p *Project, i *Issue, in IssueInput) error {
	if in.Type != nil {
		if !contains(IssueTypes, *in.Type) {
			return invalid("unknown issue type %q", *in.Type)
		}
		if *in.Type == "Epic" && i.EpicID != 0 {
			return invalid("an epic can't belong to another epic")
		}
		if *in.Type != "Epic" && i.Type == "Epic" {
			for _, c := range s.d.Issues {
				if c.EpicID == i.ID {
					return conflict("%s still has issues in it", i.Key)
				}
			}
		}
		i.Type = *in.Type
	}
	if in.Summary != nil {
		sum := strings.TrimSpace(*in.Summary)
		if sum == "" {
			return invalid("summary is required")
		}
		i.Summary = sum
	}
	if in.Description != nil {
		i.Description = *in.Description
	}
	if in.Status != nil {
		if findStatus(p, *in.Status) == nil {
			return invalid("unknown status %d", *in.Status)
		}
		// Starting work on an issue takes it out of the backlog.
		if *in.Status != i.StatusID && !inTodo(p, *in.Status) {
			i.Board = true
		}
		i.StatusID = *in.Status
	}
	if in.Priority != nil {
		if !contains(Priorities, *in.Priority) {
			return invalid("unknown priority %q", *in.Priority)
		}
		i.Priority = *in.Priority
	}
	if in.Release != nil {
		if *in.Release != 0 {
			r := s.d.Releases[*in.Release]
			if r == nil || r.ProjectID != p.ID {
				return invalid("unknown release %d", *in.Release)
			}
		}
		i.ReleaseID = *in.Release
	}
	if in.Epic != nil {
		i.JiraParent = ""
		if *in.Epic == "" {
			i.EpicID = 0
		} else {
			e := s.byKey[*in.Epic]
			if e == nil || e.ProjectID != p.ID || e.Type != "Epic" {
				return invalid("%s is not an epic in this project", *in.Epic)
			}
			if i.Type == "Epic" {
				return invalid("an epic can't belong to another epic")
			}
			i.EpicID = e.ID
		}
	}
	if in.Board != nil {
		i.Board = *in.Board
	}
	return nil
}

func (s *Store) maxRank(projectID int) float64 {
	m := 0.0
	for _, i := range s.d.Issues {
		if i.ProjectID == projectID && i.Rank > m {
			m = i.Rank
		}
	}
	return m
}

// CreateIssue adds an issue at the bottom of the backlog, in the project's
// first status unless one is given.
func (s *Store) CreateIssue(projectKey string, in IssueInput) (Issue, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.projectByKey(projectKey)
	if p == nil {
		return Issue{}, notFound("project %s not found", projectKey)
	}
	now := s.now()
	i := &Issue{
		ID: s.newID(), ProjectID: p.ID, Type: "Task", Priority: "Medium",
		StatusID: p.Statuses[0].ID, Created: now, Updated: now,
	}
	if in.Summary == nil {
		return Issue{}, invalid("summary is required")
	}
	if err := s.applyIssue(p, i, in); err != nil {
		return Issue{}, err
	}
	i.Number = p.NextNumber
	i.Key = fmt.Sprintf("%s-%d", p.Key, i.Number)
	i.Rank = s.maxRank(p.ID) + rankStep
	p.NextNumber++
	s.syncResolved(p, i)
	s.d.Issues[i.ID] = i
	s.byKey[i.Key] = i
	return *i, s.save()
}

func (s *Store) UpdateIssue(key string, in IssueInput) (Issue, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.byKey[key]
	if i == nil {
		return Issue{}, notFound("issue %s not found", key)
	}
	p := s.d.Projects[i.ProjectID]
	cp := *i
	if err := s.applyIssue(p, &cp, in); err != nil {
		return Issue{}, err
	}
	cp.Updated = s.now()
	s.syncResolved(p, &cp)
	*i = cp
	return *i, s.save()
}

func (s *Store) deleteIssue(id int) {
	i := s.d.Issues[id]
	for cid, c := range s.d.Comments {
		if c.IssueID == id {
			delete(s.d.Comments, cid)
		}
	}
	for lid, l := range s.d.Links {
		if l.From == id || l.To == id {
			delete(s.d.Links, lid)
		}
	}
	for _, c := range s.d.Issues {
		if c.EpicID == id {
			c.EpicID = 0
		}
	}
	delete(s.byKey, i.Key)
	delete(s.d.Issues, id)
}

func (s *Store) DeleteIssue(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.byKey[key]
	if i == nil {
		return notFound("issue %s not found", key)
	}
	s.deleteIssue(i.ID)
	return s.save()
}

// RankIssue moves an issue directly before or after another issue of the
// same project in the backlog order.
func (s *Store) RankIssue(key, before, after string) (Issue, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.byKey[key]
	if i == nil {
		return Issue{}, notFound("issue %s not found", key)
	}
	if (before == "") == (after == "") {
		return Issue{}, invalid("give exactly one of before or after")
	}
	ref := s.byKey[before+after]
	if ref == nil || ref.ProjectID != i.ProjectID {
		return Issue{}, invalid("%s is not an issue in this project", before+after)
	}
	if ref.ID == i.ID {
		return *i, nil
	}
	order := s.rankOrder(i.ProjectID, i.ID)
	pos := 0
	for k, x := range order {
		if x.ID == ref.ID {
			pos = k
			break
		}
	}
	if after != "" {
		pos++
	}
	// i goes between order[pos-1] and order[pos].
	lo, hi := math.Inf(-1), math.Inf(1)
	if pos > 0 {
		lo = order[pos-1].Rank
	}
	if pos < len(order) {
		hi = order[pos].Rank
	}
	switch {
	case math.IsInf(lo, -1):
		i.Rank = hi - rankStep
	case math.IsInf(hi, 1):
		i.Rank = lo + rankStep
	case hi-lo > 1e-6:
		i.Rank = (lo + hi) / 2
	default:
		// Out of room between neighbours: respace the whole project.
		withI := append(append(append([]*Issue{}, order[:pos]...), i), order[pos:]...)
		for k, x := range withI {
			x.Rank = float64(k+1) * rankStep
		}
	}
	return *i, s.save()
}

// rankOrder lists a project's issues by rank, leaving out one issue.
func (s *Store) rankOrder(projectID, skip int) []*Issue {
	out := []*Issue{}
	for _, x := range s.d.Issues {
		if x.ProjectID == projectID && x.ID != skip {
			out = append(out, x)
		}
	}
	sort.Slice(out, func(a, b int) bool {
		if out[a].Rank != out[b].Rank {
			return out[a].Rank < out[b].Rank
		}
		return out[a].Number < out[b].Number
	})
	return out
}

// ---- comments ----

func (s *Store) Comments(key string) ([]Comment, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	i := s.byKey[key]
	if i == nil {
		return nil, notFound("issue %s not found", key)
	}
	out := []Comment{}
	for _, c := range s.d.Comments {
		if c.IssueID == i.ID {
			out = append(out, *c)
		}
	}
	sort.Slice(out, func(a, b int) bool {
		if !out[a].Created.Equal(out[b].Created) {
			return out[a].Created.Before(out[b].Created)
		}
		return out[a].ID < out[b].ID
	})
	return out, nil
}

func (s *Store) AddComment(key, body string) (Comment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.byKey[key]
	if i == nil {
		return Comment{}, notFound("issue %s not found", key)
	}
	if strings.TrimSpace(body) == "" {
		return Comment{}, invalid("comment is empty")
	}
	now := s.now()
	c := &Comment{ID: s.newID(), IssueID: i.ID, Body: body, Created: now, Updated: now}
	s.d.Comments[c.ID] = c
	return *c, s.save()
}

func (s *Store) UpdateComment(id int, body string) (Comment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.d.Comments[id]
	if c == nil {
		return Comment{}, notFound("comment %d not found", id)
	}
	if strings.TrimSpace(body) == "" {
		return Comment{}, invalid("comment is empty")
	}
	c.Body = body
	c.Updated = s.now()
	return *c, s.save()
}

func (s *Store) DeleteComment(id int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.d.Comments[id] == nil {
		return notFound("comment %d not found", id)
	}
	delete(s.d.Comments, id)
	return s.save()
}

// ---- links ----

// IssueLink is a link as seen from one issue.
type IssueLink struct {
	ID      int    `json:"id"`
	Name    string `json:"name"`
	Label   string `json:"label"` // "blocks", "is blocked by", ...
	Key     string `json:"key"`   // the other issue
	Summary string `json:"summary"`
	Status  string `json:"status"`
	Done    bool   `json:"done"`
}

func (s *Store) Links(key string) ([]IssueLink, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	i := s.byKey[key]
	if i == nil {
		return nil, notFound("issue %s not found", key)
	}
	out := []IssueLink{}
	for _, l := range s.d.Links {
		var other *Issue
		label := ""
		switch i.ID {
		case l.From:
			other, label = s.d.Issues[l.To], l.Outward
		case l.To:
			other, label = s.d.Issues[l.From], l.Inward
		default:
			continue
		}
		if other == nil {
			continue
		}
		p := s.d.Projects[other.ProjectID]
		st := findStatus(p, other.StatusID)
		il := IssueLink{ID: l.ID, Name: l.Name, Label: label, Key: other.Key, Summary: other.Summary}
		if st != nil {
			il.Status, il.Done = st.Name, st.Category == CatDone
		}
		out = append(out, il)
	}
	sort.Slice(out, func(a, b int) bool {
		if out[a].Label != out[b].Label {
			return out[a].Label < out[b].Label
		}
		return out[a].Key < out[b].Key
	})
	return out, nil
}

func (s *Store) hasLink(name string, from, to int) bool {
	for _, l := range s.d.Links {
		if l.Name != name {
			continue
		}
		symmetric := l.Outward == l.Inward
		if (l.From == from && l.To == to) || (symmetric && l.From == to && l.To == from) {
			return true
		}
	}
	return false
}

// AddLink links key to other. inward=true reads the link type backwards,
// e.g. "key is blocked by other".
func (s *Store) AddLink(key, typeName, other string, inward bool) (Link, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, b := s.byKey[key], s.byKey[other]
	if a == nil {
		return Link{}, notFound("issue %s not found", key)
	}
	if b == nil {
		return Link{}, invalid("issue %s not found", other)
	}
	if a.ID == b.ID {
		return Link{}, invalid("an issue can't link to itself")
	}
	var lt *LinkType
	for k := range LinkTypes {
		if LinkTypes[k].Name == typeName {
			lt = &LinkTypes[k]
		}
	}
	if lt == nil {
		return Link{}, invalid("unknown link type %q", typeName)
	}
	if inward {
		a, b = b, a
	}
	if s.hasLink(lt.Name, a.ID, b.ID) {
		return Link{}, conflict("link already exists")
	}
	l := &Link{ID: s.newID(), Name: lt.Name, Outward: lt.Outward, Inward: lt.Inward, From: a.ID, To: b.ID}
	s.d.Links[l.ID] = l
	return *l, s.save()
}

func (s *Store) DeleteLink(id int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.d.Links[id] == nil {
		return notFound("link %d not found", id)
	}
	delete(s.d.Links, id)
	return s.save()
}
