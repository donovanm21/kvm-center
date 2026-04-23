package main

import (
	"bytes"
	"embed"
	"encoding/json"
	"io/fs"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer/html"
)

// In-app docs (v5.14.0). Markdown files under server/docs/ are
// embedded at build time, parsed for simple front matter, rendered
// to HTML on the server and served via /api/docs/*. The SPA pulls
// the tree and page HTML for the /docs route.
//
// Docs are public — no session or token required. They're
// reference material, not operational state.

//go:embed docs/*.md docs/media/*
var docsFS embed.FS

// DocPage is the flat metadata + rendered HTML for one page.
type DocPage struct {
	Slug    string `json:"slug"`
	Title   string `json:"title"`
	Section string `json:"section"`
	Order   int    `json:"order"`
	HTML    string `json:"html,omitempty"`
	TOC     []TOC  `json:"toc,omitempty"`
}

// TOC is one heading-level entry used by the SPA to render the
// right-side in-page nav.
type TOC struct {
	ID    string `json:"id"`
	Text  string `json:"text"`
	Level int    `json:"level"`
}

var (
	docsOnce     sync.Once
	docsBySlug   map[string]*DocPage
	docsOrdered  []*DocPage
	docsRenderer goldmark.Markdown
)

func loadDocs() {
	docsBySlug = map[string]*DocPage{}
	docsRenderer = goldmark.New(
		goldmark.WithExtensions(extension.GFM, extension.Table),
		goldmark.WithParserOptions(parser.WithAutoHeadingID()),
		goldmark.WithRendererOptions(html.WithUnsafe()),
	)

	entries, err := fs.ReadDir(docsFS, "docs")
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		raw, err := fs.ReadFile(docsFS, "docs/"+e.Name())
		if err != nil {
			continue
		}
		page := parseDocPage(e.Name(), raw)
		docsBySlug[page.Slug] = page
		docsOrdered = append(docsOrdered, page)
	}
	sort.SliceStable(docsOrdered, func(i, j int) bool {
		if docsOrdered[i].Section != docsOrdered[j].Section {
			return docsOrdered[i].Section < docsOrdered[j].Section
		}
		if docsOrdered[i].Order != docsOrdered[j].Order {
			return docsOrdered[i].Order < docsOrdered[j].Order
		}
		return docsOrdered[i].Title < docsOrdered[j].Title
	})
}

// parseDocPage pulls off a simple YAML-ish front matter block and
// renders the remainder as HTML. Front matter is delimited by
// matching `---` lines; recognised keys: title, section, order, slug.
// Anything else is ignored.
func parseDocPage(filename string, raw []byte) *DocPage {
	page := &DocPage{
		Slug:    strings.TrimSuffix(filename, ".md"),
		Title:   strings.TrimSuffix(filename, ".md"),
		Section: "General",
		Order:   100,
	}
	body := raw
	if bytes.HasPrefix(raw, []byte("---\n")) {
		end := bytes.Index(raw[4:], []byte("\n---"))
		if end > 0 {
			fm := raw[4 : 4+end]
			body = raw[4+end+4:]
			for _, line := range strings.Split(string(fm), "\n") {
				k, v, ok := splitFrontMatter(line)
				if !ok {
					continue
				}
				switch k {
				case "title":
					page.Title = v
				case "section":
					page.Section = v
				case "order":
					if n, err := strconv.Atoi(v); err == nil {
						page.Order = n
					}
				case "slug":
					page.Slug = v
				}
			}
			body = bytes.TrimLeft(body, "\n")
		}
	}
	var html bytes.Buffer
	if err := docsRenderer.Convert(body, &html); err == nil {
		page.HTML = html.String()
	}
	page.TOC = extractTOC(string(body))
	return page
}

func splitFrontMatter(line string) (key, val string, ok bool) {
	idx := strings.Index(line, ":")
	if idx < 0 {
		return "", "", false
	}
	return strings.TrimSpace(line[:idx]),
		strings.Trim(strings.TrimSpace(line[idx+1:]), `"'`),
		true
}

// extractTOC walks the markdown source and lifts level-2 and
// level-3 headings into a flat ToC list. Matches goldmark's
// auto-generated heading IDs (kebab-case of the text).
var headingRe = regexp.MustCompile(`(?m)^(#{2,3})\s+(.+)$`)

func extractTOC(md string) []TOC {
	var out []TOC
	for _, m := range headingRe.FindAllStringSubmatch(md, -1) {
		level := len(m[1])
		text := strings.TrimSpace(m[2])
		out = append(out, TOC{
			ID:    headingID(text),
			Text:  text,
			Level: level,
		})
	}
	return out
}

// headingID mirrors goldmark's auto-id algorithm closely enough for
// our in-page anchor links to work. Lowercase, strip non-alphanumeric
// runs to a single dash.
var headingIDRe = regexp.MustCompile(`[^a-z0-9]+`)

func headingID(s string) string {
	s = strings.ToLower(s)
	s = headingIDRe.ReplaceAllString(s, "-")
	return strings.Trim(s, "-")
}

// ── HTTP handlers ──────────────────────────────────────────────────

func handleDocsTree(w http.ResponseWriter, r *http.Request) {
	docsOnce.Do(loadDocs)
	type entry struct {
		Slug    string `json:"slug"`
		Title   string `json:"title"`
		Section string `json:"section"`
		Order   int    `json:"order"`
	}
	out := make([]entry, 0, len(docsOrdered))
	for _, p := range docsOrdered {
		out = append(out, entry{p.Slug, p.Title, p.Section, p.Order})
	}
	writeJSON(w, http.StatusOK, out)
}

func handleDocsMedia(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/api/docs/media/")
	if name == "" || strings.Contains(name, "..") || strings.Contains(name, "/") {
		writeError(w, http.StatusBadRequest, "invalid media name")
		return
	}
	data, err := fs.ReadFile(docsFS, "docs/media/"+name)
	if err != nil {
		writeError(w, http.StatusNotFound, "no media at "+name)
		return
	}
	ct := "application/octet-stream"
	switch {
	case strings.HasSuffix(name, ".webp"):
		ct = "image/webp"
	case strings.HasSuffix(name, ".png"):
		ct = "image/png"
	case strings.HasSuffix(name, ".jpg"), strings.HasSuffix(name, ".jpeg"):
		ct = "image/jpeg"
	case strings.HasSuffix(name, ".gif"):
		ct = "image/gif"
	case strings.HasSuffix(name, ".svg"):
		ct = "image/svg+xml"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "public, max-age=3600")
	_, _ = w.Write(data)
}

func handleDocsPage(w http.ResponseWriter, r *http.Request) {
	docsOnce.Do(loadDocs)
	slug := strings.TrimPrefix(r.URL.Path, "/api/docs/page/")
	if slug == "" {
		writeError(w, http.StatusBadRequest, "slug required")
		return
	}
	page, ok := docsBySlug[slug]
	if !ok {
		writeError(w, http.StatusNotFound, "no doc at "+slug)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

// Keep the encoder import busy — used when page HTML is empty and
// we fall back to returning front-matter only.
var _ = json.Marshal
