// Package manual is bdtools' manual as web pages: the documents in docs/
// (grading.md, cookbook.md) made HTML by Build, with a stylesheet and the
// pictures beside them. The pages are generated here (go generate) and
// committed, so that a checkout or GitHub serves them as they are, a
// release archive carries them, and the binary embeds them (Files):
// --manual serves or writes them, and --3d-grade's page links them.
package manual

import (
	"bytes"
	"embed"
	"fmt"
	"html"
	"io/fs"
	"net/http"
	"path"
	"regexp"
	"sort"
	"strings"
)

//go:generate go run ./gen

// Files is the manual: index.html, a page per Pages entry, style.css and
// img/.
//
//go:embed *.html style.css img
var Files embed.FS

// Pages are the manual's pages in its order, by the markdown in docs/
// each is made from (its page is the same name with .html).
var Pages = []string{"grading.md", "cookbook.md"}

// Handler serves the manual: index.html at /, the pages and pictures by
// name.
func Handler() http.Handler { return http.FileServer(http.FS(Files)) }

// Build makes the manual's generated files from docs (the docs/
// directory): the pages, by name, and index.html.
func Build(docs fs.FS) (map[string][]byte, error) {
	out := map[string][]byte{}
	type entry struct{ name, title, lead string }
	var entries []entry
	for _, src := range Pages {
		md, err := fs.ReadFile(docs, src)
		if err != nil {
			return nil, err
		}
		name := strings.TrimSuffix(src, ".md") + ".html"
		d := convert(string(md), linker(src))
		if d.title == "" {
			return nil, fmt.Errorf("%s: no title (a # heading)", src)
		}
		out[name] = page(d.title, name, d.toc, d.body)
		entries = append(entries, entry{name, d.title, d.lead})
	}
	var b strings.Builder
	b.WriteString("<p class=\"lead\">What bdtools does and how to drive it, as pages: the same text as the documents in the repository, with the pictures.</p>\n<ul class=\"pages\">\n")
	for _, e := range entries {
		fmt.Fprintf(&b, "<li><a href=\"%s\">%s</a><p>%s</p></li>\n", e.name, html.EscapeString(e.title), e.lead)
	}
	b.WriteString("</ul>\n<p>The command's own reference, every flag and the choices behind the defaults, is <a href=\"" + repo + "/blob/main/cmd/bdtools/README.md\">bdtools' README</a>; the design and the measurements of the 2D-to-3D conversion are in <a href=\"" + repo + "/blob/main/docs/2d-to-3d.md\">2d-to-3d.md</a>.</p>\n")
	b.WriteString("<p>This manual is in the binary too: <code>bdtools --manual :8080</code> serves it at that address, <code>bdtools --manual DIR</code> writes it into a directory, and the grading page links it.</p>\n")
	out["index.html"] = page("bdtools manual", "index.html", nil, b.String())
	return out, nil
}

const repo = "https://github.com/brunoga/bdtools"

// linker makes the function that turns a link of the page made from src
// (a path under docs/) into the manual's: a link to a page's markdown
// becomes the page, one into manual/ the file there, another relative
// one the repository's copy on GitHub.
func linker(src string) func(string) string {
	return func(u string) string {
		if u == "" || u[0] == '#' || strings.Contains(u, "://") || strings.HasPrefix(u, "mailto:") {
			return u
		}
		frag := ""
		if i := strings.IndexByte(u, '#'); i >= 0 {
			u, frag = u[:i], u[i:]
		}
		p := path.Join("docs", path.Dir(src), u)
		for _, s := range Pages {
			if p == path.Join("docs", s) {
				return strings.TrimSuffix(s, ".md") + ".html" + frag
			}
		}
		if rest, ok := strings.CutPrefix(p, "docs/manual/"); ok {
			return rest + frag
		}
		return repo + "/blob/main/" + p + frag
	}
}

