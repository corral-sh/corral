// Command site renders the project site (GitHub Pages) from files
// that already exist in the repository: README.md, docs/*.md and
// changelog/*.md. Nothing is authored twice — the site is a view of them.
//
//	go run ./tools/site -out public
package main

import (
	"bytes"
	"flag"
	"fmt"
	"html/template"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer/html"
)

type page struct {
	Title, File, Nav string
	Body             template.HTML
}

type release struct {
	Version, Date, File string
}

func main() {
	out := flag.String("out", "public", "output directory")
	tagline := flag.String("tagline", "", "hero tagline (default: README's first bold line)")
	version := flag.String("version", "", "version shown in the footer")
	flag.Parse()

	md := goldmark.New(
		goldmark.WithExtensions(extension.GFM),
		goldmark.WithParserOptions(parser.WithAutoHeadingID()),
		goldmark.WithRendererOptions(html.WithUnsafe()), // our own markdown; allows the README's inline HTML
	)
	render := func(src []byte) template.HTML {
		var buf bytes.Buffer
		if err := md.Convert(relink(src), &buf); err != nil {
			fail(err)
		}
		return template.HTML(buf.String()) //nolint:gosec // rendered from repository markdown
	}
	must(os.MkdirAll(filepath.Join(*out, "releases"), 0o750))

	readme := read("README.md")
	if *tagline == "" {
		*tagline = firstBold(readme)
	}
	// Releases: every changelog/<version>.md, newest first (semver order).
	var rels []release
	files, _ := filepath.Glob("changelog/*.md")
	for _, f := range files {
		base := strings.TrimSuffix(filepath.Base(f), ".md")
		if base == "unreleased" {
			continue
		}
		rels = append(rels, release{Version: base, Date: dateOf(read(f)), File: "releases/" + base + ".html"})
	}
	sort.Slice(rels, func(i, j int) bool { return semverLess(rels[j].Version, rels[i].Version) })

	nav := []page{
		{Title: "Overview", File: "index.html"},
		{Title: "Features", File: "features.html"},
		{Title: "Security model", File: "security.html"},
		{Title: "Architecture", File: "architecture.html"},
		{Title: "Feasibility", File: "feasibility.html"},
		{Title: "Contributing", File: "contributing.html"},
		{Title: "Releases", File: "releases/index.html"},
	}
	data := map[string]any{"Tagline": *tagline, "Nav": nav, "Releases": rels, "Version": *version}
	if len(rels) > 0 {
		data["Latest"] = rels[0]
	}
	write := func(file, title, depth string, body template.HTML, extra map[string]any) {
		d := map[string]any{"Title": title, "Body": body, "Depth": depth, "Active": file}
		for k, v := range data {
			d[k] = v
		}
		for k, v := range extra {
			d[k] = v
		}
		var buf bytes.Buffer
		must(tpl.Execute(&buf, d))
		must(os.WriteFile(filepath.Join(*out, file), buf.Bytes(), 0o644)) //nolint:gosec // static site
	}
	write("index.html", "Corral", "", render(stripH1(readme)), map[string]any{"Hero": true})
	write("features.html", "Feature catalog", "", render(read("docs/FEATURES.md")), nil)
	write("security.html", "Security model", "", render(read("docs/SECURITY.md")), nil)
	write("architecture.html", "Architecture", "", render(read("docs/ARCHITECTURE.md")), nil)
	write("feasibility.html", "Feasibility", "", render(read("docs/FEASIBILITY.md")), nil)
	write("contributing.html", "Contributing", "", render(read("CONTRIBUTING.md")), nil)
	var idx strings.Builder
	idx.WriteString("<h1>Releases</h1><p>One page per release — the same notes the GitHub release carries.</p><ul class=\"releases\">")
	for _, r := range rels {
		fmt.Fprintf(&idx, `<li><a href="%s">%s</a> <span class="date">%s</span></li>`, filepath.Base(r.File), r.Version, r.Date)
	}
	idx.WriteString("</ul>")
	if b := read("changelog/unreleased.md"); strings.Count(string(b), "\n- ") > 0 {
		idx.WriteString("<h2>Unreleased</h2><p class=\"muted\">On <code>main</code>, not yet tagged.</p>" + string(render(stripH1(b))))
	}
	write("releases/index.html", "Releases", "../", template.HTML(idx.String()), nil) //nolint:gosec // built above from our own files
	for _, r := range rels {
		write(r.File, "Corral "+r.Version, "../", render(read("changelog/"+r.Version+".md")), nil)
	}
	must(os.WriteFile(filepath.Join(*out, "style.css"), []byte(css), 0o644)) //nolint:gosec // static site
	// llms.txt: the convention AI assistants look for first; links rewritten to site pages.
	must(os.WriteFile(filepath.Join(*out, "llms.txt"), relink(read("llms.txt")), 0o644)) //nolint:gosec // static site
	fmt.Printf("site: %d pages, %d releases → %s\n", len(nav)+len(rels), len(rels), *out)
}

