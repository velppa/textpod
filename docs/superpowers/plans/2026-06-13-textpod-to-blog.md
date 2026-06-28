# Textpod → Blog Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Convert Textpod from a client-rendered note app into a server-rendered, paginated single-column blog (honeypot.net look), removing Tufte CSS and all in-app writing UI while keeping the HTTP write API.

**Architecture:** `GET /{$}` renders the full page server-side: an inline search box, a page of full note bodies (newest first, 10/page), and a pager. Search and pagination are pure helper functions on `[]Note`. The per-note page (`/note/{id}`) becomes view-only. Tufte CSS and the et-book font are removed and replaced by a new `blog.css`; `shared.css` keeps the Textpod overlay minus the sidebar.

**Tech Stack:** Go 1.26 (`net/http` method-prefixed routes), goldmark, embedded static assets, table-driven Go tests.

**Spec:** `docs/superpowers/specs/2026-06-13-textpod-to-blog-design.md`

---

## File structure

- `main.go` — handlers + helpers. Add `pageSize` const, `parsePage`, `filterNotes`, `paginateNotes`, `searchBoxHTML`, `pagerHTML`. Rewrite `Server.index`. Trim `notePage`. Swap embeds (drop `tufteCSS`/`etBookFS`, add `blogCSS`), drop the `/et-book/` route and tufte basePath rewrite.
- `main_test.go` — add tests for the new helpers + an httptest for `index`.
- `index.html` — strip to a static server template (placeholders only, no JS).
- `blog.css` — **new** base stylesheet (honeypot look).
- `shared.css` — remove `#toc` sidebar + external-link arrow; keep the rest.
- `tufte.css`, `et-book/` — **deleted**.

Notes are stored oldest→newest in `s.Notes` (sorted ascending by timestamp). The index displays newest first, so it iterates `s.Notes` in reverse.

---

## Task 1: `filterNotes` helper

**Files:**
- Modify: `main.go` (add helper near other `// -- utils --` functions)
- Test: `main_test.go`

- [ ] **Step 1: Write the failing test**

Add to `main_test.go`:

```go
func TestFilterNotes(t *testing.T) {
	notes := []Note{
		{ID: "1", Timestamp: "2026-05-14 10:00:00", Content: "Hello World"},
		{ID: "2", Timestamp: "2026-06-01 09:30:00", Content: "Go programming"},
		{ID: "3", Timestamp: "2026-06-01 11:00:00", Content: "another GO note"},
	}
	cases := []struct {
		name string
		q    string
		want []string // ids expected, in order
	}{
		{"empty returns all", "", []string{"1", "2", "3"}},
		{"case-insensitive content", "go", []string{"2", "3"}},
		{"timestamp substring", "2026-06-01", []string{"2", "3"}},
		{"no match", "zzz", []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := filterNotes(notes, tc.q)
			if len(got) != len(tc.want) {
				t.Fatalf("len=%d want %d (%v)", len(got), len(tc.want), got)
			}
			for i, id := range tc.want {
				if got[i].ID != id {
					t.Errorf("pos %d: got id %q want %q", i, got[i].ID, id)
				}
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./... -run TestFilterNotes`
Expected: FAIL — `undefined: filterNotes`.

- [ ] **Step 3: Write minimal implementation**

Add to `main.go` (in the `// -- utils --` section):

