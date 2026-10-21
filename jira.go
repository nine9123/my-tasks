package main

import (
	"encoding/xml"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Jira's "Export XML" (the RSS search view). Only the fields this tracker
// keeps are read; assignee, reporter, watchers, labels, components, due
// dates, time tracking and attachments are ignored.

type jiraRSS struct {
	Items []jiraItem `xml:"channel>item"`
}

type jiraItem struct {
	Project struct {
		Key  string `xml:"key,attr"`
		Name string `xml:",chardata"`
	} `xml:"project"`
	Key            string `xml:"key"`
	Summary        string `xml:"summary"`
	Description    string `xml:"description"`
	Type           string `xml:"type"`
	Priority       string `xml:"priority"`
	Status         string `xml:"status"`
	StatusCategory struct {
		Key string `xml:"key,attr"`
	} `xml:"statusCategory"`
	Created     string        `xml:"created"`
	Updated     string        `xml:"updated"`
	Resolved    string        `xml:"resolved"`
	FixVersions []string      `xml:"fixVersion"`
	Parent      string        `xml:"parent"`
	Comments    []jiraComment `xml:"comments>comment"`
	LinkTypes   []struct {
		Name    string       `xml:"name"`
		Outward jiraLinkSide `xml:"outwardlinks"`
		Inward  jiraLinkSide `xml:"inwardlinks"`
	} `xml:"issuelinks>issuelinktype"`
	CustomFields []struct {
		Key    string   `xml:"key,attr"`
		Values []string `xml:"customfieldvalues>customfieldvalue"`
	} `xml:"customfields>customfield"`
}

type jiraComment struct {
	ID      string `xml:"id,attr"`
	Created string `xml:"created,attr"`
	Body    string `xml:",chardata"`
}

type jiraLinkSide struct {
	Description string   `xml:"description,attr"`
	Keys        []string `xml:"issuelink>issuekey"`
}

func (it *jiraItem) rank() string {
	for _, cf := range it.CustomFields {
		if cf.Key == "com.pyxis.greenhopper.jira:gh-lexo-rank" && len(cf.Values) > 0 {
			return strings.TrimSpace(cf.Values[0])
		}
	}
	return ""
}

// ParseJiraXML reads one export file. Jira caps an XML export at 1000
// issues, so a bigger project comes as several files.
func ParseJiraXML(r io.Reader) ([]jiraItem, error) {
	var doc jiraRSS
	dec := xml.NewDecoder(r)
	dec.Strict = false
	dec.Entity = xml.HTMLEntity
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("not a Jira XML export: %w", err)
	}
	for k := range doc.Items {
		it := &doc.Items[k]
		it.Key = strings.TrimSpace(it.Key)
		it.Project.Key = strings.TrimSpace(it.Project.Key)
		if it.Key == "" || it.Project.Key == "" {
			return nil, fmt.Errorf("item %d has no issue or project key", k+1)
		}
	}
	return doc.Items, nil
}

func parseJiraTime(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	t, err := time.Parse("Mon, 2 Jan 2006 15:04:05 -0700", s)
	if err != nil {
		return time.Time{}, false
	}
	return t.UTC(), true
}