var (
	mdLink   = regexp.MustCompile(`\]\(((?:docs/|changelog/)?[A-Za-z0-9_.-]+)\.md(#[^)]*)?\)`)
	boldLine = regexp.MustCompile(`(?m)^\*\*(.+?)\*\*\s*$`)
	dateLine = regexp.MustCompile(`(?m)^# [^—]+—\s*(\S+)`)
	h1Line   = regexp.MustCompile(`(?m)^# .*\n`)
)

// relink rewrites repository-relative markdown links into site links; files
// that have no page on the site point at the repository.
func relink(src []byte) []byte {
	src = bytes.ReplaceAll(src, []byte("](changelog/)"), []byte("](releases/index.html)"))
	src = bytes.ReplaceAll(src, []byte("](CLAUDE.md)"), []byte("](https://github.com/corral-sh/corral/blob/main/CLAUDE.md)"))
	return mdLink.ReplaceAllFunc(src, func(m []byte) []byte {
		sub := mdLink.FindSubmatch(m)
		name, frag := string(sub[1]), string(sub[2])
		var target string
		switch {
		case name == "README":
			target = "index.html"
		case name == "CHANGELOG":
			target = "releases/index.html"
		case strings.HasPrefix(name, "docs/"):
			target = strings.ToLower(strings.TrimPrefix(name, "docs/")) + ".html"
		case strings.HasPrefix(name, "changelog/"):
			target = "releases/" + strings.TrimPrefix(name, "changelog/") + ".html"
		default:
			return m
		}
		return []byte("](" + target + frag + ")")
	})
}

func firstBold(b []byte) string {
	if m := boldLine.FindSubmatch(b); m != nil {
		return string(m[1])
	}
	return ""
}
func dateOf(b []byte) string {
	if m := dateLine.FindSubmatch(b); m != nil {
		return string(m[1])
	}
	return ""
}
func stripH1(b []byte) []byte { return h1Line.ReplaceAll(b, nil) }

func semverLess(a, b string) bool {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < 3 && i < len(pa) && i < len(pb); i++ {
		var x, y int
		_, _ = fmt.Sscanf(pa[i], "%d", &x)
		_, _ = fmt.Sscanf(pb[i], "%d", &y)
		if x != y {
			return x < y
		}
	}
	return a < b
}

func read(p string) []byte {
	b, err := os.ReadFile(p)
	if err != nil {
		fail(err)
	}
	return b
}
func must(err error) {
	if err != nil {
		fail(err)
	}
}
func fail(err error) { fmt.Fprintln(os.Stderr, "site:", err); os.Exit(1) }

