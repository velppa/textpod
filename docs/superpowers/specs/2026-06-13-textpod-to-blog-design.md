# Textpod → Blog: Design

Convert Textpod from a single-page, client-rendered note app into a normal
server-rendered blog: honeypot.net-style single-column layout (no Tufte),
server-side pagination, no sidebar, and no in-app writing UI. The HTTP write
API stays so `textpod.el` keeps publishing.

## Goals

- Server-rendered index with full note bodies, paginated.
- Inline server-side search.
- Clean single-column blog styling; remove Tufte CSS and the et-book font.
- Remove all in-app writing/editing UI (textarea, submit, drag-drop, edit/delete links).
- Keep the write API (`POST/PUT/DELETE /notes`, `POST /upload`, asset PUT) and token auth.

## Non-goals

- Author/byline display, multi-page navigation (Archive), RSS, comments.
- Changing the on-disk `notes.md` format, ids, tags, or snapshot syntax.

## Current state (baseline)

- `GET /{$}` serves `index.html`, which client-renders: JS `fetch('/notes')` →
  `displayNotes()` builds all notes into `#notes`; a `#toc` fixed sidebar lists
  headings; a `<textarea id="editor">` doubles as writer (Ctrl+Enter) and
  search (`/` prefix); `READONLY` toggles the writer off.
- `GET /note/{id}` (`notePage`) server-renders one note with an edit/delete
  editor `<script>` and a JS-built per-note ToC.
- Styling: `tufte.css` (base typography/width/color/sidenotes) + `shared.css`
  (Textpod overlay: tags, details, tables, dl, subtitle, `#toc`, `.note-toc`).
- Notes carry precomputed `.HTML` from `loadNotes`. `injectSubtitle` and
  `wrapH3InDetails` exist in **both** Go (`main.go`) and JS (`index.html`).

## Architecture changes

### 1. Server-rendered, paginated index

`GET /{$}` (`Server.index`) renders the complete page HTML server-side instead
of returning a JS shell.

- Read `q` and `page` from the query string.
- Build the note list newest-first (reverse of `s.Notes`).
- If `q` is non-empty, filter: keep notes whose lower-cased `Content` contains
  lower-cased `q`, OR whose `Timestamp` contains `q` (same predicate as the
  current JS filter in `displayNotes`).
- Paginate the filtered list: page size 10, 1-based `page`, default 1. Clamp
  out-of-range pages (page < 1 → 1; page > last → last; empty result → page 1,
  no notes).
- For each note on the page, produce its section HTML using the existing
  server-side `injectSubtitle` and `wrapH3InDetails`. The subtitle inner is the
  same `<a href="{BASE_PATH}/note/{id}"><time …>…</time></a>` the JS built.
- Concatenate sections, wrap with the search box (§3) and pager (§2), emit full
  HTML document with `blog.css` + `shared.css` inlined.

`injectSubtitle` and `wrapH3InDetails` JS copies in `index.html` are deleted;
the Go versions are the single source of truth. `getNotes` already applies
`wrapH3InDetails`; the index handler does the same per note.

`GET /notes` (JSON) is kept unchanged as an API surface.

### 2. Pagination UI

Rendered at the bottom of the index:

- `← Newer posts` link when `page > 1` → `?q=<q>&page=<page-1>`.
- `Older posts →` link when more notes remain → `?q=<q>&page=<page+1>`.
- A `Page X of Y` indicator between them.
- `q` is preserved (URL-encoded) across page links; omitted when empty.

### 3. Inline search

- A `<form method="GET" action="{BASE_PATH}/">` with a single
  `<input type="search" name="q">` (placeholder e.g. "Search…"), prefilled with
  the current `q`. Placed above the note list. No site header/branding chrome.
- Submitting reloads the index with `?q=…` (page resets to 1, i.e. `page` is
  not carried from the form).
- `:tag:` links continue to work unchanged — `tagsToLinks` emits `?q=:tag:`,
  which the index filter handles.

### 4. Remove writing UI; keep write API

`index.html`:

- Remove `<textarea id="editor">`, `#submitContainer`/`#submitButton`, the
  drag/drop upload handler, the `/`-search handler, `saveNotes`, and all
  `READONLY` UI logic.
- Remove the `{{READONLY}}` template variable and its substitution in
  `Server.index`. Pages are always view-only in the browser.

`notePage` (`/note/{id}`):

