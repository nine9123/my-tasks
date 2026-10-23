# my-tasks

A small, single-user issue tracker for a NAS, shaped after the parts of Jira
that actually get used: projects, a backlog, a board, releases. One static Go
binary with the web UI built in, one JSON file for the data, an API key for
auth. No users, no permissions, no automations.

- **Projects** with a key (`WAR`), issues numbered `WAR-1`, `WAR-2`, …
- **Issues**: Task, Story, Bug (and Epic to group them), priority
  Highest → Lowest, a status, a release, a Markdown description, comments
  and links (blocks / relates to / duplicates).
- **Workflow per project**, configurable under *Settings*. New projects start
  with `TODO → In Progress → Testing → Done`. Each status has a category
  (to do / in progress / done); moving an issue into a *done* status sets its
  resolved date.
- **Backlog**: the issues not on the board yet, in one ranked list, split
  into a section per unreleased release plus one for issues without a
  release, with a *Board* section on top listing what is on the board. Drag
  to reorder, to move an issue into another release, or onto / off the
  board; type into a section's input and press Enter to create an issue in
  TODO, already tagged with that release.
- **Board**: the tab a project opens on. A column per status with the issues sent to the board (from
  the backlog, or by moving an issue out of a to-do status anywhere). Drag
  cards between and within columns to set status and order. Shows the
  unreleased work by default; pick a single release or everything instead.
  As on Jira's boards, the unreleased work shows done issues only for 14
  days after they were resolved; *Hide done issues* in the done column's
  `⋯` menu hides the ones there now without waiting (*Show hidden done
  issues* brings them back). The epic filter has *No epic*, which is
  what Jira's *No Epic* swimlane shows when grouped by epic. When the
  columns don't fit, a small map of them sits in the bottom-right corner, as
  on Jira: drag its frame to scroll the board sideways. Cards look like
  Jira's: the summary (up to three lines), then the type, key and priority.
- **Quick edits** on backlog rows and board cards, as in Jira: click the
  type or priority icon to change it, the pencil to rename in place, and
  the `⋯` menu to send an issue to the board / backlog or move it to the
  top or bottom.
- **Issues** opened from the backlog, board, releases or search open in a
  modal over the page; *Open as page* opens the issue's own page
  (`#/i/WAR-1`) in a new tab. To link an issue, search for it by key or
  summary and pick it from the list.
- **Search** in the header over all projects, by key (or just its
  number: `12` finds `WAR-12`), summary and description. Press `/` to jump to it.
- **Epics**: a tab of their own, like Jira's *Timeline* without the
  dates: each open epic with its status and the progress of its issues,
  folding out to those issues; epics in a done status are folded away
  under *Done*. Create an epic, or an issue in one, from there.
- **Releases**: name, start and release date, description, released or not,
  with progress per release. Ordered by release date, then by version number.