var tpl = template.Must(template.New("page").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>{{.Title}} · Corral</title>
<link rel="stylesheet" href="{{.Depth}}style.css">
<script src="https://cdn.jsdelivr.net/npm/mermaid@11.17.2/dist/mermaid.min.js" integrity="sha384-EOXBFmc3gx5mb+vn0vPvvGqACToJD24hhacX5Yx+8NUUQrHIle/Qi5Bg9o3zKwW2" crossorigin="anonymous" defer></script>
<script>addEventListener('DOMContentLoaded',()=>{if(!window.mermaid)return;const dark=matchMedia('(prefers-color-scheme: dark)').matches;mermaid.initialize({startOnLoad:false,theme:dark?'dark':'neutral'});const nodes=[...document.querySelectorAll('pre > code.language-mermaid')].map(c=>{const p=document.createElement('pre');p.className='mermaid';p.textContent=c.textContent;c.parentElement.replaceWith(p);return p});if(nodes.length)mermaid.run({nodes})})</script></head>
<body><header><a class="brand" href="{{.Depth}}index.html">🐎 Corral</a><nav>
{{range .Nav}}<a href="{{$.Depth}}{{.File}}"{{if eq .File $.Active}} class="on"{{end}}>{{.Title}}</a>{{end}}</nav></header>
{{if .Hero}}<section class="hero"><h1>{{.Tagline}}</h1>
<p>Corral runs AI coding agents inside a per-project Linux VM on your Mac — full speed inside, nothing of yours outside.</p>
<p class="install"><code>brew install corral-sh/tap/corral</code></p>
{{with .Latest}}<p class="latest">Latest release: <a href="releases/{{.Version}}.html">{{.Version}}</a> · {{.Date}}</p>{{end}}</section>{{end}}
<main>{{.Body}}</main>
<footer><a href="https://github.com/corral-sh/corral">Corral on GitHub</a> · Apache-2.0. Rendered from the repository's README, docs and changelog{{if .Version}} · corral {{.Version}}{{end}}.</footer>
</body></html>
`))

const css = `
:root{--bg:#0f1115;--fg:#e6e6e6;--muted:#9aa0a6;--acc:#f5b400;--panel:#171a21;--line:#262a33}
@media (prefers-color-scheme: light){:root{--bg:#fff;--fg:#1c1e21;--muted:#5f6368;--acc:#b8860b;--panel:#f6f7f9;--line:#e3e5e8}}
*{box-sizing:border-box}body{margin:0;font:16px/1.55 -apple-system,BlinkMacSystemFont,"Segoe UI",Helvetica,Arial,sans-serif;background:var(--bg);color:var(--fg)}
header{display:flex;gap:1.5rem;align-items:center;padding:.9rem 1.5rem;border-bottom:1px solid var(--line);position:sticky;top:0;background:var(--bg)}
.brand{font-weight:700;text-decoration:none;color:var(--fg)}nav{display:flex;gap:1rem;flex-wrap:wrap}nav a{color:var(--muted);text-decoration:none}nav a.on,nav a:hover{color:var(--acc)}
.hero{padding:3rem 1.5rem 2rem;max-width:900px;margin:0 auto}.hero h1{font-size:2.2rem;line-height:1.15;margin:0 0 .5rem}.hero p{color:var(--muted)}
.install code{display:block;padding:.8rem 1rem;background:var(--panel);border:1px solid var(--line);border-radius:8px;overflow-x:auto;white-space:pre;color:var(--fg)}
.latest a{color:var(--acc)}
main{max-width:900px;margin:0 auto;padding:1rem 1.5rem 3rem}main h1{font-size:1.9rem}main h2{margin-top:2.2rem;border-bottom:1px solid var(--line);padding-bottom:.3rem}
main a{color:var(--acc)}main code{background:var(--panel);padding:.1rem .35rem;border-radius:4px;font-size:.92em}main pre{background:var(--panel);border:1px solid var(--line);border-radius:8px;padding:1rem;overflow-x:auto}main pre code{background:none;padding:0}
main pre.mermaid{background:none;border:none;text-align:center}main pre.mermaid svg{max-width:100%;height:auto}
main table{border-collapse:collapse;width:100%;display:block;overflow-x:auto;font-size:.95em}main th,main td{border:1px solid var(--line);padding:.45rem .6rem;text-align:left;vertical-align:top}main th{background:var(--panel)}
main blockquote{border-left:3px solid var(--acc);margin:1rem 0;padding:.2rem 1rem;color:var(--muted)}
ul.releases{list-style:none;padding:0}ul.releases li{padding:.5rem 0;border-bottom:1px solid var(--line)}ul.releases a{font-weight:600;font-size:1.1em}.date,.muted{color:var(--muted)}
footer{max-width:900px;margin:0 auto;padding:1.5rem;color:var(--muted);border-top:1px solid var(--line);font-size:.9em}
`
