package dashboard_test

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/petersonsalme/blockchain-with-go/internal/dashboard"
)

// classValues are the values a template action inside a class attribute can take, keyed by the action text.
func classValues() map[string][]string {
	var states []string

	for s := dashboard.StateNone; s <= dashboard.StateFork; s++ {
		states = append(states, s.String())
	}

	series := make([]string, 6)
	for i := range series {
		series[i] = fmt.Sprintf("series-%d", i)
	}

	return map[string][]string{
		".Level":                   {"ok", "warn", "bad"},
		"$level":                   {"ok", "warn", "bad"},
		".Kind":                    {"tip", "behind", "fork", "missing", "stale"},
		".Class":                   series,
		".Assessment.State.String": states,
	}
}

// hookClasses are classes templates write for tests and docs to read; app.css need not define them.
func hookClasses() map[string]bool {
	hooks := map[string]bool{"nav-node--ok": true}

	for s := dashboard.StateNone; s <= dashboard.StateFork; s++ {
		hooks["banner-"+s.String()] = true
	}

	return hooks
}

var (
	cssComment  = regexp.MustCompile(`(?s)/\*.*?\*/`)
	cssString   = regexp.MustCompile(`"[^"]*"|'[^']*'`)
	cssClass    = regexp.MustCompile(`\.(-?[A-Za-z_][A-Za-z0-9_-]*)`)
	classAttr   = regexp.MustCompile(`\bclass="((?:\{\{.*?\}\}|[^"])*)"`)
	controlAct  = regexp.MustCompile(`\{\{-?\s*(?:if|else|end|range|with)\b.*?\}\}`)
	classToken  = regexp.MustCompile(`(?:\{\{.*?\}\}|\S)+`)
	actionInTok = regexp.MustCompile(`\{\{(.*?)\}\}`)
)

func definedClasses(css string) map[string]bool {
	css = cssComment.ReplaceAllString(css, "")
	css = cssString.ReplaceAllString(css, "")
	out := map[string]bool{}

	for _, m := range cssClass.FindAllStringSubmatch(css, -1) {
		out[m[1]] = true
	}

	return out
}

func undefinedClasses(templates map[string]string, css string, values map[string][]string, hooks map[string]bool) (missing, unused []string) {
	defined := definedClasses(css)
	used := map[string]bool{}
	miss := map[string]bool{}

	names := make([]string, 0, len(templates))
	for n := range templates {
		names = append(names, n)
	}

	sort.Strings(names)

	for _, file := range names {
		for _, attr := range classAttr.FindAllStringSubmatch(templates[file], -1) {
			val := controlAct.ReplaceAllString(attr[1], " ")

			for _, tok := range classToken.FindAllString(val, -1) {
				acts := actionInTok.FindAllStringSubmatchIndex(tok, -1)

				var classes []string

				switch len(acts) {
				case 0:
					classes = []string{tok}
				case 1:
					key := strings.Trim(tok[acts[0][2]:acts[0][3]], "- ")
					vs, ok := values[key]

					if !ok {
						miss[fmt.Sprintf("%s: class action {{%s}} has no declared values", file, key)] = true

						continue
					}

					for _, v := range vs {
						classes = append(classes, tok[:acts[0][0]]+v+tok[acts[0][1]:])
					}
				default:
					miss[fmt.Sprintf("%s: class token %q has more than one action", file, tok)] = true

					continue
				}

				for _, c := range classes {
					used[c] = true

					if !defined[c] && !hooks[c] {
						miss[fmt.Sprintf("%s: class %q is not defined in static/app.css", file, c)] = true
					}
				}
			}
		}
	}

	for m := range miss {
		missing = append(missing, m)
	}

	sort.Strings(missing)

	for c := range defined {
		if !used[c] {
			unused = append(unused, c)
		}
	}

	sort.Strings(unused)

	return missing, unused
}

func TestUndefinedClasses(t *testing.T) {
	t.Parallel()

	const undef = `t.html: class %q is not defined in static/app.css`

	tests := []struct {
		name, tmpl, css string
		missing, unused []string
	}{
		{"static class missing", `<a class="a b">`, `.a{}`, []string{fmt.Sprintf(undef, "b")}, nil},
		{"comment is not a definition", `<a class="a b">`, `/* .b */ .a{}`, []string{fmt.Sprintf(undef, "b")}, nil},
		{"string is not a definition", `<a class="a b">`, `.a::after{content:".b"}`, []string{fmt.Sprintf(undef, "b")}, nil},
		{"control action is dropped", `<p class="a{{if .X}} b{{end}}">`, `.a{}.b{}`, nil, nil},
		{"action expands every value", `<p class="d--{{.Level}}">`, `.d--ok{}.d--warn{}`, []string{fmt.Sprintf(undef, "d--bad")}, nil},
		{"unknown action", `<p class="{{.Kind}}">`, ``, []string{`t.html: class action {{.Kind}} has no declared values`}, nil},
		{"two actions in one token", `<p class="{{.Level}}{{.Level}}">`, ``, []string{`t.html: class token "{{.Level}}{{.Level}}" has more than one action`}, nil},
		{"hook is exempt", `<p class="hook">`, ``, nil, nil},
		{"unused is listed", `<p class="a">`, `.a{}.z{width:0.5em}`, nil, []string{"z"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			missing, unused := undefinedClasses(map[string]string{"t.html": tc.tmpl}, tc.css,
				map[string][]string{".Level": {"ok", "warn", "bad"}}, map[string]bool{"hook": true})

			if !slices.Equal(missing, tc.missing) {
				t.Errorf("missing = %q, want %q", missing, tc.missing)
			}

			if !slices.Equal(unused, tc.unused) {
				t.Errorf("unused = %q, want %q", unused, tc.unused)
			}
		})
	}
}

func TestTemplateClassesExistInCSS(t *testing.T) {
	t.Parallel()

	css, err := os.ReadFile("static/app.css")
	if err != nil {
		t.Fatal(err)
	}

	files, err := filepath.Glob("templates/*.html")
	if err != nil {
		t.Fatal(err)
	}

	if len(files) == 0 {
		t.Fatal("no templates found")
	}

	templates := map[string]string{}

	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}

		templates[filepath.Base(f)] = string(b)
	}

	if len(definedClasses(string(css))) == 0 {
		t.Fatal("no class defined in static/app.css")
	}

	missing, unused := undefinedClasses(templates, string(css), classValues(), hookClasses())

	for _, m := range missing {
		t.Error(m)
	}

	t.Logf("defined in static/app.css but unused by templates (%d): %s", len(unused), strings.Join(unused, " "))
}
