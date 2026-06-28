package main

import (
	"fmt"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProcessTagsHeading(t *testing.T) {
	cases := []struct {
		name string
		in   string
		// substrings that must appear / must not appear in the result.
		want    []string
		notWant []string
	}{
		{
			name:    "trailing colon required (regression: :line:column)",
			in:      `<h2>Support to jumping to a :line:column</h2>`,
			want:    []string{`Support to jumping to a :line:column`},
			notWant: []string{`class="tags"`, `class="tag"`},
		},
		{
			name: "single tag",
			in:   `<h2>Note title :Blog:</h2>`,
			want: []string{
				`<span class="tags">`,
				`>Blog</a>`,
				`<span>Note title</span>`,
			},
		},
		{
			name: "multiple tags split into separate links",
			in:   `<h2>Note title :Blog:Tech:Coding:</h2>`,
			want: []string{
				`>Blog</a>`,
				`>Tech</a>`,
				`>Coding</a>`,
			},
		},
		{
			name: "tags mid-heading preserve trailing text",
			in:   `<h2>Title :Blog: trailing text</h2>`,
			want: []string{
				`>Blog</a>`,
				` trailing text</h2>`,
			},
		},
		{
			name:    "no tags, plain heading",
			in:      `<h2>Plain heading</h2>`,
			notWant: []string{`class="tags"`},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := processTags(tc.in, "")
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Errorf("expected %q in result, got %q", w, got)
				}
			}
			for _, nw := range tc.notWant {
				if strings.Contains(got, nw) {
					t.Errorf("did not expect %q in result, got %q", nw, got)
				}
			}
		})
	}
}

func TestProcessTagsBody(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    []string
		notWant []string
	}{
		{
			name:    "colon-pair without trailing colon is not a tag",
			in:      `<p>Open file :line:column for details</p>`,
			want:    []string{`Open file :line:column for details`},
			notWant: []string{`class="tag"`},
		},
		{
			name: "real tag in body becomes link",
			in:   `<p>See :Blog: now</p>`,
			want: []string{`>Blog</a>`, ` now`},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := processTags(tc.in, "")
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Errorf("expected %q in result, got %q", w, got)
				}
			}
			for _, nw := range tc.notWant {
				if strings.Contains(got, nw) {
					t.Errorf("did not expect %q in result, got %q", nw, got)
				}
			}
		})
	}
}

func TestWrapH3InDetailsOxHtml(t *testing.T) {
	in := `<article>
<h1 id="x">Top title</h1>
<section><p>Intro.</p></section>
<section>
<h2 id="y">Sub one</h2>
<section><p>Inner one.</p></section>
</section>
<section>
<h2 id="z">Sub two</h2>
<section><p>Inner two.</p></section>
</section>
</article>`
	got := wrapH3InDetails(in)
	wants := []string{
		`<h1 id="x">Top title</h1>`,
		`<details><summary>Sub one</summary>`,
		`<p>Inner one.</p>`,
		`<details><summary>Sub two</summary>`,
		`<p>Inner two.</p>`,
	}
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Errorf("expected %q in result, got %q", w, got)
		}
	}
	// Two <details> open, two close.
	if strings.Count(got, "<details>") != 2 || strings.Count(got, "</details>") != 2 {
		t.Errorf("unbalanced details tags, got %q", got)
	}
	// The original section wrappers around each h2 are consumed.
	if strings.Contains(got, `<section>
<h2`) {
		t.Errorf("expected section/h2 wrapper to be replaced, got %q", got)
	}
}

func TestWrapH3InDetailsBareH2(t *testing.T) {
	in := `<h2 id="a">First</h2><p>One.</p><h2 id="b">Second</h2><p>Two.</p>`
	got := wrapH3InDetails(in)
	wants := []string{
		`<details><summary>First</summary>`,
		`<p>One.</p>`,
		`<details><summary>Second</summary>`,
		`<p>Two.</p>`,
	}
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Errorf("expected %q in result, got %q", w, got)
		}
	}
	if strings.Count(got, "<details>") != 2 || strings.Count(got, "</details>") != 2 {
		t.Errorf("unbalanced details tags, got %q", got)
	}
}

func TestWrapH3InDetailsNoH2(t *testing.T) {
	in := `<p>Just a paragraph.</p>`
	got := wrapH3InDetails(in)
	if got != in {
		t.Errorf("expected input unchanged, got %q", got)
	}
}