// page is a page of the manual: its title, the navigation, the contents
// (the second-level headings) and the body.
func page(title, name string, toc []heading, body string) []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, "<!doctype html>\n<html lang=\"en\">\n<head>\n<meta charset=\"utf-8\">\n<meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">\n<title>%s</title>\n<link rel=\"stylesheet\" href=\"style.css\">\n</head>\n<body>\n", html.EscapeString(title))
	b.WriteString("<header><a class=\"brand\" href=\"index.html\">bdtools manual</a><nav>")
	for _, s := range Pages {
		n := strings.TrimSuffix(s, ".md") + ".html"
		cur := ""
		if n == name {
			cur = " class=\"cur\""
		}
		fmt.Fprintf(&b, "<a href=\"%s\"%s>%s</a>", n, cur, strings.TrimSuffix(s, ".md"))
	}
	fmt.Fprintf(&b, "<a href=\"%s\">GitHub</a></nav></header>\n", repo)
	if len(toc) > 0 {
		b.WriteString("<div class=\"wrap\">\n")
		b.WriteString("<aside><details open><summary>Contents</summary><ol>")
		for _, h := range toc {
			fmt.Fprintf(&b, "<li><a href=\"#%s\">%s</a></li>", h.id, h.text)
		}
		b.WriteString("</ol></details></aside>\n")
	} else {
		b.WriteString("<div class=\"wrap single\">\n")
	}
	fmt.Fprintf(&b, "<main>\n<h1>%s</h1>\n%s</main>\n</div>\n</body>\n</html>\n", html.EscapeString(title), body)
	return b.Bytes()
}

type heading struct{ id, text string }

type doc struct {
	title, lead, body string
	toc               []heading
}

// convert turns the markdown this manual is written in (headings, paragraphs,
// bullet and numbered lists with continuation lines, tables, fenced code,
// code spans, bold, links and pictures) into HTML; link turns each link's
// address into the manual's.
func convert(md string, link func(string) string) doc {
	var d doc
	var b strings.Builder
	lines := strings.Split(strings.ReplaceAll(md, "\r\n", "\n"), "\n")
	var para []string
	list := "" // "ul" or "ol" while in a list
	var item []string
	flushItem := func() {
		if list != "" && item != nil {
			b.WriteString("<li>" + inline(strings.Join(item, " "), link) + "</li>\n")
			item = nil
		}
	}
	flushList := func() {
		flushItem()
		if list != "" {
			b.WriteString("</" + list + ">\n")
			list = ""
		}
	}
	flushPara := func() {
		if len(para) == 0 {
			return
		}
		text := strings.Join(para, " ")
		para = nil
		if m := imageRE.FindStringSubmatch(text); m != nil && strings.TrimSpace(text) == m[0] {
			fmt.Fprintf(&b, "<figure><img src=\"%s\" alt=\"%s\" loading=\"lazy\"><figcaption>%s</figcaption></figure>\n",
				html.EscapeString(link(m[2])), html.EscapeString(m[1]), inline(m[1], link))
			return
		}
		h := inline(text, link)
		if d.lead == "" && d.title != "" {
			d.lead = h
		}
		b.WriteString("<p>" + h + "</p>\n")
	}
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		switch {
		case strings.HasPrefix(line, "```"):
			flushPara()
			flushList()
			var code []string
			for i++; i < len(lines) && !strings.HasPrefix(lines[i], "```"); i++ {
				code = append(code, lines[i])
			}
			b.WriteString("<pre><code>" + html.EscapeString(strings.Join(code, "\n")) + "</code></pre>\n")
		case strings.HasPrefix(line, "# "):
			flushPara()
			flushList()
			if d.title == "" {
				d.title = strings.TrimSpace(line[2:])
			} else {
				b.WriteString("<h1>" + inline(line[2:], link) + "</h1>\n")
			}
		case strings.HasPrefix(line, "## "), strings.HasPrefix(line, "### "):
			flushPara()
			flushList()
			level, text := 2, strings.TrimSpace(line[3:])
			if line[2] == '#' {
				level, text = 3, strings.TrimSpace(line[4:])
			}
			id := slug(text)
			if level == 2 {
				d.toc = append(d.toc, heading{id, inline(text, link)})
			}
			fmt.Fprintf(&b, "<h%d id=\"%s\">%s</h%d>\n", level, id, inline(text, link), level)
		case strings.HasPrefix(line, "|"):
			flushPara()
			flushList()
			var rows []string
			for ; i < len(lines) && strings.HasPrefix(lines[i], "|"); i++ {
				rows = append(rows, lines[i])
			}
			i--
			b.WriteString("<table>\n")
			for r, row := range rows {
				if r == 1 && strings.Trim(row, "|-: ") == "" {
					continue
				}
				tag := "td"
				if r == 0 {
					tag = "th"
				}
				cells := strings.Split(strings.Trim(row, "|"), "|")
				b.WriteString("<tr>")
				for _, c := range cells {
					b.WriteString("<" + tag + ">" + inline(strings.TrimSpace(c), link) + "</" + tag + ">")
				}
				b.WriteString("</tr>\n")
			}
			b.WriteString("</table>\n")
		case strings.HasPrefix(line, "- "), numberedRE.MatchString(line):
			flushPara()
			kind, text := "ul", strings.TrimSpace(line[2:])
			if line[0] != '-' {
				kind, text = "ol", strings.TrimSpace(numberedRE.ReplaceAllString(line, ""))
			}
			if list != kind {
				flushList()
				list = kind
				b.WriteString("<" + kind + ">\n")
			}
			flushItem()
			item = []string{text}
		case strings.TrimSpace(line) == "":
			flushPara()
			flushList()
		case list != "" && strings.HasPrefix(line, " "):
			item = append(item, strings.TrimSpace(line))
		default:
			flushList()
			para = append(para, strings.TrimSpace(line))
		}
	}
	flushPara()
	flushList()
	d.body = b.String()
	return d
}

