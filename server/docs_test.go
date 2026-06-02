package main

import (
	"strings"
	"testing"
)

// TestDocsRender confirms the embedded markdown docs still render to
// non-empty HTML after dropping html.WithUnsafe(), and that GFM tables
// (which several pages rely on) survive — table cells are produced by
// goldmark's renderer, not by raw-HTML passthrough, so they must be
// unaffected by the unsafe-render change.
func TestDocsRender(t *testing.T) {
	docsOnce.Do(loadDocs)
	if len(docsOrdered) == 0 {
		t.Fatal("no docs loaded from embed.FS")
	}
	for _, p := range docsOrdered {
		if strings.TrimSpace(p.HTML) == "" {
			t.Errorf("doc %q rendered to empty HTML", p.Slug)
		}
	}
	// troubleshooting.md is table-heavy; assert real <table> markup is
	// emitted by the renderer.
	if tp, ok := docsBySlug["troubleshooting"]; ok {
		if !strings.Contains(tp.HTML, "<table>") {
			t.Errorf("troubleshooting doc lost its <table> rendering: %.200q", tp.HTML)
		}
	}
}

// TestDocsEscapesRawHTML is the security regression guard for M3: with
// html.WithUnsafe() removed, any literal raw HTML in markdown must come
// out HTML-escaped, never as a live element. The rendered HTML is
// injected into the SPA via dangerouslySetInnerHTML, so a live <script>
// here would be stored XSS.
func TestDocsEscapesRawHTML(t *testing.T) {
	src := []byte("# Title\n\nbefore <script>alert(1)</script> after\n")
	page := parseDocPage("xss-probe.md", src)
	// With html.WithUnsafe() removed, goldmark neutralises raw HTML.
	// It must NOT emit a live <script> element; the safe-text payload
	// ("alert(1)") may pass through as inert text, but no executable
	// markup may survive.
	if strings.Contains(page.HTML, "<script>") || strings.Contains(page.HTML, "</script>") {
		t.Fatalf("raw <script> survived rendering — XSS sink reopened: %q", page.HTML)
	}
	// Sanity: the surrounding prose is still rendered.
	if !strings.Contains(page.HTML, "before") || !strings.Contains(page.HTML, "after") {
		t.Fatalf("surrounding prose lost during render: %q", page.HTML)
	}
}