```go
func filterNotes(notes []Note, q string) []Note {
	if q == "" {
		return notes
	}
	ql := strings.ToLower(q)
	out := make([]Note, 0, len(notes))
	for _, n := range notes {
		if strings.Contains(strings.ToLower(n.Content), ql) || strings.Contains(n.Timestamp, q) {
			out = append(out, n)
		}
	}
	return out
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./... -run TestFilterNotes`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add main.go main_test.go
git commit -m "feat: add filterNotes helper for server-side search"
```

---

## Task 2: `paginateNotes` helper

**Files:**
- Modify: `main.go`
- Test: `main_test.go`

- [ ] **Step 1: Write the failing test**

Add to `main_test.go`:

```go
func TestPaginateNotes(t *testing.T) {
	mk := func(n int) []Note {
		out := make([]Note, n)
		for i := range out {
			out[i] = Note{ID: fmt.Sprintf("%d", i)}
		}
		return out
	}
	cases := []struct {
		name      string
		total     int
		page      int
		size      int
		wantLen   int
		wantPage  int
		wantPages int
		wantFirst string // id of first item, "" if empty
	}{
		{"first page", 25, 1, 10, 10, 1, 3, "0"},
		{"second page", 25, 2, 10, 10, 2, 3, "10"},
		{"last partial page", 25, 3, 10, 5, 3, 3, "20"},
		{"page below 1 clamps", 25, 0, 10, 10, 1, 3, "0"},
		{"page over max clamps", 25, 99, 10, 5, 3, 3, "20"},
		{"empty input", 0, 1, 10, 0, 1, 1, ""},
		{"exact multiple", 20, 2, 10, 10, 2, 2, "10"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, page, pages := paginateNotes(mk(tc.total), tc.page, tc.size)
			if len(got) != tc.wantLen {
				t.Errorf("len=%d want %d", len(got), tc.wantLen)
			}
			if page != tc.wantPage {
				t.Errorf("page=%d want %d", page, tc.wantPage)
			}
			if pages != tc.wantPages {
				t.Errorf("pages=%d want %d", pages, tc.wantPages)
			}
			if tc.wantFirst != "" {
				if len(got) == 0 || got[0].ID != tc.wantFirst {
					t.Errorf("first id mismatch, got %v want %q", got, tc.wantFirst)
				}
			}
		})
	}
}
```

This test uses `fmt`; `main_test.go` currently imports only `strings` and `testing`. Add `"fmt"` to its import block.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./... -run TestPaginateNotes`
Expected: FAIL — `undefined: paginateNotes`.

- [ ] **Step 3: Write minimal implementation**

Add to `main.go`:

```go
const pageSize = 10

// paginateNotes returns the slice of notes for the given 1-based page,
// the clamped page number, and the total number of pages (>=1).
func paginateNotes(notes []Note, page, size int) ([]Note, int, int) {
	pages := (len(notes) + size - 1) / size
	if pages < 1 {
		pages = 1
	}
	if page < 1 {
		page = 1
	}
	if page > pages {
		page = pages
	}
	start := (page - 1) * size
	if start > len(notes) {
		start = len(notes)
	}
	end := start + size
	if end > len(notes) {
		end = len(notes)
	}
	return notes[start:end], page, pages
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./... -run TestPaginateNotes`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add main.go main_test.go
git commit -m "feat: add paginateNotes helper with page clamping"
```

---

## Task 3: `parsePage`, `searchBoxHTML`, `pagerHTML` render helpers

**Files:**
- Modify: `main.go` (needs `strconv` import added)
- Test: `main_test.go`

- [ ] **Step 1: Write the failing test**

Add to `main_test.go`:

```go
func TestParsePage(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"", 1}, {"1", 1}, {"3", 3}, {"0", 1}, {"-2", 1}, {"abc", 1}, {" 4 ", 4},
	}
	for _, tc := range cases {
		if got := parsePage(tc.in); got != tc.want {
			t.Errorf("parsePage(%q)=%d want %d", tc.in, got, tc.want)
		}
	}
}

func TestSearchBoxHTML(t *testing.T) {
	got := searchBoxHTML("/notes", `a "b"`)
	for _, w := range []string{
		`action="/notes/"`,
		`name="q"`,
		`value="a &#34;b&#34;"`,
		`type="search"`,
	} {
		if !strings.Contains(got, w) {
			t.Errorf("expected %q in %q", w, got)
		}
	}
}

