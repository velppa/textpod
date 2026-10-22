package main

import (
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
				`href="/notes/"`, `href="/vinboard/"`, `href="/youtube-summaries/"`, `href="/"`, `class="selected nav-notes" href="/notes/"`,
				`class="site-footer wrap"`,
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
			if strings.Contains(body, `class="post-year">2026</h2>`) == tc.wantEmpty {
				t.Error("post list does not match the search results")
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
		`family=Fraunces`, `class="site-footer wrap"`,
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
	want := `<h1>A note</h1><p class="subtitle"><a href="/notes/note/42"><time datetime="2026-10-06 12:00:00">` +
		`<span class="date-long">2026-10-06 Tue 12:00:00</span><span class="date-short">Oct 6, 2026</span></time></a>`
	if !strings.Contains(rec.Body.String(), want) {
		t.Errorf("note page lacks the timestamp permalink right under the title")
	}
}

func TestBlogIndexListsPosts(t *testing.T) {
	s := newTestServer([]Note{
		{ID: "1", Timestamp: "2025-12-30 09:00:00", Content: "# Old", HTML: `<h1>Old</h1><dl><dt>k</dt><dd>v</dd></dl><p></p><p>First <b>words</b> &amp; more.</p>`},
		{ID: "2", Timestamp: "2026-09-25 10:00:00", Content: "# New", HTML: `<h1>New</h1><p>` + strings.Repeat("long ", 60) + `</p><img src="/notes/assets/a.png" width="10">`},
	})
	s.BasePath = "/notes"
	rec := httptest.NewRecorder()
	s.index(rec, httptest.NewRequest("GET", "/notes/", nil))
	body := rec.Body.String()
	for _, want := range []string{
		`<h2 class="post-year">2026</h2><div class="entry"><a class="post" href="/notes/note/2">`,
		`<h3 class="post-title">New</h3><time class="post-date" datetime="2026-09-25 10:00:00">Sep 25</time>`,
		`long long…</p></div><img class="post-thumb" src="/notes/assets/a.png" alt="" loading="lazy"></a><section class="note"><h1>New</h1><p class="subtitle">`,
		`<h2 class="post-year">2025</h2><div class="entry"><a class="post" href="/notes/note/1">`,
		`<p class="post-summary">First words &amp; more.</p></div></a><section class="note">`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("post list missing %q", want)
		}
	}
}

func TestInjectSubtitleRendersTagsBothWays(t *testing.T) {
	got := injectSubtitle(`<h1><span>Title</span> <span class="tags"><a class="tag" href="?q=%3AEmacs%3A">Emacs</a></span></h1><p>Body.</p>`, "date")
	want := `<p class="note-tags"><a class="tag" href="?q=%3AEmacs%3A">Emacs</a></p><h1><span>Title</span></h1>` +
		`<p class="subtitle">date &middot; <span class="tags"><a class="tag" href="?q=%3AEmacs%3A">:Emacs:</a></span></p><p>Body.</p>`
	if got != want {
		t.Errorf("injectSubtitle = %q, want %q", got, want)
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

func TestThemesShareMarkupAndSwapStylesheets(t *testing.T) {
	s := newTestServer([]Note{{
		ID: "42", Timestamp: "2026-10-06 12:00:00",
		Content: "# A note", HTML: `<h1>A note</h1><p>Body.</p>`,
	}})
	s.BasePath = "/notes"
	s.HTML = strings.ReplaceAll(s.HTML, "{{FAVICON}}", "data:image/svg+xml;base64,")
	if len(themeCSS) < 2 || themeCSS["clay"] == "" || themeCSS["notebook"] == "" {
		t.Fatalf("designs = %v, want clay and notebook", themeNames())
	}
	render := func(target, cookie string) (*httptest.ResponseRecorder, string) {
		req := httptest.NewRequest("GET", target, nil)
		if cookie != "" {
			req.AddCookie(&http.Cookie{Name: "textpod_theme", Value: cookie})
		}
		rec := httptest.NewRecorder()
		s.index(rec, req)
		body := rec.Body.String()
		return rec, body[:strings.Index(body, "<style>")] + body[strings.Index(body, "</style>"):]
	}
	cssOf := func(target, cookie string) string {
		req := httptest.NewRequest("GET", target, nil)
		if cookie != "" {
			req.AddCookie(&http.Cookie{Name: "textpod_theme", Value: cookie})
		}
		rec := httptest.NewRecorder()
		s.index(rec, req)
		body := rec.Body.String()
		return body[strings.Index(body, "<style>"):strings.Index(body, "</style>")]
	}

	if !strings.Contains(cssOf("/notes/", ""), "Clay:") {
		t.Error("default design is not clay")
	}
	if !strings.Contains(cssOf("/notes/", "notebook"), "Notebook:") {
		t.Error("design cookie is ignored")
	}
	if !strings.Contains(cssOf("/notes/", "bogus"), "Clay:") {
		t.Error("unknown design cookie is not ignored")
	}
	s.Theme = "notebook"
	if !strings.Contains(cssOf("/notes/", ""), "Notebook:") {
		t.Error("--theme default is ignored")
	}
	s.Theme = ""

	rec, _ := render("/notes/?theme=notebook", "")
	if c := rec.Result().Cookies(); len(c) != 1 || c[0].Name != "textpod_theme" || c[0].Value != "notebook" || c[0].Path != "/notes" {
		t.Errorf("?theme= cookie = %v", c)
	}

	_, clay := render("/notes/?theme=clay", "")
	_, notebook := render("/notes/?theme=notebook", "")
	normalize := func(page string) string {
		page = strings.Replace(page, `<a href="?theme=clay" aria-current="true">`, `<a href="?theme=clay">`, 1)
		return strings.Replace(page, `<a href="?theme=notebook" aria-current="true">`, `<a href="?theme=notebook">`, 1)
	}
	if normalize(clay) != normalize(notebook) {
		t.Error("designs render different markup")
	}
	for _, want := range []string{`<a href="?theme=clay" aria-current="true">Clay</a>`, `<a href="?theme=notebook">Notebook</a>`} {
		if !strings.Contains(clay, want) {
			t.Errorf("design switch missing %q", want)
		}
	}
}
