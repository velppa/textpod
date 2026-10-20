package main

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBlogIndexViews(t *testing.T) {
	notes := make([]Note, 12)
	for i := range notes {
		notes[i] = Note{
			ID:        fmt.Sprintf("202610%02d120000", i+1),
			Timestamp: fmt.Sprintf("2026-10-%02d 12:00:00", i+1),
			Content:   fmt.Sprintf("entry-%02d", i),
			HTML:      fmt.Sprintf("<h1>Entry %02d</h1><p>Content.</p>", i),
		}
	}
	s := newTestServer(notes)
	s.BasePath = "/notes"
	s.HTML = strings.ReplaceAll(s.HTML, "{{FAVICON}}", "data:image/svg+xml;base64,")

	for _, tc := range []struct {
		name, target, view, label string
		wantEmpty                 bool
	}{
		{"home", "/notes/", "home-view", "", false},
		{"search", "/notes/?q=entry-03", "archive-view", "Search / 1 matching notes", false},
		{"archive", "/notes/?page=2", "archive-view", "Archive / Page 2 of 2", false},
		{"no matches", "/notes/?q=missing", "archive-view", "Search / 0 matching notes", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			s.index(rec, httptest.NewRequest("GET", tc.target, nil))
			body := rec.Body.String()
			for _, want := range []string{
				`class="` + tc.view + `"`, tc.label,
				`aria-label="Search notes"`, `id="note-search"`, `action="/notes/"`,
				`href="/notes/"`, `href="/vinboard/"`, `href="/youtube-summaries/"`, `href="/"`, `<span>Vinboard</span>`,
			} {
				if !strings.Contains(body, want) {
					t.Errorf("missing %q", want)
				}
			}
			for _, gone := range []string{`class="hero"`, "Part A", "Look something up", "A word, a topic", "#search"} {
				if strings.Contains(body, gone) {
					t.Errorf("page still renders %q", gone)
				}
			}
			if strings.Contains(body, "{{") {
				t.Error("page contains an unresolved template placeholder")
			}
			if strings.Contains(body, `class="empty-state"`) != tc.wantEmpty {
				t.Error("empty state does not match the search results")
			}
		})
	}
}

func TestBlogEmptyArchive(t *testing.T) {
	s := newTestServer(nil)
	rec := httptest.NewRecorder()
	s.index(rec, httptest.NewRequest("GET", "/", nil))
	for _, want := range []string{`class="empty-state"`} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("empty archive missing %q", want)
		}
	}
}

func TestBlogNotePageNavigation(t *testing.T) {
	s := newTestServer([]Note{{
		ID: "42", Timestamp: "2026-10-06 12:00:00",
		Content: "# A note", HTML: `<article><h1>A note</h1><section><p>Original content.</p></section></article>`,
	}})
	s.BasePath = "/notes"
	req := httptest.NewRequest("GET", "/notes/note/42", nil)
	req.SetPathValue("id", "42")
	rec := httptest.NewRecorder()
	s.notePage(rec, req)
	for _, want := range []string{
		`class="note-page"`, `aria-label="Main navigation"`,
		`href="/notes/"`, `href="/vinboard/"`, `href="/youtube-summaries/"`, `href="/"`,
		`<main id="notes">`, `<section id="noteView" class="note">`,
		`Original content.`, `A note | Hotter`,
		`/notes/theme/paper-305-tile.jpg`, `/notes/theme/starve-050.webp`,
	} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("note page missing %q", want)
		}
	}
}

func TestBlogNotePagePermalinkUnderTitle(t *testing.T) {
	s := newTestServer([]Note{{
		ID: "42", Timestamp: "2026-10-06 12:00:00",
		Content: "# A note", HTML: `<h1>A note</h1><p>Body.</p>`,
	}})
	s.BasePath = "/notes"
	req := httptest.NewRequest("GET", "/notes/note/42", nil)
	req.SetPathValue("id", "42")
	rec := httptest.NewRecorder()
	s.notePage(rec, req)
	want := `<h1>A note</h1><p class="subtitle"><a href="/notes/note/42"><time datetime="2026-10-06 12:00:00">`
	if !strings.Contains(rec.Body.String(), want) {
		t.Errorf("note page lacks the timestamp permalink right under the title")
	}
}