func TestPagerHTML(t *testing.T) {
	t.Run("single page hidden", func(t *testing.T) {
		if got := pagerHTML("", "", 1, 1); got != "" {
			t.Errorf("expected empty, got %q", got)
		}
	})
	t.Run("middle page both links", func(t *testing.T) {
		got := pagerHTML("", "go", 2, 3)
		for _, w := range []string{
			`Newer posts`, `Older posts`,
			`href="/?page=1&q=go"`, `href="/?page=3&q=go"`,
			`Page 2 of 3`,
		} {
			if !strings.Contains(got, w) {
				t.Errorf("expected %q in %q", w, got)
			}
		}
	})
	t.Run("first page no newer link", func(t *testing.T) {
		got := pagerHTML("", "", 1, 3)
		if strings.Contains(got, "Newer posts") {
			t.Errorf("did not expect Newer link on page 1: %q", got)
		}
		if !strings.Contains(got, "Older posts") {
			t.Errorf("expected Older link: %q", got)
		}
	})
}
```

Note: `url.Values.Encode()` sorts keys alphabetically, so `page` precedes `q` in the query string — the asserted `?page=1&q=go` order is intentional.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./... -run 'TestParsePage|TestSearchBoxHTML|TestPagerHTML'`
Expected: FAIL — `undefined: parsePage` (etc).

- [ ] **Step 3: Write minimal implementation**

Add `"strconv"` to the `main.go` import block (alphabetical, after `"sort"`). Then add:

```go
func parsePage(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n < 1 {
		return 1
	}
	return n
}

func searchBoxHTML(basePath, q string) string {
	return fmt.Sprintf(`<form class="search" method="get" action="%s/">`+
		`<input type="search" name="q" value="%s" placeholder="Search…"></form>`,
		basePath, html.EscapeString(q))
}

func pagerHTML(basePath, q string, page, pages int) string {
	if pages <= 1 {
		return ""
	}
	link := func(p int) string {
		v := url.Values{}
		if q != "" {
			v.Set("q", q)
		}
		v.Set("page", strconv.Itoa(p))
		return basePath + "/?" + v.Encode()
	}
	var b strings.Builder
	b.WriteString(`<nav class="pager">`)
	if page > 1 {
		fmt.Fprintf(&b, `<a class="newer" href="%s">&larr; Newer posts</a>`, link(page-1))
	} else {
		b.WriteString(`<span class="newer"></span>`)
	}
	fmt.Fprintf(&b, `<span class="page-of">Page %d of %d</span>`, page, pages)
	if page < pages {
		fmt.Fprintf(&b, `<a class="older" href="%s">Older posts &rarr;</a>`, link(page+1))
	} else {
		b.WriteString(`<span class="older"></span>`)
	}
	b.WriteString(`</nav>`)
	return b.String()
}
```

(`fmt`, `html`, `net/url`, `strings` are already imported in `main.go`.)

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./... -run 'TestParsePage|TestSearchBoxHTML|TestPagerHTML'`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add main.go main_test.go
git commit -m "feat: add parsePage, searchBoxHTML, pagerHTML render helpers"
```

---

## Task 4: New `blog.css` and trimmed `shared.css`

**Files:**
- Create: `blog.css`
- Modify: `shared.css`

- [ ] **Step 1: Create `blog.css`**

Write `blog.css` (entire file):

