package dashboard_test

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// TestImportsOnlyStandardLibrary keeps the dashboard apart from node code: it talks to nodes over HTTP only.
func TestImportsOnlyStandardLibrary(t *testing.T) {
	t.Parallel()

	all, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}

	var files []string

	for _, name := range all {
		if !strings.HasSuffix(name, "_test.go") { // tests import the package under test
			files = append(files, name)
		}
	}

	if len(files) == 0 {
		t.Fatal("no source files")
	}

	fset := token.NewFileSet()

	for _, name := range files {
		f, err := parser.ParseFile(fset, name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}

		for _, imp := range f.Imports {
			path, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				t.Fatal(err)
			}

			if first, _, _ := strings.Cut(path, "/"); strings.Contains(first, ".") {
				t.Errorf("%s imports %s: only the standard library is allowed", name, path)
			}
		}
	}
}

// TestTemplatesAreCSPClean walks the embedded templates for what the CSP and htmx config forbid.
func TestTemplatesAreCSPClean(t *testing.T) {
	t.Parallel()

	forbidden := map[string]*regexp.Regexp{
		"inline script":   regexp.MustCompile(`(?i)<script\b[^>]*>\s*[^<\s]`),
		"script no src":   regexp.MustCompile(`(?i)<script\b(?:[^>]*\s)?[^>]*>`),
		"style attribute": regexp.MustCompile(`(?i)\sstyle\s*=`),
		"style element":   regexp.MustCompile(`(?i)<style\b`),
		"hx-on":           regexp.MustCompile(`(?i)\shx-on`),
		"js: value":       regexp.MustCompile(`(?i)\bjs:`),
		"javascript:":     regexp.MustCompile(`(?i)javascript:`),
		"event handler":   regexp.MustCompile(`(?i)\son[a-z]+\s*=`),
	}
	srcScript := regexp.MustCompile(`(?i)<script\b[^>]*\ssrc=`)

	count := 0

	err := fs.WalkDir(os.DirFS("templates"), ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}

		count++

		raw, err := os.ReadFile(filepath.Join("templates", path))
		if err != nil {
			return err
		}

		text := string(raw)

		for name, re := range forbidden {
			if name == "script no src" {
				for _, tag := range regexp.MustCompile(`(?i)<script\b[^>]*>`).FindAllString(text, -1) {
					if !srcScript.MatchString(tag) {
						t.Errorf("%s: %s: %s", path, name, tag)
					}
				}

				continue
			}

			if m := re.FindString(text); m != "" {
				t.Errorf("%s: %s: %q", path, name, m)
			}
		}

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	if count == 0 {
		t.Fatal("no templates found")
	}
}
