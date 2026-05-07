package main

import (
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