```css
/* blog.css — base typography + single-column layout for the Textpod
 * blog. Replaces tufte.css. honeypot.net feel: centered narrow column,
 * serif body, blue links, gray metadata, dark-mode aware.
 * Color tokens (--color-*) are defined in shared.css :root. */

html {
    -webkit-text-size-adjust: 100%;
}

body {
    max-width: 40rem;
    margin: 2rem auto;
    padding: 0 1rem;
    font-family: Charter, "Iowan Old Style", Georgia, Cambria, "Times New Roman", serif;
    line-height: 1.6;
    color: var(--color-text);
    background-color: #fff;
}

@media (prefers-color-scheme: dark) {
    body { background-color: #1a1a1a; }
}

h1, h2, h3, h4, h5, h6 {
    line-height: 1.25;
    margin: 1.6rem 0 0.6rem;
    font-weight: 600;
}

h1 { font-size: 1.8rem; }
h2 { font-size: 1.45rem; }
h3 { font-size: 1.2rem; }
h4, h5, h6 { font-size: 1.05rem; }

p { margin: 0.8rem 0; }

a { color: #1a6fd4; text-decoration: none; }
a:hover { text-decoration: underline; }

@media (prefers-color-scheme: dark) {
    a { color: #6ab0ff; }
}

blockquote {
    margin: 1rem 0;
    padding-left: 1rem;
    border-left: 3px solid var(--color-bg-secondary);
    color: var(--color-secondary);
}

ul, ol { padding-left: 1.4rem; }

img { max-width: 100%; height: auto; }

pre {
    overflow-x: auto;
    padding: 0.75rem 1rem;
    background-color: var(--color-bg-secondary);
    border-radius: 4px;
    font-size: 0.9rem;
}

code {
    font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
}

hr {
    border: none;
    border-top: 1px solid var(--color-bg-secondary);
    margin: 2rem 0;
}

/* search box */
form.search { margin: 0 0 2rem; }

form.search input[type="search"] {
    width: 100%;
    box-sizing: border-box;
    padding: 0.5em 0.7em;
    font-size: 1rem;
    font-family: inherit;
    border: 1px solid var(--color-secondary);
    border-radius: 4px;
    background-color: #fff;
    color: var(--color-text);
}

@media (prefers-color-scheme: dark) {
    form.search input[type="search"] { background-color: #2a2a2a; }
}

/* pager */
nav.pager {
    display: flex;
    justify-content: space-between;
    align-items: baseline;
    gap: 1rem;
    margin: 2.5rem 0 1rem;
    font-family: "Gill Sans", "Gill Sans MT", Calibri, sans-serif;
    font-size: 0.9rem;
}

nav.pager .page-of { color: var(--color-secondary); }

footer {
    margin-top: 3rem;
    padding-top: 1rem;
    border-top: 1px solid var(--color-bg-secondary);
    font-size: 0.8rem;
    color: var(--color-secondary);
    text-align: center;
}
```

- [ ] **Step 2: Trim `shared.css` — remove the external-link arrow**

Delete this block from `shared.css` (currently lines 51–56):

```css
/* External link arrow — Textpod-only affordance for outbound links. */
section.note p > a[href^="http"]::after,
section.note li > a[href^="http"]::after {
    content: " \2197";
    font-size: 0.7em;
}
```

- [ ] **Step 3: Trim `shared.css` — remove the `#toc` sidebar**

Delete the entire `#toc` sidebar block and its trailing duplicate media query. That is everything from this comment:

```css
/* Index-page ToC: a fixed left-side sidebar listing all rendered
 * note headings, with its own filter input.  Only the index page
 * uses `#toc` (per-note pages render ToC as a marginnote). */
#toc {
```

…through the end of the **second**:

```css
@media (max-width: 1100px) {
    #toc {
        display: none;
    }
}
```

This removes all `#toc ...` rules (the sidebar container, its search input, `ul`, `li`, `a`, and both `@media (max-width: 1100px) { #toc { display: none } }` blocks). Keep the `.note-toc` rules (the per-note ToC) intact — they sit between the two `#toc` media queries, so delete the two `#toc` regions individually rather than one contiguous span:
- Region A: from `/* Index-page ToC: ... */` / `#toc {` through the first `@media (max-width: 1100px) { #toc { display: none; } }` (ends just before `/* JS-built per-note ToC. ... */`).
- Region B: the final standalone `@media (max-width: 1100px) { #toc { display: none; } }` at the end of the file.

After editing, confirm `.note-toc`, `a.tag`, `details > summary`, table, `dl`, `p.subtitle`, `.metadata`, and the `:root` `--color-*` block all remain.

- [ ] **Step 4: Verify no `#toc` rules remain**

Run: `grep -n '#toc' shared.css`
Expected: no output.

Run: `grep -n 'note-toc' shared.css`
Expected: still present (per-note ToC kept).

- [ ] **Step 5: Commit**

