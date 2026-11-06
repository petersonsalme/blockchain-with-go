package adr_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var (
	prefixRe = regexp.MustCompile(`^(\d{4})-.*\.md$`)
	rowRe    = regexp.MustCompile(`^\| \[(\d{4})\]\(docs/adr/([^)]+)\) \|`)
)

// adrFiles returns the ADR file names in docs/adr, in ascending order.
func adrFiles(t *testing.T) []string {
	t.Helper()

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}

	var names []string

	for _, e := range entries {
		if !e.IsDir() && prefixRe.MatchString(e.Name()) {
			names = append(names, e.Name())
		}
	}

	return names
}

func TestUniquePrefixes(t *testing.T) {
	seen := map[string]string{}

	for _, name := range adrFiles(t) {
		p := name[:4]
		if other, ok := seen[p]; ok {
			t.Errorf("duplicate ADR number %s: %s and %s", p, other, name)
		}

		seen[p] = name
	}
}

func TestHeadingMatchesPrefix(t *testing.T) {
	for _, name := range adrFiles(t) {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}

		first, _, _ := strings.Cut(string(data), "\n")
		if want := "# ADR-" + name[:4] + ":"; !strings.HasPrefix(first, want) {
			t.Errorf("%s: first line is %q, want it to start with %q", name, first, want)
		}
	}
}

func TestReadmeIndex(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "README.md"))
	if err != nil {
		t.Fatal(err)
	}

	_, rest, ok := strings.Cut(string(data), "\n## Architecture Decision Records\n")
	if !ok {
		t.Fatal("README.md has no \"## Architecture Decision Records\" section")
	}

	if i := strings.Index(rest, "\n## "); i >= 0 {
		rest = rest[:i]
	}

	rows := map[string]int{}

	for line := range strings.SplitSeq(rest, "\n") {
		m := rowRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}

		rows[m[2]]++

		_, statErr := os.Stat(m[2])
		if statErr != nil {
			t.Errorf("README row links %s, which does not exist", m[2])
		}

		if !strings.HasPrefix(m[2], m[1]+"-") {
			t.Errorf("README row [%s] links %s, whose number differs", m[1], m[2])
		}
	}

	for name, n := range rows {
		if n > 1 {
			t.Errorf("README has %d rows for %s, want 1", n, name)
		}
	}

	for _, name := range adrFiles(t) {
		if rows[name] == 0 {
			t.Errorf("README has no row for %s", name)
		}
	}
}