func TestRewriteFileLinks(t *testing.T) {
	cases := []struct {
		name     string
		in       string
		basePath string
		known    []string
		want     string
	}{
		{
			name: "bare html link",
			in:   `<a href="foo.html">x</a>`,
			want: `<a href="?q=foo.">x</a>`,
		},
		{
			name: "bare md link",
			in:   `<a href="foo.md">x</a>`,
			want: `<a href="?q=foo.">x</a>`,
		},
		{
			name: "parent-dir prefix",
			in:   `<a href="../foo.html">x</a>`,
			want: `<a href="?q=foo.">x</a>`,
		},
		{
			name: "multiple parent-dir prefix",
			in:   `<a href="../../foo.html">x</a>`,
			want: `<a href="?q=foo.">x</a>`,
		},
		{
			name: "subdir path",
			in:   `<a href="reference/foo.html">x</a>`,
			want: `<a href="?q=foo.">x</a>`,
		},
		{
			name: "nested subdir path",
			in:   `<a href="a/b/c/foo.md">x</a>`,
			want: `<a href="?q=foo.">x</a>`,
		},
		{
			name: "non-ID anchor dropped",
			in:   `<a href="foo.html#section">x</a>`,
			want: `<a href="?q=foo.">x</a>`,
		},
		{
			name:  "ID-prefixed numeric, known",
			in:    `<a href="foo.html#ID-20260514125132">x</a>`,
			known: []string{"20260514125132"},
			want:  `<a href="/note/20260514125132">x</a>`,
		},
		{
			name:  "bare numeric, known",
			in:    `<a href="foo.html#20260514125132">x</a>`,
			known: []string{"20260514125132"},
			want:  `<a href="/note/20260514125132">x</a>`,
		},
		{
			name:  "ID-prefixed TS in subdir, known (regression)",
			in:    `<a href="reference/the_unreasonable_effectiveness_of_html.html#ID-20260514T125132.987905">x</a>`,
			known: []string{"20260514T125132.987905"},
			want:  `<a href="/note/20260514T125132.987905">x</a>`,
		},
		{
			name:  "bare TS, known",
			in:    `<a href="foo.html#20260514T125132.987905">x</a>`,
			known: []string{"20260514T125132.987905"},
			want:  `<a href="/note/20260514T125132.987905">x</a>`,
		},
		{
			name:  "ID-prefixed UUID, known",
			in:    `<a href="foo.html#ID-f47ac10b-58cc-4372-a567-0e02b2c3d479">x</a>`,
			known: []string{"f47ac10b-58cc-4372-a567-0e02b2c3d479"},
			want:  `<a href="/note/f47ac10b-58cc-4372-a567-0e02b2c3d479">x</a>`,
		},
		{
			name:  "bare UUID, known",
			in:    `<a href="foo.html#f47ac10b-58cc-4372-a567-0e02b2c3d479">x</a>`,
			known: []string{"f47ac10b-58cc-4372-a567-0e02b2c3d479"},
			want:  `<a href="/note/f47ac10b-58cc-4372-a567-0e02b2c3d479">x</a>`,
		},
		{
			name: "ID-shaped numeric but UNKNOWN falls back",
			in:   `<a href="foo.html#ID-20260514125132">x</a>`,
			want: `<a href="?q=foo.">x</a>`,
		},
		{
			name: "ID-shaped TS but UNKNOWN falls back",
			in:   `<a href="foo.html#20260514T125132.987905">x</a>`,
			want: `<a href="?q=foo.">x</a>`,
		},
		{
			name: "ID-shaped UUID but UNKNOWN falls back",
			in:   `<a href="foo.html#f47ac10b-58cc-4372-a567-0e02b2c3d479">x</a>`,
			want: `<a href="?q=foo.">x</a>`,
		},
		{
			name: "non-ID-shaped ID-prefix anchor falls back",
			in:   `<a href="foo.html#ID-not-an-id">x</a>`,
			want: `<a href="?q=foo.">x</a>`,
		},
		{
			name: "wrong-length numeric anchor falls back",
			in:   `<a href="foo.html#1234">x</a>`,
			want: `<a href="?q=foo.">x</a>`,
		},
		{
			name:  "arbitrary string id, known",
			in:    `<a href="foo.html#ID-my-custom-id">x</a>`,
			known: []string{"my-custom-id"},
			want:  `<a href="/note/my-custom-id">x</a>`,
		},
		{
			name:     "known TS anchor with basePath",
			in:       `<a href="foo.html#20260514T125132.987905">x</a>`,
			basePath: "/notes",
			known:    []string{"20260514T125132.987905"},
			want:     `<a href="/notes/note/20260514T125132.987905">x</a>`,
		},
		{
			name:     "known UUID anchor with basePath",
			in:       `<a href="foo.html#ID-f47ac10b-58cc-4372-a567-0e02b2c3d479">x</a>`,
			basePath: "/notes",
			known:    []string{"f47ac10b-58cc-4372-a567-0e02b2c3d479"},
			want:     `<a href="/notes/note/f47ac10b-58cc-4372-a567-0e02b2c3d479">x</a>`,
		},
		{
			name:     "bare link with basePath",
			in:       `<a href="foo.md">x</a>`,
			basePath: "/notes",
			want:     `<a href="/notes?q=foo.">x</a>`,
		},
		{
			name: "external http link untouched",
			in:   `<a href="http://example.com/foo.html">x</a>`,
			want: `<a href="http://example.com/foo.html">x</a>`,
		},
		{
			name: "external https link untouched",
			in:   `<a href="https://example.com/foo.md#x">x</a>`,
			want: `<a href="https://example.com/foo.md#x">x</a>`,
		},
		{
			name: "non html/md extension untouched",
			in:   `<a href="foo.pdf">x</a>`,
			want: `<a href="foo.pdf">x</a>`,
		},
		{
			name:  "multiple links one pass",
			in:    `<a href="a.html">a</a> <a href="b.md#ID-20260514125132">b</a>`,
			known: []string{"20260514125132"},
			want:  `<a href="?q=a.">a</a> <a href="/note/20260514125132">b</a>`,
		},
		{
			name: "filename with special chars url-escaped",
			in:   `<a href="foo bar.html">x</a>`,
			want: `<a href="?q=foo+bar.">x</a>`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			set := make(map[string]struct{}, len(tc.known))
			for _, k := range tc.known {
				set[k] = struct{}{}
			}
			isKnown := func(id string) bool { _, ok := set[id]; return ok }
			got := rewriteFileLinks(tc.in, tc.basePath, isKnown)
			if got != tc.want {
				t.Errorf("rewriteFileLinks(%q, %q) =\n  got:  %q\n  want: %q", tc.in, tc.basePath, got, tc.want)
			}
		})
	}

	t.Run("nil predicate -> all anchors fall back", func(t *testing.T) {
		got := rewriteFileLinks(`<a href="foo.html#ID-20260514125132">x</a>`, "", nil)
		want := `<a href="?q=foo.">x</a>`
		if got != want {
			t.Errorf("got %q want %q", got, want)
		}
	})
}