- Remove the edit/delete links from the subtitle and the entire editor
  `<script>` (edit/cancel/submit/delete/drag-drop). Keep the per-note ToC
  builder script and view rendering.
- Subtitle becomes `<time>…</time> · <a href="{BASE_PATH}">back</a>` only.

Kept unchanged (server-side, still token-guarded):

- `POST /notes`, `PUT /notes/{id}`, `DELETE /notes/{id}`, `POST /upload`,
  `PUT/HEAD/GET /assets/{name…}`, `hasValidToken`/`requireAuth`, the
  token-cookie redirect in `index`/`notePage`.

### 5. Styling — drop Tufte

Remove:

- `//go:embed tufte.css` + `tufteCSS` var, the `{{BASE_PATH}}` rewrite of it,
  and `{{TUFTE_CSS}}` substitution in both `index` and `notePage`.
- `//go:embed et-book` + `etBookFS` and the `GET /et-book/` route.
- `tufte.css` and the `et-book/` directory from the repo and embeds.

Add `blog.css` (new embedded stylesheet, honeypot.net look):

- Single centered column: `body { max-width: ~40rem; margin: 2rem auto; padding: 0 1rem; }`.
- System/serif font stack; comfortable line-height.
- Colors: white background, near-black text, blue links, gray metadata; dark
  mode via `@media (prefers-color-scheme: dark)`. Defines the base typography,
  width, and color tokens that `tufte.css` previously provided, including the
  `--color-*` tokens `shared.css` consumes.
- Base styling for headings, paragraphs, blockquote, lists, inline/blocks code,
  images — replacing what Tufte supplied.

Trim `shared.css`:

- Remove the `#toc` fixed-sidebar block (and its `@media` rules) — no sidebar.
- Remove the external-link `↗` arrow rules (`a[href^="http"]::after`) — cleaner
  blog.
- Keep: tag chips (`a.tag`), `details/summary`, dense tables, `dl`,
  `p.subtitle` metadata, `.metadata`/`.noteMetadata` links, `.note-toc`
  (per-note ToC), `section.note img`, the `--color-*` token block,
  inter-note spacing. (The `--color-*` tokens may move to `blog.css`; keep one
  authoritative definition.)
- Note: `shared.css` no longer layers on Tufte, so any rule that depended on a
  Tufte default (e.g. overriding `p.subtitle` size) is reconciled against
  `blog.css` instead.

`index.html` inline `<style>`: remove the `margin-left: 17%` sidebar reservation
and the editor/submit styles; keep/move note-section spacing and subtitle tweaks
(or fold into `blog.css`).

## Data flow (index request)

```
GET /?q=foo&page=2
  → parse q, page
  → notes = reverse(s.Notes)
  → if q: notes = filter(notes, q)
  → total = len(notes); pages = ceil(total/10); page = clamp(page,1,pages)
  → slice = notes[(page-1)*10 : page*10]
  → for n in slice: section = injectSubtitle(wrapH3InDetails(n.HTML), <time/link>)
  → render: <style>blog.css shared.css</style> + searchBox(q) + sections + pager(page,pages,q)
```

## Error handling

- Bad/non-numeric `page` → treat as 1.
- `page` out of range → clamp into `[1, lastPage]`.
- Empty notes / empty filtered result → render search box + pager (page 1 of 0
  or 1) with an empty list; no error.
- Token-cookie redirect behavior in `index`/`notePage` unchanged.

## Testing

- Keep existing `wrapH3InDetails` / `injectSubtitle` / tag / link-rewrite tests.
- Add: index pagination slice (page size, newest-first order, clamping) and
  search filter (content + timestamp substring, case-insensitive).
- Remove any test asserting et-book/tufte routes or the `{{READONLY}}`/editor UI.
- `go build .` and `go test ./...` must pass before completion.

## File touch list

- `main.go` — rewrite `index`; trim `notePage`; drop tufte/et-book embeds +
  route; remove `{{READONLY}}`; add pagination/search/render helpers.
- `index.html` — strip to a server-rendered template (search box + notes +
  pager); delete writer/search JS and JS `injectSubtitle`/`wrapH3InDetails`.
- `blog.css` — new.
- `shared.css` — trim sidebar + link-arrow; keep overlay; reconcile base.
- `tufte.css`, `et-book/` — delete.
- `main_test.go` — adjust per above.
