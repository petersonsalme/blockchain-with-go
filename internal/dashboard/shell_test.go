package dashboard_test

import (
	"net/http"
	"regexp"
	"slices"
	"strings"
	"testing"
)

var (
	reAriaCurrent = regexp.MustCompile(`aria-current="page">([^<]*)<`)
	reCrumb       = regexp.MustCompile(`<li>(?:<a href="[^"]*">|<span aria-current="page">)([^<]*)<`)
)

func extractBetween(s, startMarker, endMarker string) (string, bool) {
	start := strings.Index(s, startMarker)
	if start == -1 {
		return "", false
	}

	s = s[start+len(startMarker):]

	end := strings.Index(s, endMarker)
	if end == -1 {
		return "", false
	}

	return s[:end], true
}

func TestShellMarksTheCurrentPage(t *testing.T) {
	t.Parallel()

	h := chainServer(t, fullChainNode(t))

	tests := []struct {
		path   string
		nav    string
		tabs   bool
		tab    string
		crumbs []string
	}{
		{path: "/", nav: "Overview", tabs: false, tab: "", crumbs: []string{"Network"}},
		{path: "/peers", nav: "Peers", tabs: false, tab: "", crumbs: []string{"Network", "Peers"}},
		{path: "/consensus", nav: "Consensus", tabs: false, tab: "", crumbs: []string{"Network", "Consensus"}},
		{path: "/metrics", nav: "Metrics", tabs: false, tab: "", crumbs: []string{"Network", "Metrics"}},
		{path: "/metrics?node=nodeX", nav: "Metrics", tabs: false, tab: "", crumbs: []string{"Network", "Metrics"}},
		{path: "/n/node1/", nav: "", tabs: true, tab: "Overview", crumbs: []string{"Network", "node1"}},
		{path: "/n/node1/blocks", nav: "", tabs: true, tab: "Blocks", crumbs: []string{"Network", "node1", "Blocks"}},
		{path: "/n/node1/mempool", nav: "", tabs: true, tab: "Mempool", crumbs: []string{"Network", "node1", "Mempool"}},
		{path: "/n/node1/blocks?from=x", nav: "", tabs: true, tab: "Blocks", crumbs: []string{"Network", "node1", "Blocks"}},
		{path: "/n/node1/block/1", nav: "", tabs: true, tab: "", crumbs: []string{"Network", "node1", "Block 1"}},
		{path: "/n/node1/tx/" + txA, nav: "", tabs: true, tab: "", crumbs: []string{"Network", "node1", "Transaction"}},
		{path: "/n/node1/address/" + ownerAddr, nav: "", tabs: true, tab: "", crumbs: []string{"Network", "node1", "Address"}},
		{path: "/n/node1/contract/" + txA, nav: "", tabs: true, tab: "", crumbs: []string{"Network", "node1", "Contract"}},
		{path: "/search?node=node1", nav: "", tabs: true, tab: "", crumbs: []string{"Network", "node1", "Search"}},
		{path: "/n/nodeX/blocks", nav: "", tabs: false, tab: "", crumbs: []string{"Network", "Not found"}},
	}

	for _, tc := range tests {
		rec := do(h, http.MethodGet, tc.path)
		body := rec.Body.String()

		sidebarGroup, ok := extractBetween(body, `aria-labelledby="nav-net">`, "</ul>")
		if !ok {
			t.Fatalf("%s: missing sidebar group", tc.path)
		}

		var gotNav []string
		for _, m := range reAriaCurrent.FindAllStringSubmatch(sidebarGroup, -1) {
			gotNav = append(gotNav, m[1])
		}

		var wantNav []string
		if tc.nav != "" {
			wantNav = []string{tc.nav}
		}

		if !slices.Equal(gotNav, wantNav) {
			t.Errorf("%s: sidebar nav aria-current got %v, want %v", tc.path, gotNav, wantNav)
		}

		tabsGroup, hasTabs := extractBetween(body, `<ul class="tabs">`, "</ul>")
		if hasTabs != tc.tabs {
			t.Errorf("%s: tabs present got %v, want %v", tc.path, hasTabs, tc.tabs)
		}

		if tc.tabs {
			var gotTab []string
			for _, m := range reAriaCurrent.FindAllStringSubmatch(tabsGroup, -1) {
				gotTab = append(gotTab, m[1])
			}

			var wantTab []string
			if tc.tab != "" {
				wantTab = []string{tc.tab}
			}

			if !slices.Equal(gotTab, wantTab) {
				t.Errorf("%s: tab aria-current got %v, want %v", tc.path, gotTab, wantTab)
			}
		}

		crumbsGroup, ok := extractBetween(body, `<ol class="crumbs">`, "</ol>")
		if !ok {
			t.Fatalf("%s: missing crumbs group", tc.path)
		}

		var gotCrumbs []string
		for _, m := range reCrumb.FindAllStringSubmatch(crumbsGroup, -1) {
			gotCrumbs = append(gotCrumbs, m[1])
		}

		if !slices.Equal(gotCrumbs, tc.crumbs) {
			t.Errorf("%s: crumbs got %v, want %v", tc.path, gotCrumbs, tc.crumbs)
		}

		if count := strings.Count(crumbsGroup, `aria-current="page"`); count != 1 {
			t.Errorf("%s: crumbs aria-current count got %d, want 1", tc.path, count)
		}

		lastCrumb := tc.crumbs[len(tc.crumbs)-1]

		wantLastSpan := `<span aria-current="page">` + lastCrumb + "<"
		if !strings.Contains(crumbsGroup, wantLastSpan) {
			t.Errorf("%s: crumbs group does not end with span aria-current for %q", tc.path, lastCrumb)
		}

		wantCount := 1
		if tc.nav != "" {
			wantCount++
		}

		if tc.tab != "" {
			wantCount++
		}

		if gotCount := strings.Count(body, `aria-current="page"`); gotCount != wantCount {
			t.Errorf("%s: total aria-current count got %d, want %d", tc.path, gotCount, wantCount)
		}

		if len(tc.crumbs) > 2 && tc.crumbs[1] == "node1" {
			if !strings.Contains(crumbsGroup, `<a href="/n/node1/">node1</a>`) {
				t.Errorf("%s: crumbs group missing <a href=\"/n/node1/\">node1</a>", tc.path)
			}
		}
	}
}