func TestIDToTimestamp(t *testing.T) {
	cases := []struct {
		name   string
		id     string
		wantTS string
		wantOK bool
	}{
		{"numeric", "20260514125132", "2026-05-14 12:51:32", true},
		{"ts with fraction", "20260514T125132.987905", "2026-05-14 12:51:32", true},
		{"ts no fraction", "20260514T125132", "2026-05-14 12:51:32", true},
		{"uuid not recoverable", "f47ac10b-58cc-4372-a567-0e02b2c3d479", "", false},
		{"empty", "", "", false},
		{"short numeric", "12345", "", false},
		{"non-digit", "20260514T12513x", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotTS, gotOK := idToTimestamp(tc.id)
			if gotOK != tc.wantOK || gotTS != tc.wantTS {
				t.Errorf("idToTimestamp(%q) = (%q,%v), want (%q,%v)", tc.id, gotTS, gotOK, tc.wantTS, tc.wantOK)
			}
		})
	}
}

func TestParseHeaderLine(t *testing.T) {
	cases := []struct {
		in     string
		wantTS string
		wantID string
	}{
		{"2026-05-14 12:51:32", "2026-05-14 12:51:32", ""},
		{"2026-05-14 12:51:32 20260514125132", "2026-05-14 12:51:32", "20260514125132"},
		{"2026-05-14 12:51:32  20260514T125132.987654", "2026-05-14 12:51:32", "20260514T125132.987654"},
		{"2026-05-14 12:51:32     f47ac10b-58cc-4372-a567-0e02b2c3d479", "2026-05-14 12:51:32", "f47ac10b-58cc-4372-a567-0e02b2c3d479"},
		{"  2026-05-14 12:51:32    20260514T125132.987654  ", "2026-05-14 12:51:32", "20260514T125132.987654"},
		{"2026-05-14 12:51:32\r", "2026-05-14 12:51:32", ""},
		// historical tab-separated header still parses
		{"2026-05-14 12:51:32\t20260514T125132.987654", "2026-05-14 12:51:32", "20260514T125132.987654"},
	}
	for _, tc := range cases {
		ts, id := parseHeaderLine(tc.in)
		if ts != tc.wantTS || id != tc.wantID {
			t.Errorf("parseHeaderLine(%q) = (%q,%q), want (%q,%q)", tc.in, ts, id, tc.wantTS, tc.wantID)
		}
	}
}

