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
		{"home", "/notes/", "home-view", "Part A / The notebook", false},
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
				`<dd>12 notes</dd>`, `<dd>12.10.2026</dd>`,
				`for="note-search"`, `id="note-search"`, `action="/notes/"`,
				`href="/notes/"`, `href="/notes/#search"`, `href="#notes"`,
			} {
				if !strings.Contains(body, want) {
					t.Errorf("missing %q", want)
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
	for _, want := range []string{`<dd>0 notes</dd>`, `<dd>Not yet</dd>`, `class="empty-state"`} {
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
		`href="/notes/"`, `href="/notes/#notes"`, `href="/notes/#search"`,
		`<main id="notes">`, `<section id="noteView" class="note">`,
		`Original content.`, `A note | Hotter`,
		`/notes/theme/paper-305-tile.jpg`, `/notes/theme/starve-050.webp`,
	} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("note page missing %q", want)
		}
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