func normName(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func jiraCategory(key string) string {
	switch key {
	case "done":
		return CatDone
	case "indeterminate":
		return CatInProgress
	default:
		return CatTodo
	}
}

type ImportResult struct {
	Files           int      `json:"files"`
	ProjectsCreated []string `json:"projectsCreated"`
	IssuesCreated   int      `json:"issuesCreated"`
	IssuesUpdated   int      `json:"issuesUpdated"`
	ReleasesCreated []string `json:"releasesCreated"`
	StatusesAdded   []string `json:"statusesAdded"`
	Comments        int      `json:"comments"`
	Links           int      `json:"links"`
	Warnings        []string `json:"warnings"`
}

func (r *ImportResult) warn(format string, a ...any) {
	if len(r.Warnings) < 200 {
		r.Warnings = append(r.Warnings, fmt.Sprintf(format, a...))
	} else if len(r.Warnings) == 200 {
		r.Warnings = append(r.Warnings, "… more warnings left out")
	}
}

var issueKeyRe = regexp.MustCompile(`^([A-Z][A-Z0-9]*)-(\d+)$`)

// ImportJira merges the issues of one or more export files into the store.
// Issues are matched by key, so importing again updates them instead of
// adding copies; keys and numbers stay as they were in Jira. The backlog
// order of the imported issues is set from Jira's rank.
func (s *Store) ImportJira(files [][]jiraItem) (ImportResult, error) {
	res := ImportResult{Files: len(files), ProjectsCreated: []string{}, ReleasesCreated: []string{},
		StatusesAdded: []string{}, Warnings: []string{}}

	// The same issue may be in two files (overlapping exports): keep the
	// most recently updated copy.
	items := map[string]*jiraItem{}
	for _, f := range files {
		for k := range f {
			it := &f[k]
			if old, ok := items[it.Key]; ok {
				ot, _ := parseJiraTime(old.Updated)
				nt, _ := parseJiraTime(it.Updated)
				if !nt.After(ot) {
					continue
				}
			}
			items[it.Key] = it
		}
	}
	keys := make([]string, 0, len(items))
	for k := range items {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(a, b int) bool { return naturalCompare(keys[a], keys[b]) < 0 })

	s.mu.Lock()
	defer s.mu.Unlock()

	touched := map[int]bool{} // project ids
	for _, key := range keys {
		it := items[key]
		m := issueKeyRe.FindStringSubmatch(it.Key)
		if m == nil || m[1] != it.Project.Key {
			res.warn("%s: unexpected issue key, skipped", it.Key)
			continue
		}
		num, _ := strconv.Atoi(m[2])

		p := s.projectByKey(it.Project.Key)
		if p == nil {
			name := strings.TrimSpace(it.Project.Name)
			if name == "" {
				name = it.Project.Key
			}
			var err error
			if p, err = s.createProject(it.Project.Key, name, ""); err != nil {
				s.load()
				return res, fmt.Errorf("project %s: %w", it.Project.Key, err)
			}
			res.ProjectsCreated = append(res.ProjectsCreated, p.Key)
		}
		touched[p.ID] = true
		if num >= p.NextNumber {
			p.NextNumber = num + 1
		}

		i := s.byKey[it.Key]
		if i == nil {
			i = &Issue{ID: s.newID(), ProjectID: p.ID, Number: num, Key: it.Key}
			s.d.Issues[i.ID] = i
			s.byKey[i.Key] = i
			res.IssuesCreated++
		} else {
			res.IssuesUpdated++
		}

		i.Summary = strings.TrimSpace(it.Summary)
		if i.Summary == "" {
			i.Summary = "(no summary)"
		}
		i.Description = HTMLToMarkdown(it.Description)
		i.Type = strings.TrimSpace(it.Type)
		if !contains(IssueTypes, i.Type) {
			res.warn("%s: type %q imported as Task", it.Key, i.Type)
			i.Type = "Task"
		}
		i.Priority = strings.TrimSpace(it.Priority)
		if !contains(Priorities, i.Priority) {
			if i.Priority != "" {
				res.warn("%s: priority %q imported as Medium", it.Key, i.Priority)
			}
			i.Priority = "Medium"
		}
		i.StatusID = s.importStatus(p, it, &res)

		i.ReleaseID = 0
		if len(it.FixVersions) > 0 {
			if len(it.FixVersions) > 1 {
				res.warn("%s: has %d fix versions, kept only %q", it.Key, len(it.FixVersions), it.FixVersions[0])
			}
			name := strings.TrimSpace(it.FixVersions[0])
			r := s.releaseByName(p.ID, name)
			if r == nil {
				r = &Release{ID: s.newID(), ProjectID: p.ID, Name: name}
				s.d.Releases[r.ID] = r
				res.ReleasesCreated = append(res.ReleasesCreated, p.Key+" "+name)
			}
			i.ReleaseID = r.ID
		}

		now := s.now()
		var ok bool
		if i.Created, ok = parseJiraTime(it.Created); !ok {
			i.Created = now
		}
		if i.Updated, ok = parseJiraTime(it.Updated); !ok {
			i.Updated = i.Created
		}
		i.Resolved = nil
		if t, ok := parseJiraTime(it.Resolved); ok {
			i.Resolved = &t
		}
		s.syncResolved(p, i)
		i.JiraRank = it.rank()
	}

	// An item only shows the wording of its own side of a link, so collect
	// both from all items.
	words := map[string][2]string{}
	for _, it := range items {
		for _, lt := range it.LinkTypes {
			w := words[strings.TrimSpace(lt.Name)]
			if d := strings.TrimSpace(lt.Outward.Description); d != "" {
				w[0] = d
			}
			if d := strings.TrimSpace(lt.Inward.Description); d != "" {
				w[1] = d
			}
			words[strings.TrimSpace(lt.Name)] = w
		}
	}

	// Second pass: everything that points at other issues.
	for _, key := range keys {
		it := items[key]
		i := s.byKey[it.Key]
		if i == nil {
			continue
		}
		i.EpicID = 0
		i.JiraParent = strings.TrimSpace(it.Parent)

		for _, jc := range it.Comments {
			c := s.commentByExternalID(i.ID, jc.ID)
			if c == nil {
				c = &Comment{ID: s.newID(), IssueID: i.ID, ExternalID: jc.ID}
				s.d.Comments[c.ID] = c
				res.Comments++
			}
			c.Body = HTMLToMarkdown(jc.Body)
			t, ok := parseJiraTime(jc.Created)
			if !ok {
				t = i.Created
			}
			c.Created, c.Updated = t, t
		}

		for _, lt := range it.LinkTypes {
			name := strings.TrimSpace(lt.Name)
			for _, side := range []struct {
				js      jiraLinkSide
				outward bool
			}{{lt.Outward, true}, {lt.Inward, false}} {
				for _, k := range side.js.Keys {
					other := s.byKey[strings.TrimSpace(k)]
					if other == nil {
						res.warn("%s: linked issue %s is not in the export, link skipped", it.Key, strings.TrimSpace(k))
						continue
					}
					from, to := i, other
					if !side.outward {
						from, to = other, i
					}
					if s.hasLink(name, from.ID, to.ID) {
						continue
					}
					l := &Link{ID: s.newID(), Name: name, From: from.ID, To: to.ID}
					l.Outward, l.Inward = linkWords(name, words[name][0], words[name][1])
					s.d.Links[l.ID] = l
					res.Links++
				}
			}
		}
	}

	// Parents last, including ones left pending by an earlier import whose
	// epic only came with this one.
	pending := []*Issue{}
	for _, i := range s.d.Issues {
		if i.JiraParent != "" {
			pending = append(pending, i)
		}
	}
	sort.Slice(pending, func(a, b int) bool { return naturalCompare(pending[a].Key, pending[b].Key) < 0 })
	for _, i := range pending {
		parent := i.JiraParent
		e := s.byKey[parent]
		switch {
		case e == nil:
			res.warn("%s: epic %s is not imported yet, linked once it is", i.Key, parent)
			continue
		case e.Type != "Epic":
			res.warn("%s: parent %s is a %s, not an epic; left without epic", i.Key, parent, e.Type)
		case i.Type == "Epic":
			res.warn("%s: an epic can't have a parent, %s dropped", i.Key, parent)
		default:
			i.EpicID = e.ID
		}
		i.JiraParent = ""
	}

	for pid := range touched {
		s.rerankFromJira(pid)
	}
	return res, s.save()
}

func linkWords(name, outward, inward string) (string, string) {
	for _, lt := range LinkTypes {
		if lt.Name == name {
			return lt.Outward, lt.Inward
		}
	}
	// A link whose other side isn't in any of the files has only one
	// wording; fall back to the type name for the other.
	if outward == "" {
		outward = strings.ToLower(name)
	}
	if inward == "" {
		inward = strings.ToLower(name)
	}
	return outward, inward
}

func (s *Store) commentByExternalID(issueID int, ext string) *Comment {
	if ext == "" {
		return nil
	}
	for _, c := range s.d.Comments {
		if c.IssueID == issueID && c.ExternalID == ext {
			return c
		}
	}
	return nil
}

// importStatus finds the project status matching a Jira status by name,
// ignoring case and spaces ("To Do" is "TODO"), and adds it to the
// workflow if there is none.
func (s *Store) importStatus(p *Project, it *jiraItem, res *ImportResult) int {
	name := strings.TrimSpace(it.Status)
	if name == "" {
		return p.Statuses[0].ID
	}
	for _, st := range p.Statuses {
		if normName(st.Name) == normName(name) {
			return st.ID
		}
	}
	st := Status{ID: p.NextStatusID, Name: name, Category: jiraCategory(it.StatusCategory.Key)}
	p.NextStatusID++
	// Keep done statuses at the end of the workflow.
	pos := len(p.Statuses)
	if st.Category != CatDone {
		for k, x := range p.Statuses {
			if x.Category == CatDone {
				pos = k
				break
			}
		}
	}
	p.Statuses = append(p.Statuses[:pos], append([]Status{st}, p.Statuses[pos:]...)...)
	res.StatusesAdded = append(res.StatusesAdded, p.Key+" "+name)
	return st.ID
}

// rerankFromJira renumbers a project's backlog: issues that came from Jira in
// Jira's rank order, then the ones created here in their current order.
func (s *Store) rerankFromJira(projectID int) {
	order := s.rankOrder(projectID, 0)
	sort.SliceStable(order, func(a, b int) bool {
		x, y := order[a].JiraRank, order[b].JiraRank
		if (x == "") != (y == "") {
			return x != ""
		}
		return x < y
	})
	for k, i := range order {
		i.Rank = float64(k+1) * rankStep
	}
}

// ---- HTML to Markdown ----

// HTMLToMarkdown turns the HTML Jira renders descriptions and comments into
// (as an export) into the small Markdown dialect the web UI displays.
func HTMLToMarkdown(src string) string {
	if strings.TrimSpace(src) == "" {
		return ""
	}
	dec := xml.NewDecoder(strings.NewReader("<root>" + src + "</root>"))
	dec.Strict = false
	dec.AutoClose = xml.HTMLAutoClose
	dec.Entity = xml.HTMLEntity

	var b strings.Builder
	type list struct {
		ordered bool
		n       int
	}
	var lists []list
	pre := 0
	var linkHref string
	var linkText *strings.Builder

	out := func(s string) {
		if linkText != nil {
			linkText.WriteString(s)
		} else {
			b.WriteString(s)
		}
	}
	blank := func() { // start a new paragraph
		s := strings.TrimRight(b.String(), " ")
		b.Reset()
		b.WriteString(s)
		if b.Len() > 0 && !strings.HasSuffix(s, "\n\n") {
			if strings.HasSuffix(s, "\n") {
				b.WriteString("\n")
			} else {
				b.WriteString("\n\n")
			}
		}
	}
	newline := func() {
		s := strings.TrimRight(b.String(), " ")
		b.Reset()
		b.WriteString(s)
		if b.Len() > 0 && !strings.HasSuffix(s, "\n") {
			b.WriteString("\n")
		}
	}
	atLineStart := func() bool {
		s := b.String()
		return s == "" || strings.HasSuffix(s, "\n")
	}

	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch strings.ToLower(t.Name.Local) {
			case "p", "div", "table", "blockquote":
				if pre == 0 {
					blank()
				}
			case "br":
				if pre > 0 {
					out("\n")
				} else {
					newline()
				}
			case "h1", "h2", "h3", "h4", "h5", "h6":
				blank()
				n, _ := strconv.Atoi(t.Name.Local[1:])
				b.WriteString(strings.Repeat("#", n) + " ")
			case "ul", "ol":
				if len(lists) == 0 {
					blank()
				}
				lists = append(lists, list{ordered: strings.EqualFold(t.Name.Local, "ol")})
			case "li":
				newline()
				if len(lists) == 0 {
					b.WriteString("- ")
					break
				}
				l := &lists[len(lists)-1]
				l.n++
				b.WriteString(strings.Repeat("  ", len(lists)-1))
				if l.ordered {
					b.WriteString(strconv.Itoa(l.n) + ". ")
				} else {
					b.WriteString("- ")
				}
			case "tr":
				newline()
			case "td", "th":
				out(" | ")
			case "pre":
				blank()
				b.WriteString("```\n")
				pre++
			case "b", "strong":
				if pre == 0 {
					out("**")
				}
			case "em", "i":
				if pre == 0 {
					out("_")
				}
			case "tt", "code":
				if pre == 0 {
					out("`")
				}
			case "a":
				if pre == 0 && linkText == nil {
					linkHref = ""
					for _, a := range t.Attr {
						if strings.EqualFold(a.Name.Local, "href") {
							linkHref = a.Value
						}
					}
					linkText = &strings.Builder{}
				}
			case "hr":
				blank()
				b.WriteString("---")
				blank()
			}
		case xml.EndElement:
			switch strings.ToLower(t.Name.Local) {
			case "p", "div", "table", "blockquote", "h1", "h2", "h3", "h4", "h5", "h6":
				if pre == 0 {
					blank()
				}
			case "ul", "ol":
				if len(lists) > 0 {
					lists = lists[:len(lists)-1]
				}
				if len(lists) == 0 {
					blank()
				}
			case "pre":
				if pre > 0 {
					pre--
					s := strings.TrimRight(b.String(), "\n ")
					b.Reset()
					b.WriteString(s + "\n```")
					blank()
				}
			case "b", "strong":
				if pre == 0 {
					out("**")
				}
			case "em", "i":
				if pre == 0 {
					out("_")
				}
			case "tt", "code":
				if pre == 0 {
					out("`")
				}
			case "a":
				if linkText != nil {
					text := strings.TrimSpace(linkText.String())
					linkText = nil
					switch {
					case linkHref == "" || strings.HasPrefix(linkHref, "#"):
						out(text)
					case text == "" || text == linkHref:
						out(linkHref)
					default:
						out("[" + text + "](" + linkHref + ")")
					}
				}
			}
		case xml.CharData:
			s := string(t)
			if pre > 0 {
				out(s)
				continue
			}
			s = strings.Join(strings.Fields(s), " ")
			if s == "" {
				if len(t) > 0 && !atLineStart() && linkText == nil {
					out(" ")
				}
				continue
			}
			if len(t) > 0 && isSpace(t[0]) && !atLineStart() {
				s = " " + s
			}
			if isSpace(t[len(t)-1]) {
				s += " "
			}
			if linkText == nil && atLineStart() {
				s = strings.TrimLeft(s, " ")
			}
			out(s)
		}
	}
	lines := strings.Split(b.String(), "\n")
	for k, l := range lines {
		lines[k] = strings.TrimRight(l, " ")
	}
	return strings.TrimSpace(multiBlank.ReplaceAllString(strings.Join(lines, "\n"), "\n\n"))
}

var multiBlank = regexp.MustCompile(`\n{3,}`)

func isSpace(c byte) bool { return c == ' ' || c == '\n' || c == '\t' || c == '\r' }