func TestFormatHeaderLine(t *testing.T) {
	cases := []struct {
		ts, id, want string
	}{
		// derivable id → timestamp only
		{"2026-05-14 12:51:32", "20260514125132", "2026-05-14 12:51:32"},
		{"2026-05-14 12:51:32", "", "2026-05-14 12:51:32"},
		// non-derivable → space + id appended
		{"2026-05-14 12:51:32", "20260514T125132.987654", "2026-05-14 12:51:32 20260514T125132.987654"},
		{"2026-05-14 12:51:32", "f47ac10b-58cc-4372-a567-0e02b2c3d479", "2026-05-14 12:51:32 f47ac10b-58cc-4372-a567-0e02b2c3d479"},
	}
	for _, tc := range cases {
		got := formatHeaderLine(tc.ts, tc.id)
		if got != tc.want {
			t.Errorf("formatHeaderLine(%q,%q) = %q, want %q", tc.ts, tc.id, got, tc.want)
		}
	}
}

func TestHeaderRoundTrip(t *testing.T) {
	cases := []struct {
		ts, id string
	}{
		{"2026-05-14 12:51:32", "20260514125132"},
		{"2026-05-14 12:51:32", "20260514T125132.987654"},
		{"2026-05-14 12:51:32", "f47ac10b-58cc-4372-a567-0e02b2c3d479"},
	}
	for _, tc := range cases {
		line := formatHeaderLine(tc.ts, tc.id)
		gotTS, gotID := parseHeaderLine(line)
		if gotTS != tc.ts {
			t.Errorf("ts round-trip: got %q want %q (line=%q)", gotTS, tc.ts, line)
		}
		// derivable id is omitted on disk, recovered by caller via timestampToID
		wantID := tc.id
		if wantID == timestampToID(tc.ts) {
			wantID = ""
		}
		if gotID != wantID {
			t.Errorf("id round-trip: got %q want %q (line=%q)", gotID, wantID, line)
		}
	}
}

func TestProcessTagsSpanFormat(t *testing.T) {
	in := `<h2>Title&#xa0;<span class="tag">` +
		`<span class="Blog">Blog</span>&#xa0;` +
		`<span class="Tech">Tech</span></span></h2>`
	got := processTags(in, "")
	for _, w := range []string{`>Blog</a>`, `>Tech</a>`, `<span>Title</span>`} {
		if !strings.Contains(got, w) {
			t.Errorf("expected %q in result, got %q", w, got)
		}
	}
}

func TestFilterNotes(t *testing.T) {
	notes := []Note{
		{ID: "1", Timestamp: "2026-05-14 10:00:00", Content: "Hello World"},
		{ID: "2", Timestamp: "2026-06-01 09:30:00", Content: "Go programming"},
		{ID: "3", Timestamp: "2026-06-01 11:00:00", Content: "another GO note"},
	}
	cases := []struct {
		name string
		q    string
		want []string
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
		wantFirst string
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

func newTestServer(notes []Note) *Server {
	return &Server{
		Config: Config{},
		HTML:   indexHTML,
		Notes:  notes,
	}
}

func TestIndexPaginationAndSearch(t *testing.T) {
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
		if !strings.Contains(body, `value="note 07"`) {
			t.Errorf("search box should echo query")
		}
	})
}