- **Import from Jira**: see [below](#migrating-from-jira).

Deliberately left out: sprints, summary, timeline, archive, subtasks,
assignees, labels, components, attachments, time tracking, automations.

## Quick start

```sh
echo "API_KEY=$(openssl rand -hex 24)" > .env   # see .env.example
docker compose up -d --build
```

Open `http://<nas>:8099` and sign in with the API key.

Everything lives in `/data/tasks.json` (mounted from `./data`). It is
rewritten atomically, so copying it is a backup; include the folder in
your snapshots.

Changes are not written one by one: the first change starts a
`SAVE_DELAY` timer (5 minutes by default), and when it runs out everything
changed in the meantime is written in one go. So a session of small edits
wakes a sleeping HDD once, not on every drag and rename. Stopping the
container (`docker stop`, idlegate) writes pending changes first. A crash
or power cut loses at most the last `SAVE_DELAY` of changes; set
`SAVE_DELAY=0` to write every change immediately, as before.

| Variable   | Default | |
|------------|---------|-|
| `API_KEY`  | –       | required |
| `DATA_DIR` | `/data` in the image, `./data` otherwise | |
| `PORT`     | `8099`  | |
| `SAVE_DELAY` | `5m` | how long changes may wait in memory before they are written (`30s`, `10m`, …; `0`: write every change) |

### TrueNAS / image from the registry

`.gitea/workflows/build.yml` runs the tests and builds the image on every
push to `main` (and on demand), and pushes
`gitea.internal:30008/nine9/my-tasks:<first 8 chars of the commit>` and
`:latest`. It needs the Actions secrets `REGISTRY_USER` and
`REGISTRY_TOKEN` (write:package), the same as media-watcher. If the runner
can't reach Docker Hub, set the repository variable `BUILD_IMAGE` to a copy
of `golang:1.23-alpine` in the Gitea registry; the final image is `FROM
scratch`, so that is the only image pulled.

As a TrueNAS custom app (Install via YAML), with your key and dataset:

```yaml
services:
  tasks:
    image: gitea.internal:30008/nine9/my-tasks:<sha>
    restart: unless-stopped
    user: "568:568"   # the TrueNAS apps user; it must own the dataset
    ports:
      - "8099:8099"
    environment:
      API_KEY: "<your key>"   # no .env on TrueNAS: write it here
    volumes:
      - /mnt/tank/apps/my-tasks:/data
```

Check with `curl http://<nas>:8099/healthz` → `ok`.

### Behind idlegate (stop it while unused)

[idlegate](https://github.com/nine9123/idlegate) stops the container after
a while without requests and starts it again on the next one, so the
tracker uses no memory while nobody has it open. my-tasks needs nothing
special for that: it is ready as soon as it listens, keeps everything in
`tasks.json`, and the login cookie is derived from the API key, so you stay
signed in across a sleep. The UI doesn't poll, so an open tab doesn't keep
it awake either.

[`docker-compose.idlegate.yml`](docker-compose.idlegate.yml) is the
compose file above with idlegate in front:

```sh
docker compose -f docker-compose.idlegate.yml up -d --build
```

If a host already runs an idlegate, add the route to it instead, with
`my-tasks` on a network it can reach and no published port of its own:

```
:8099 http://my-tasks:8099 my-tasks idle=30m health=/healthz
```

Worth knowing:

- The first page load after a sleep waits for the container to start.
- `/healthz` through the gate's port counts as use: an uptime monitor
  polling it keeps the tracker awake. The image's own `HEALTHCHECK` runs
  inside the container and doesn't go through the gate.
- The command line import (below) needs the server stopped, and a request
  arriving through the gate during the import would start it again. Stop the
  gate, or use *Import from Jira* in the UI, which is safe at any time.

## Migrating from Jira

1. In Jira, go to *Filters → Advanced issue search*, search
   `project = WAR ORDER BY created DESC`, then *Export → Export XML*.
2. **Jira exports at most 1000 issues per file.** For more, split the
   search so each part is under 1000, e.g. `project = WAR AND key >= WAR-5000`
   and `project = WAR AND key < WAR-5000`, until every issue is in some file.
   (Atlassian documents adding `&tempMax=1000&pager/start=1000`, `2000`, …
   to the export URL for CSV; it may work for the XML export URL too.
   Overlapping files do no harm.)
3. In my-tasks, *Import from Jira*, select **all** the files at once, Import.

What is kept, per issue: key and number, summary, description, type,
priority, status, fix version (as the release), epic (the parent), comments,
links, created/updated/resolved dates, and the backlog order (Jira's rank).
Assignee, reporter, watchers, labels, votes and the epic colour are dropped.

Details worth knowing:

- **Statuses** are matched by name ignoring case and spaces, so Jira's
  `To Do`, `TESTING` and `DONE` land in `TODO`, `Testing` and `Done`. A
  status that matches nothing is added to the project's workflow.
- **Board or backlog**: imported issues in a to-do status go to the
  backlog, the others onto the board. Re-importing only moves an issue to
  the board when its status changed out of to-do. Data from before the
  board/backlog split is sorted the same way the first time it is loaded.
  Jira's XML doesn't say which issues are on Jira's board, so to-do issues
  you had on the board there, and in-progress ones you had left in the
  backlog, need moving once by hand (*Send to board* / *Send to backlog*).
- **Releases** are created from the fix version names. Jira's XML has no
  release dates or released flag, so all imported releases start out
  *unreleased* without dates: set those on the *Releases* page (2.1 and 2.2
  of the attached export have all issues done and are likely to be released).
- **Re-importing is safe**: issues are matched by key and updated, comments
  by their Jira id, links are not duplicated. Imported fields are
  overwritten, including the backlog order of imported issues. An issue
  created here is never overwritten: if Jira has an issue with the same key,
  that one is skipped with a warning.
- A project whose key can't be used here (longer than 10 characters, say)
  is skipped with a warning; the other projects in the files are imported.
- Epics and links that point into a file you didn't select are reported as
  warnings. An epic link is remembered and filled in when the epic is
  imported later; a link is filled in when the file with the other issue is
  imported (Jira writes a link into both issues).
- Descriptions and comments are converted from Jira's HTML to Markdown
  (paragraphs, lists, code blocks, links, bold/italic, headings).
- Issues of other types (e.g. Sub-task) become Tasks, with a warning.

There is also a command line import, for when the server is **stopped** (it
keeps the data in memory and would overwrite the file):

```sh
docker compose run --rm tasks import /data/export-1.xml /data/export-2.xml
```

## API

Everything the UI does goes through a JSON API. Send the key as
`X-Api-Key: <key>` or `Authorization: Bearer <key>`.

| | |
|---|---|
| `GET/POST /api/projects` | list, create `{key, name}` |
| `GET/PATCH/DELETE /api/projects/{KEY}` | |
| `PUT /api/projects/{KEY}/statuses` | replace the workflow `[{id?, name, category}]` |
| `GET/POST /api/projects/{KEY}/releases`, `PATCH/DELETE /api/releases/{id}` | |
| `GET /api/projects/{KEY}/issues?q=&type=&status=&release=&epic=` | ranked; `release=-1`: none. No descriptions |
| `POST /api/projects/{KEY}/issues` | `{summary, type, priority, status, release, epic, description}` |
| `GET/PATCH/DELETE /api/issues/{KEY-N}` | PATCH takes any of the fields above |
| `POST /api/issues/{KEY-N}/rank` | `{before: "KEY-M"}` or `{after: "KEY-M"}` |
| `GET/POST /api/issues/{KEY-N}/comments`, `PATCH/DELETE /api/comments/{id}` | `{body}` |
| `GET/POST /api/issues/{KEY-N}/links`, `DELETE /api/links/{id}` | `{type: "Blocks", issue: "KEY-M", inward: false}` |
| `POST /api/import` | multipart, one or more `file` fields |

## Development

Standard library only, no dependencies.

```sh
go test ./...
API_KEY=dev go run .        # http://localhost:8099, data in ./data
```