var (
	numberedRE = regexp.MustCompile(`^\d+\. `)
	codeRE     = regexp.MustCompile("`[^`]+`")
	boldRE     = regexp.MustCompile(`\*\*(.+?)\*\*`)
	imageRE    = regexp.MustCompile(`!\[([^\]]*)\]\(([^)\s]+)\)`)
	linkRE     = regexp.MustCompile(`\[([^\]]+)\]\(([^)\s]+)\)`)
)

// inline is a run of text with its code spans, bold, pictures and links as
// HTML, the rest escaped.
func inline(s string, link func(string) string) string {
	var codes []string
	s = codeRE.ReplaceAllStringFunc(s, func(c string) string {
		codes = append(codes, "<code>"+html.EscapeString(c[1:len(c)-1])+"</code>")
		return fmt.Sprintf("\x00%d\x00", len(codes)-1)
	})
	s = html.EscapeString(s)
	s = boldRE.ReplaceAllString(s, "<strong>$1</strong>")
	s = imageRE.ReplaceAllStringFunc(s, func(m string) string {
		p := imageRE.FindStringSubmatch(m)
		return fmt.Sprintf("<img src=\"%s\" alt=\"%s\">", html.EscapeString(link(html.UnescapeString(p[2]))), p[1])
	})
	s = linkRE.ReplaceAllStringFunc(s, func(m string) string {
		p := linkRE.FindStringSubmatch(m)
		return fmt.Sprintf("<a href=\"%s\">%s</a>", html.EscapeString(link(html.UnescapeString(p[2]))), p[1])
	})
	for i := len(codes) - 1; i >= 0; i-- {
		s = strings.ReplaceAll(s, fmt.Sprintf("\x00%d\x00", i), codes[i])
	}
	return s
}

// slug is a heading's anchor as GitHub makes it: lower case, letters,
// digits and hyphens, spaces hyphens.
func slug(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			b.WriteRune(r)
		case r == ' ':
			b.WriteByte('-')
		}
	}
	return b.String()
}

// Names is Files' names, sorted, for a listing.
func Names() []string {
	var names []string
	_ = fs.WalkDir(Files, ".", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			names = append(names, p)
		}
		return nil
	})
	sort.Strings(names)
	return names
}
