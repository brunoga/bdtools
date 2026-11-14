package manual

import (
	"bytes"
	"os"
	"regexp"
	"strings"
	"testing"
)

// The committed pages are what the documents make: go generate was run.
func TestPagesAreCurrent(t *testing.T) {
	out, err := Build(os.DirFS(".."))
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range out {
		got, err := Files.ReadFile(name)
		if err != nil || !bytes.Equal(got, want) {
			t.Errorf("%s is not what docs/ makes: run go generate ./docs/manual", name)
		}
	}
}

// Every picture and local link of the pages is in the manual.
func TestLinksResolve(t *testing.T) {
	re := regexp.MustCompile(`(?:src|href)="([^"]+)"`)
	for _, name := range Names() {
		if !strings.HasSuffix(name, ".html") {
			continue
		}
		b, _ := Files.ReadFile(name)
		for _, m := range re.FindAllStringSubmatch(string(b), -1) {
			u := m[1]
			if strings.Contains(u, "://") || strings.HasPrefix(u, "#") {
				continue
			}
			if i := strings.IndexByte(u, '#'); i >= 0 {
				u = u[:i]
			}
			if _, err := Files.ReadFile(u); err != nil {
				t.Errorf("%s links %s, which the manual does not have", name, m[1])
			}
		}
	}
}

func TestConvert(t *testing.T) {
	md := "# Title\n\nFirst *para* with `code` and **bold** and [a link](other.md#x) and [out](../cmd/README.md).\n\n## Sec one\n\n- item one\n  goes on\n- item two\n\n1. first\n2. second\n\n| a | b |\n|---|---|\n| `x` | y |\n\n```sh\nbdtools --check <y>\n```\n\n![A picture](manual/img/p.png)\n"
	d := convert(md, linker("grading.md"))
	if d.title != "Title" {
		t.Errorf("title %q", d.title)
	}
	for _, want := range []string{
		"<code>code</code>", "<strong>bold</strong>", `<a href="https://github.com/brunoga/bdtools/blob/main/docs/other.md#x">a link</a>`,
		`<a href="https://github.com/brunoga/bdtools/blob/main/cmd/README.md">out</a>`,
		`<h2 id="sec-one">Sec one</h2>`, "<ul>\n<li>item one goes on</li>\n<li>item two</li>\n</ul>", "<ol>\n<li>first</li>\n<li>second</li>\n</ol>",
		"<tr><th>a</th><th>b</th></tr>", "<tr><td><code>x</code></td><td>y</td></tr>", "<pre><code>bdtools --check &lt;y&gt;</code></pre>",
		`<figure><img src="img/p.png" alt="A picture" loading="lazy"><figcaption>A picture</figcaption></figure>`,
	} {
		if !strings.Contains(d.body, want) {
			t.Errorf("missing %s in\n%s", want, d.body)
		}
	}
	if len(d.toc) != 1 || d.toc[0].id != "sec-one" {
		t.Errorf("toc %v", d.toc)
	}
	if got := linker("grading.md")("cookbook.md#tracks"); got != "cookbook.html#tracks" {
		t.Errorf("page link %q", got)
	}
}