func TestSearchNodeErrorKeepsTheSearchPlace(t *testing.T) {
	t.Parallel()

	n := routedNode(t, map[string]reply{
		"/tx/" + txA:    {http.StatusInternalServerError, `{}`},
		"/block/" + txA: ok(`{}`),
	})
	rec := do(chainServer(t, n), http.MethodGet, "/search?node=node1&q="+txA)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("code got %d, want %d", rec.Code, http.StatusBadGateway)
	}

	body := rec.Body.String()

	crumbsGroup, ok := extractBetween(body, `<ol class="crumbs">`, "</ol>")
	if !ok {
		t.Fatal("missing crumbs group")
	}

	var gotCrumbs []string
	for _, m := range reCrumb.FindAllStringSubmatch(crumbsGroup, -1) {
		gotCrumbs = append(gotCrumbs, m[1])
	}

	wantCrumbs := []string{"Network", "node1", "Search"}
	if !slices.Equal(gotCrumbs, wantCrumbs) {
		t.Errorf("crumbs got %v, want %v", gotCrumbs, wantCrumbs)
	}

	if !strings.Contains(crumbsGroup, `<span aria-current="page">Search<`) {
		t.Errorf("crumbs group missing <span aria-current=\"page\">Search<: %s", crumbsGroup)
	}

	if !strings.Contains(crumbsGroup, `<a href="/n/node1/">node1</a>`) {
		t.Errorf("crumbs group missing <a href=\"/n/node1/\">node1</a>: %s", crumbsGroup)
	}

	tabsGroup, hasTabs := extractBetween(body, `<ul class="tabs">`, "</ul>")
	if !hasTabs {
		t.Fatal("tabs group missing")
	}

	if matches := reAriaCurrent.FindAllStringSubmatch(tabsGroup, -1); len(matches) != 0 {
		t.Errorf("tabs group has aria-current: %v", matches)
	}

	if count := strings.Count(body, `aria-current="page"`); count != 1 {
		t.Errorf("total aria-current count got %d, want 1", count)
	}
}