```bash
git add blog.css shared.css
git commit -m "feat: add blog.css, drop sidebar and link-arrow from shared.css"
```

---

## Task 5: Swap embeds and routes in `main.go`

This task does not compile to green on its own (the `index`/`notePage` rewrites in Tasks 6–7 finish it). Do Tasks 5–7 back-to-back, then build.

**Files:**
- Modify: `main.go`

- [ ] **Step 1: Replace the tufte/et-book embeds with blog.css**

In `main.go`, replace this block:

```go
//go:embed tufte.css
var tufteCSS string

//go:embed et-book
var etBookFS embed.FS
```

with:

```go
//go:embed blog.css
var blogCSS string
```

- [ ] **Step 2: Fix the `embed` import**

`embed.FS` is no longer referenced, but `//go:embed` into `string`/`[]byte` still requires the package. In the import block, change:

```go
	"embed"
```

to:

```go
	_ "embed"
```

- [ ] **Step 3: Remove the tufte basePath rewrite in `main()`**

Delete these lines from `main()`:

```go
	// tufte.css contains @font-face url("{{BASE_PATH}}/et-book/...") refs
	// so font URLs resolve under the base-path-mounted route.
	tufteCSS = strings.ReplaceAll(tufteCSS, "{{BASE_PATH}}", cfg.BasePath)
```

- [ ] **Step 4: Remove the et-book route**

In the `mux` setup, delete this line:

```go
	mux.Handle("GET /et-book/", http.FileServerFS(etBookFS))
```

- [ ] **Step 5: Delete the obsolete files**

```bash
git rm tufte.css
git rm -r et-book
```

