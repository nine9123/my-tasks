package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := OpenStore(filepath.Join(t.TempDir(), "tasks.json"))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func ptr[T any](v T) *T { return &v }

func mustIssue(t *testing.T, s *Store, project, summary string) Issue {
	t.Helper()
	i, err := s.CreateIssue(project, IssueInput{Summary: ptr(summary)})
	if err != nil {
		t.Fatal(err)
	}
	return i
}

func errStatus(err error) int {
	var e *Error
	if errors.As(err, &e) {
		return e.Status
	}
	return 0
}

func TestCreateIssueDefaultsAndPersistence(t *testing.T) {
	dir := t.TempDir()
	s, _ := OpenStore(filepath.Join(dir, "tasks.json"))
	if _, err := s.CreateProject("app", "My App", ""); err != nil {
		t.Fatal(err)
	}
	a := mustIssue(t, s, "APP", "first")
	b := mustIssue(t, s, "APP", "second")
	if a.Key != "APP-1" || b.Key != "APP-2" {
		t.Fatalf("keys %s %s", a.Key, b.Key)
	}
	if a.Type != "Task" || a.Priority != "Medium" || a.StatusID != 1 || a.Resolved != nil {
		t.Fatalf("defaults: %+v", a)
	}
	if b.Rank <= a.Rank {
		t.Fatal("new issues go to the bottom of the backlog")
	}

	s2, err := OpenStore(filepath.Join(dir, "tasks.json"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := s2.Issue("APP-2")
	if err != nil || got.Summary != "second" {
		t.Fatalf("reload: %+v %v", got, err)
	}
	c := mustIssue(t, s2, "APP", "third")
	if c.Key != "APP-3" {
		t.Fatalf("numbering after reload: %s", c.Key)
	}
}

func TestSaveDelay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tasks.json")
	s, _ := OpenStore(path)
	s.SetSaveDelay(time.Hour)
	if _, err := s.CreateProject("app", "My App", ""); err != nil {
		t.Fatal(err)
	}
	mustIssue(t, s, "APP", "first")
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("written before the delay: %v", err)
	}
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	s2, _ := OpenStore(path)
	if got, err := s2.Issue("APP-1"); err != nil || got.Summary != "first" {
		t.Fatalf("after flush: %+v %v", got, err)
	}

	// Nothing pending: Flush doesn't touch the file.
	before, _ := os.Stat(path)
	time.Sleep(10 * time.Millisecond)
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	if after, _ := os.Stat(path); !after.ModTime().Equal(before.ModTime()) {
		t.Fatal("clean store rewrote the file")
	}

	// The timer writes everything changed since the first change at once.
	s.SetSaveDelay(20 * time.Millisecond)
	mustIssue(t, s, "APP", "second")
	mustIssue(t, s, "APP", "third")
	deadline := time.Now().Add(5 * time.Second)
	for {
		s3, err := OpenStore(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s3.Issue("APP-3"); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("delayed write never happened")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestProjectValidation(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.CreateProject("1X", "x", ""); errStatus(err) != 400 {
		t.Fatalf("bad key: %v", err)
	}
	s.CreateProject("APP", "x", "")
	if _, err := s.CreateProject("APP", "y", ""); errStatus(err) != 409 {
		t.Fatalf("duplicate: %v", err)
	}
}

func TestStatusChangeSetsResolved(t *testing.T) {
	s := newTestStore(t)
	s.CreateProject("APP", "x", "")
	mustIssue(t, s, "APP", "a")
	i, err := s.UpdateIssue("APP-1", IssueInput{Status: ptr(4)})
	if err != nil || i.Resolved == nil {
		t.Fatalf("done: %+v %v", i, err)
	}
	i, _ = s.UpdateIssue("APP-1", IssueInput{Status: ptr(3)})
	if i.Resolved != nil {
		t.Fatal("reopened issue keeps resolved date")
	}
	if _, err := s.UpdateIssue("APP-1", IssueInput{Status: ptr(99)}); errStatus(err) != 400 {
		t.Fatalf("unknown status: %v", err)
	}
}

func TestSetStatuses(t *testing.T) {
	s := newTestStore(t)
	s.CreateProject("APP", "x", "")
	mustIssue(t, s, "APP", "a") // in status 1

	p, err := s.SetStatuses("APP", []Status{
		{ID: 1, Name: "Backlog", Category: CatTodo},
		{ID: 2, Name: "Doing", Category: CatInProgress},
		{Name: "Review", Category: CatInProgress},
		{ID: 4, Name: "Done", Category: CatDone},
	})
	if err != nil {
		t.Fatal(err)
	}
	if p.Statuses[2].ID != 5 || p.Statuses[0].Name != "Backlog" {
		t.Fatalf("statuses: %+v", p.Statuses)
	}
	// Dropping a status that has issues is refused.
	_, err = s.SetStatuses("APP", []Status{{ID: 4, Name: "Done", Category: CatDone}})
	if errStatus(err) != 409 {
		t.Fatalf("drop used status: %v", err)
	}
	// Turning the issue's status into a done one resolves it.
	s.SetStatuses("APP", []Status{{ID: 1, Name: "Closed", Category: CatDone}})
	i, _ := s.Issue("APP-1")
	if i.Resolved == nil {
		t.Fatal("category change didn't resolve")
	}
}

func TestRankIssue(t *testing.T) {
	s := newTestStore(t)
	s.CreateProject("APP", "x", "")
	for _, n := range []string{"a", "b", "c", "d"} {
		mustIssue(t, s, "APP", n)
	}
	order := func() string {
		is, _ := s.Issues("APP", IssueFilter{})
		out := ""
		for _, i := range is {
			out += i.Summary
		}
		return out
	}
	s.RankIssue("APP-4", "APP-1", "")
	if got := order(); got != "dabc" {
		t.Fatalf("before first: %s", got)
	}
	s.RankIssue("APP-4", "", "APP-3")
	if got := order(); got != "abcd" {
		t.Fatalf("after last: %s", got)
	}
	s.RankIssue("APP-1", "", "APP-2")
	if got := order(); got != "bacd" {
		t.Fatalf("middle: %s", got)
	}
	// Squeezing into the same gap again and again ends in a respace.
	for k := 0; k < 80; k++ {
		if k%2 == 0 {
			s.RankIssue("APP-4", "APP-1", "")
		} else {
			s.RankIssue("APP-3", "APP-1", "")
		}
	}
	if got := order(); got != "bdca" && got != "bcda" {
		t.Fatalf("after many moves: %s", got)
	}
	if _, err := s.RankIssue("APP-1", "", ""); errStatus(err) != 400 {
		t.Fatalf("no target: %v", err)
	}
}

func TestEpicsAndReleases(t *testing.T) {
	s := newTestStore(t)
	s.CreateProject("APP", "x", "")
	epic, _ := s.CreateIssue("APP", IssueInput{Summary: ptr("big"), Type: ptr("Epic")})
	task := mustIssue(t, s, "APP", "small")
	if _, err := s.UpdateIssue(task.Key, IssueInput{Epic: ptr(epic.Key)}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateIssue(epic.Key, IssueInput{Epic: ptr(epic.Key)}); errStatus(err) != 400 {
		t.Fatalf("epic in epic: %v", err)
	}
	if _, err := s.UpdateIssue(epic.Key, IssueInput{Type: ptr("Task")}); errStatus(err) != 409 {
		t.Fatalf("epic with children changes type: %v", err)
	}

	r, err := s.CreateRelease("APP", ReleaseInput{Name: ptr("1.0"), ReleaseDate: ptr("2026-12-01")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateRelease("APP", ReleaseInput{Name: ptr("1.0")}); errStatus(err) != 409 {
		t.Fatalf("duplicate release: %v", err)
	}
	if _, err := s.UpdateRelease(r.ID, ReleaseInput{ReleaseDate: ptr("Dec 1")}); errStatus(err) != 400 {
		t.Fatalf("bad date: %v", err)
	}
	s.UpdateIssue(task.Key, IssueInput{Release: ptr(r.ID)})
	if is, _ := s.Issues("APP", IssueFilter{Release: r.ID}); len(is) != 1 {
		t.Fatalf("release filter: %d", len(is))
	}
	if err := s.DeleteRelease(r.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Issue(task.Key)
	if got.ReleaseID != 0 {
		t.Fatal("issue still points at deleted release")
	}

	if err := s.DeleteIssue(epic.Key); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Issue(task.Key)
	if got.EpicID != 0 {
		t.Fatal("issue still points at deleted epic")
	}
}

func TestSortReleases(t *testing.T) {
	rs := []Release{{ID: 1, Name: "2.10"}, {ID: 2, Name: "2.9"}, {ID: 3, Name: "3.0", ReleaseDate: "2026-01-01"}, {ID: 4, Name: "1.0", ReleaseDate: "2026-02-01"}}
	sortReleases(rs)
	got := ""
	for _, r := range rs {
		got += r.Name + " "
	}
	if got != "3.0 1.0 2.9 2.10 " {
		t.Fatalf("order: %s", got)
	}
}

func TestLinks(t *testing.T) {
	s := newTestStore(t)
	s.CreateProject("APP", "x", "")
	mustIssue(t, s, "APP", "a")
	mustIssue(t, s, "APP", "b")
	if _, err := s.AddLink("APP-1", "Blocks", "APP-2", true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddLink("APP-2", "Blocks", "APP-1", false); errStatus(err) != 409 {
		t.Fatalf("same link from the other side: %v", err)
	}
	l1, _ := s.Links("APP-1")
	l2, _ := s.Links("APP-2")
	if len(l1) != 1 || l1[0].Label != "is blocked by" || l1[0].Key != "APP-2" {
		t.Fatalf("APP-1 links: %+v", l1)
	}
	if len(l2) != 1 || l2[0].Label != "blocks" {
		t.Fatalf("APP-2 links: %+v", l2)
	}
	s.AddLink("APP-1", "Relates", "APP-2", false)
	if _, err := s.AddLink("APP-2", "Relates", "APP-1", false); errStatus(err) != 409 {
		t.Fatalf("relates is symmetric: %v", err)
	}
	s.DeleteIssue("APP-2")
	if l, _ := s.Links("APP-1"); len(l) != 0 {
		t.Fatalf("links to deleted issue: %+v", l)
	}
}

func TestBoardFlag(t *testing.T) {
	s := newTestStore(t)
	s.CreateProject("APP", "x", "")
	a := mustIssue(t, s, "APP", "a")
	if a.Board {
		t.Fatal("new issues start in the backlog")
	}
	b, _ := s.CreateIssue("APP", IssueInput{Summary: ptr("b"), Board: ptr(true)})
	if !b.Board {
		t.Fatal("created on the board")
	}
	a, _ = s.UpdateIssue("APP-1", IssueInput{Status: ptr(2)})
	if !a.Board {
		t.Fatal("starting work moves an issue to the board")
	}
	a, _ = s.UpdateIssue("APP-1", IssueInput{Board: ptr(false)})
	if a.Board || a.StatusID != 2 {
		t.Fatalf("send to backlog: %+v", a)
	}
	a, _ = s.UpdateIssue("APP-1", IssueInput{Summary: ptr("a2"), Status: ptr(2)})
	if a.Board {
		t.Fatal("an unchanged status doesn't move the issue")
	}
	a, _ = s.UpdateIssue("APP-1", IssueInput{Status: ptr(3), Board: ptr(false)})
	if a.Board {
		t.Fatal("an explicit board flag wins")
	}
}

func TestMigrateBoardFlag(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tasks.json")
	old := `{"nextId":10,"projects":{"1":{"id":1,"key":"APP","name":"x","statuses":[
		{"id":1,"name":"TODO","category":"todo"},{"id":2,"name":"Doing","category":"inprogress"},
		{"id":3,"name":"Done","category":"done"}],"nextNumber":4,"nextStatusId":4}},
		"issues":{"2":{"id":2,"projectId":1,"number":1,"key":"APP-1","status":1},
		"3":{"id":3,"projectId":1,"number":2,"key":"APP-2","status":2},
		"4":{"id":4,"projectId":1,"number":3,"key":"APP-3","status":3}}}`
	if err := os.WriteFile(path, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"APP-1": false, "APP-2": true, "APP-3": true}
	for k, w := range want {
		if i, _ := s.Issue(k); i.Board != w {
			t.Fatalf("%s board %v, want %v", k, i.Board, w)
		}
	}
	// Migrated once: sending an issue to the backlog sticks over a reload.
	s.UpdateIssue("APP-2", IssueInput{Board: ptr(false)})
	s2, _ := OpenStore(path)
	if i, _ := s2.Issue("APP-2"); i.Board {
		t.Fatal("migration ran again on a migrated file")
	}
}

func TestSearch(t *testing.T) {
	s := newTestStore(t)
	s.CreateProject("APP", "x", "")
	s.CreateProject("WEB", "y", "")
	mustIssue(t, s, "APP", "login page")
	s.CreateIssue("WEB", IssueInput{Summary: ptr("other"), Description: ptr("fix the login")})
	mustIssue(t, s, "WEB", "unrelated")
	for k := 0; k < 10; k++ {
		mustIssue(t, s, "APP", "filler")
	}

	keys := func(hs []SearchHit) []string {
		out := []string{}
		for _, h := range hs {
			out = append(out, h.Key)
		}
		return out
	}
	if got := keys(s.Search("LOGIN", 20)); len(got) != 2 || got[0] != "APP-1" || got[1] != "WEB-1" {
		t.Fatalf("summary before description: %v", got)
	}
	if got := keys(s.Search("app-1", 3)); len(got) != 3 || got[0] != "APP-1" {
		t.Fatalf("exact key first, limited: %v", got)
	}
	hs := s.Search("web-2", 20)
	if len(hs) != 1 || hs[0].Project != "WEB" || hs[0].Status != "TODO" || hs[0].Done {
		t.Fatalf("hit: %+v", hs)
	}
	if got := s.Search("  ", 20); len(got) != 0 {
		t.Fatalf("empty query: %v", got)
	}
}