func TestThemeTextures(t *testing.T) {
	mux := http.NewServeMux()
	mux.Handle("GET /theme/", http.FileServerFS(themeFiles))
	handler := http.StripPrefix("/notes", mux)
	for _, tc := range []struct {
		name, contentType string
		prefix            []byte
	}{
		{"paper-305-tile.jpg", "image/jpeg", []byte{0xff, 0xd8}},
		{"paper-305-dark-tile.jpg", "image/jpeg", []byte{0xff, 0xd8}},
		{"starve-050.webp", "image/webp", []byte("RIFF")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, httptest.NewRequest("GET", "/notes/theme/"+tc.name, nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("texture returned status %d", rec.Code)
			}
			if got := rec.Header().Get("Content-Type"); got != tc.contentType {
				t.Errorf("Content-Type = %q, want %q", got, tc.contentType)
			}
			if !bytes.HasPrefix(rec.Body.Bytes(), tc.prefix) {
				t.Error("response is not the expected image format")
			}
		})
	}
}

func TestNoteToHTMLRepairsLegacyEpigraphs(t *testing.T) {
	for _, tc := range []struct {
		name, input, want string
	}{
		{
			"legacy quote before another section",
			`<article><section><div class="epigraph"><blockquote><p>Quote.</p></blockquote></section><p>After.</p></section><section><dl><dt>Author</dt><dd>Daisy</dd></dl></section></article>`,
			`<article><section><div class="epigraph"><blockquote><p>Quote.</p></blockquote></div><p>After.</p></section><section><dl><dt>Author</dt><dd>Daisy</dd></dl></section></article>`,
		},
		{
			"ordinary blockquote remains a section",
			`<section><blockquote><p>Quote.</p></blockquote></section>`,
			`<section><blockquote><p>Quote.</p></blockquote></section>`,
		},
		{
			"valid nested quote stays intact",
			`<div class="epigraph"><blockquote><section><blockquote>Inner.</blockquote></section></blockquote></div>`,
			`<div class="epigraph"><blockquote><section><blockquote>Inner.</blockquote></section></blockquote></div>`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := noteToHTML(tc.input, "", nil); got != tc.want {
				t.Errorf("rendered HTML = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestNoteToHTMLWrapsMindmaps(t *testing.T) {
	input := "<section><p>Before.</p><pre class=\"mindmap\">  root ─┬─ branch\n        ╰─ leaf</pre><pre class=\"code\">Code.</pre><p>After.</p></section>"
	want := "<section><p>Before.</p><div class=\"mindmap-frame\"><pre class=\"mindmap\">  root ─┬─ branch\n        ╰─ leaf</pre></div><pre class=\"code\">Code.</pre><p>After.</p></section>"
	if got := noteToHTML(input, "", nil); got != want {
		t.Errorf("mindmap rendering = %q, want %q", got, want)
	}
}

func TestNoteToHTMLMovesSidenotesToFootnotes(t *testing.T) {
	input := `<article><h1>T</h1><section><p>One<label for="sn-1" class="margin-toggle sidenote-number"></label><input type="checkbox" id="sn-1" class="margin-toggle"/><span class="sidenote">First <span class="x">nested</span>.</span> two<label for="sn-2" class="margin-toggle sidenote-number"></label><input type="checkbox" id="sn-2" class="margin-toggle"/><span class="sidenote">Code:
<pre class="code"><code>(+ 1 2)
</code></pre></span>.</p></section></article>
`
	got := noteToHTML(input, "", nil)
	for _, want := range []string{
		`One<sup class="footnote-ref"><a href="#fn-`, `-1">1</a></sup> two<sup class="footnote-ref">`,
		`<footer class="footnotes"><ol><li id="fn-`, `First <span class="x">nested</span>. <a class="footnote-back"`,
		"Code:\n<pre class=\"code\"><code>(+ 1 2)\n</code></pre> <a class=\"footnote-back\"",
		`</ol></footer></article>`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("footnotes missing %q in %q", want, got)
		}
	}
	for _, gone := range []string{"margin-toggle", `class="sidenote"`} {
		if strings.Contains(got, gone) {
			t.Errorf("rendered note still contains %q", gone)
		}
	}
	stripped := `<p>A<label for="sn-1" class="margin-toggle sidenote-number"></label><input type="checkbox" id="sn-1" class="margin-toggle"/><span class="sidenote">Uses <code>f</code>:

<code>(f 1)
(f 2)
</code></span>.</p>`
	got = noteToHTML(stripped, "", nil)
	if !strings.Contains(got, "Uses <code>f</code>:\n\n<pre class=\"code\"><code>(f 1)\n(f 2)\n</code></pre>") {
		t.Errorf("multi-line footnote code is not a source block: %q", got)
	}
	if strings.Contains(got, `<pre class="code"><code>f</code>`) {
		t.Error("inline footnote code became a source block")
	}
	if plain := "<p>No notes.</p>"; noteToHTML(plain, "", nil) != plain {
		t.Error("note without sidenotes changed")
	}
}