- [ ] **Step 6: Commit** (build still red until Task 7 — that's expected)

```bash
git add main.go
git commit -m "refactor: swap tufte/et-book embeds for blog.css"
```

---

## Task 6: Rewrite `Server.index` for server-side render

**Files:**
- Modify: `main.go` (`Server.index`)
- Modify: `index.html`

- [ ] **Step 1: Replace `index.html` with a static template**

Overwrite `index.html` (entire file):

```html
<!DOCTYPE html>
<html>

<head>
    <title>Textpod</title>
    <meta name="color-scheme" content="light dark" />
    <meta name="viewport" content="width=device-width, initial-scale=1" />
    <link rel="shortcut icon" href="{{FAVICON}}" />
    <style>
        {{BLOG_CSS}}
        {{SHARED_CSS}}
    </style>
</head>

<body>
    {{SEARCH_BOX}}
    <main id="notes">{{NOTES}}</main>
    {{PAGER}}
    <footer>Textpod</footer>
</body>

</html>
```

- [ ] **Step 2: Rewrite `Server.index`**

Replace the entire `func (s *Server) index(...)` with:

```go
func (s *Server) index(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if provided := q.Get("token"); provided != "" && s.HasToken && provided == s.Token {
		cookiePath := "/"
		redirect := "/"
		if s.BasePath != "" {
			cookiePath = s.BasePath
			redirect = s.BasePath
		}
		w.Header().Set("Set-Cookie", fmt.Sprintf("textpod_token=%s; Path=%s; HttpOnly; SameSite=Strict", provided, cookiePath))
		w.Header().Set("Location", redirect)
		w.WriteHeader(http.StatusSeeOther)
		return
	}

	search := strings.TrimSpace(q.Get("q"))
	page := parsePage(q.Get("page"))

	s.mu.Lock()
	notes := make([]Note, 0, len(s.Notes))
	for i := len(s.Notes) - 1; i >= 0; i-- {
		notes = append(notes, s.Notes[i])
	}
	s.mu.Unlock()

	notes = filterNotes(notes, search)
	pageNotes, page, pages := paginateNotes(notes, page, pageSize)

	var b strings.Builder
	for _, n := range pageNotes {
		body := wrapH3InDetails(n.HTML)
		link := fmt.Sprintf(`<a href="%s/note/%s"><time datetime="%s">%s</time></a>`,
			s.BasePath, n.ID, n.Timestamp, formatTimestampWithDay(n.Timestamp))
		b.WriteString(`<section class="note">`)
		b.WriteString(injectSubtitle(body, link))
		b.WriteString("</section>\n")
	}

	out := strings.ReplaceAll(s.HTML, "{{BLOG_CSS}}", blogCSS)
	out = strings.ReplaceAll(out, "{{SHARED_CSS}}", sharedCSS)
	out = strings.ReplaceAll(out, "{{SEARCH_BOX}}", searchBoxHTML(s.BasePath, search))
	out = strings.ReplaceAll(out, "{{NOTES}}", b.String())
	out = strings.ReplaceAll(out, "{{PAGER}}", pagerHTML(s.BasePath, search, page, pages))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.WriteString(w, out)
}
```

This drops the `{{READONLY}}`/`{{TUFTE_CSS}}` substitutions and the `hasValidToken` read-only branch (index UI is always view-only now). `formatTimestampWithDay`, `wrapH3InDetails`, and `injectSubtitle` already exist in `main.go`.

- [ ] **Step 3: Build (still red until Task 7)**

Run: `go build .`
Expected: may still fail on `notePage` references to `tufteCSS`. Proceed to Task 7. (If `index` alone is the only remaining error and Task 7 isn't done, that's expected.)

- [ ] **Step 4: Commit**

```bash
git add main.go index.html
git commit -m "feat: server-render paginated index with inline search"
```

---

## Task 7: Make `notePage` view-only and Tufte-free

**Files:**
- Modify: `main.go` (`Server.notePage`)

- [ ] **Step 1: Replace the entire `func (s *Server) notePage(...)`**

```go
func (s *Server) notePage(w http.ResponseWriter, r *http.Request) {
	if provided := r.URL.Query().Get("token"); provided != "" && s.HasToken && provided == s.Token {
		cookiePath := "/"
		if s.BasePath != "" {
			cookiePath = s.BasePath
		}
		w.Header().Set("Set-Cookie", fmt.Sprintf("textpod_token=%s; Path=%s; HttpOnly; SameSite=Strict", provided, cookiePath))
		w.Header().Set("Location", r.URL.Path)
		w.WriteHeader(http.StatusSeeOther)
		return
	}
	id := r.PathValue("id")
	s.mu.Lock()
	var note *Note
	for i := range s.Notes {
		if s.Notes[i].ID == id {
			n := s.Notes[i]
			note = &n
			break
		}
	}
	s.mu.Unlock()
	if note == nil {
		http.Error(w, fmt.Sprintf("note with id %s not found", id), http.StatusNotFound)
		return
	}
	title := html.EscapeString(noteTitle(note))
	subtitleInner := fmt.Sprintf(`<time datetime="%s">%s</time> &middot; <a href="%s">back</a>`,
		note.Timestamp, formatTimestampWithDay(note.Timestamp), s.BasePath)
	noteBody := injectSubtitle(note.HTML, subtitleInner)
	page := fmt.Sprintf(`<!DOCTYPE html>
<html>
<head>
    <title>%s - Textpod</title>
    <meta name="color-scheme" content="light dark" />
    <meta name="viewport" content="width=device-width, initial-scale=1" />
    <style>
        %s
        %s
    </style>
</head>
<body>
    <section id="noteView" class="note">%s</section>
    <script>
        (function() {
            const headings = document.querySelectorAll('.note h1, .note h2, .note h3, .note h4, .note h5, .note h6');
            const subtitle = document.querySelector('.note .subtitle');
            if (headings.length >= 2 && subtitle) {
                const ul = document.createElement('ul');
                ul.style.margin = '0';
                ul.style.paddingLeft = '0';
                const minLevel = Math.min(...[...headings].map(h => parseInt(h.tagName[1])));
                headings.forEach((h, i) => {
                    const id = 'heading-' + i;
                    h.id = id;
                    const li = document.createElement('li');
                    const level = parseInt(h.tagName[1]) - minLevel;
                    li.style.marginLeft = (level * 0.6) + 'em';
                    li.style.listStyle = 'none';
                    const a = document.createElement('a');
                    const text = h.querySelector('span') ? h.querySelector('span').textContent : h.textContent;
                    a.textContent = text.trim();
                    a.href = '#' + id;
                    li.appendChild(a);
                    ul.appendChild(li);
                });
                const box = document.createElement('aside');
                box.className = 'note-toc';
                box.appendChild(ul);
                subtitle.parentNode.insertBefore(box, subtitle.nextSibling);
            }
        })();
    </script>
</body>
</html>`, title, blogCSS, sharedCSS, noteBody)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.WriteString(w, page)
}
```

This removes: the `readonly` variable and edit/delete subtitle links, the `#editor`/`#editActions` inline styles, the `<div id="noteEdit">` editor markup, the editor JS (edit/cancel/submit/delete/drag-drop), and the `idJSON`/`contentJSON`/`basePathJSON` marshals. It swaps `tufteCSS` → `blogCSS` and keeps only the per-note ToC builder script.

- [ ] **Step 2: Build**

Run: `go build .`
Expected: SUCCESS — no remaining references to `tufteCSS` or `etBookFS`.

If the compiler reports an unused import (e.g. `encoding/json` if it became unused — it should NOT, since `writeJSON`/`saveNote` still use it), resolve by confirming usage; do not remove imports that are still referenced.

- [ ] **Step 3: Run the full test suite**

Run: `go test ./...`
Expected: PASS (existing tests + Tasks 1–3 tests).

- [ ] **Step 4: Commit**

```bash
git add main.go
git commit -m "feat: make note page view-only, drop tufte/editor UI"
```

---

## Task 8: Index handler integration test

**Files:**
- Test: `main_test.go`

- [ ] **Step 1: Write the failing test**

Add to `main_test.go` (this needs `net/http/httptest`, `net/http`, and `io` — add them to the import block):

```go
func newTestServer(notes []Note) *Server {
	return &Server{
		Config: Config{},
		HTML:   indexHTML,
		Notes:  notes,
	}
}

func TestIndexPaginationAndSearch(t *testing.T) {
	// 25 notes, ascending timestamps; index shows newest first.
	notes := make([]Note, 25)
	for i := range notes {
		id := fmt.Sprintf("%02d", i)
		notes[i] = Note{
			ID:        id,
			Timestamp: fmt.Sprintf("2026-05-%02d 10:00:00", i+1),
			Content:   "note " + id,
			HTML:      "<p>note " + id + "</p>",
		}
	}
	s := newTestServer(notes)

	get := func(target string) string {
		req := httptest.NewRequest("GET", target, nil)
		rec := httptest.NewRecorder()
		s.index(rec, req)
		if rec.Code != 200 {
			t.Fatalf("GET %s: status %d", target, rec.Code)
		}
		body, _ := io.ReadAll(rec.Result().Body)
		return string(body)
	}

	t.Run("page 1 shows newest 10", func(t *testing.T) {
		body := get("/")
		if !strings.Contains(body, "/note/24") || !strings.Contains(body, "/note/15") {
			t.Errorf("page 1 should contain notes 24..15")
		}
		if strings.Contains(body, "/note/14") {
			t.Errorf("page 1 should not contain note 14")
		}
		if !strings.Contains(body, "Older posts") {
			t.Errorf("expected pager with Older posts")
		}
		if strings.Contains(body, "<textarea") {
			t.Errorf("index must not contain a textarea")
		}
	})

	t.Run("page 2 shows next slice", func(t *testing.T) {
		body := get("/?page=2")
		if !strings.Contains(body, "/note/14") || !strings.Contains(body, "/note/05") {
			t.Errorf("page 2 should contain notes 14..05")
		}
		if !strings.Contains(body, "Newer posts") {
			t.Errorf("page 2 should have Newer posts link")
		}
	})

	t.Run("search filters", func(t *testing.T) {
		body := get("/?q=note+07")
		if !strings.Contains(body, "/note/07") {
			t.Errorf("search should match note 07")
		}
		if strings.Contains(body, "/note/08") {
			t.Errorf("search should not match note 08")
		}
		// search box echoes the query
		if !strings.Contains(body, `value="note 07"`) {
			t.Errorf("search box should echo query")
		}
	})
}
```

- [ ] **Step 2: Run test to verify it fails, then passes**

Run: `go test ./... -run TestIndexPaginationAndSearch`
Expected: initially the import additions cause this to be the only new test; it should PASS once imports are added and Tasks 6–7 are in place. If it FAILS, read the assertion and fix the handler — do not weaken the test.

- [ ] **Step 3: Commit**

```bash
git add main_test.go
git commit -m "test: index pagination, search, and no-textarea integration test"
```

---

## Task 9: Full verification

**Files:** none (verification only)

- [ ] **Step 1: Build**

Run: `go build -o textpod .`
Expected: SUCCESS, binary produced.

- [ ] **Step 2: Full test suite**

Run: `go test ./...`
Expected: `ok  github.com/velppa/textpod`.

- [ ] **Step 3: `go vet`**

Run: `go vet ./...`
Expected: no output.

- [ ] **Step 4: Manual smoke test**

```bash
mkdir -p /tmp/textpod-blog-test && cd /tmp/textpod-blog-test
printf '2026-05-01 10:00:00\n\n# First note :Blog:\n\nHello world.\n\n\x0c\n\n2026-05-02 11:00:00\n\n# Second note\n\nMore text.\n\n\x0c\n' > notes.md
/Users/pavel/Developer/src/github.com/velppa/textpod/textpod --port 3999 &
sleep 1
curl -s http://127.0.0.1:3999/ | grep -c 'class="note"'   # expect >=1
curl -s http://127.0.0.1:3999/ | grep -c '<textarea'      # expect 0
curl -s http://127.0.0.1:3999/?q=Second | grep -c '/note/' # expect match for second note
curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:3999/et-book/et-book/  # expect 404
kill %1
```

Expected: a `class="note"` count ≥ 1, a `<textarea` count of 0, the search returns the matching note, and `/et-book/` returns 404.

- [ ] **Step 5: Confirm write API still present (read-only without token)**

```bash
cd /tmp/textpod-blog-test
/Users/pavel/Developer/src/github.com/velppa/textpod/textpod --port 3999 &
sleep 1
curl -s -X POST http://127.0.0.1:3999/notes -H 'Content-Type: application/json' -d '"API still works"'
sleep 1
curl -s 'http://127.0.0.1:3999/?q=API' | grep -c 'API still works'  # expect >=1
kill %1
```

Expected: the POST succeeds (no token configured ⇒ writes allowed) and the new note appears, proving the write API was retained.

---

## Self-review notes

- **Spec coverage:** server-render index (Task 6) ✓; pagination (Tasks 2, 6) ✓; inline search (Tasks 1, 3, 6) ✓; drop writer UI / keep API (Tasks 6, 7; verified Task 9 step 5) ✓; drop Tufte + et-book, add blog.css (Tasks 4, 5) ✓; trim shared.css sidebar/arrow, keep collapsing (Task 4 keeps `details`; index keeps `wrapH3InDetails`) ✓; note page view-only (Task 7) ✓; tests + build (Tasks 1–3, 8, 9) ✓.
- **Type/name consistency:** `filterNotes(notes, q)`, `paginateNotes(notes, page, size) → (slice, page, pages)`, `parsePage(s) int`, `searchBoxHTML(basePath, q)`, `pagerHTML(basePath, q, page, pages)`, `blogCSS`, `pageSize` — used identically across tasks.
- **Collapsing kept:** the user chose to keep `<h2>→details` collapsing; the index calls `wrapH3InDetails` (Task 6) and `shared.css` retains `details > summary` (Task 4).
- **Commit reminder:** the user's global CLAUDE.md says "Do NOT commit nor stage changes." The commit steps above are the plan's default cadence — at execution time, follow the user's standing instruction (skip commits/staging) unless they say otherwise.
```
