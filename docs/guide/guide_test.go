package guide_test

import (
	"io/fs"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

var (
	// anchorRe matches `path/to/file.go:Func` and `path/to/file.go:Type.Method`.
	anchorRe = regexp.MustCompile("`([A-Za-z0-9_./-]+\\.go):([A-Za-z_][A-Za-z0-9_]*(?:\\.[A-Za-z_][A-Za-z0-9_]*)?)`")
	adrRe    = regexp.MustCompile(`\]\((\.\./adr/[^)#\s]+)(?:#[^)]*)?\)`)
	stepRe   = regexp.MustCompile(`^\d+\. `)
)

// repoFS is the repository root, two levels above docs/guide.
func repoFS(t *testing.T) fs.FS {
	t.Helper()

	return os.DirFS("../..")
}

// declares reports whether the Go file src declares the function or method name.
func declares(src, name string) bool {
	typ, method, isMethod := strings.Cut(name, ".")
	if !isMethod {
		return regexp.MustCompile(`(?m)^func ` + regexp.QuoteMeta(name) + `[(\[]`).MatchString(src)
	}

	return regexp.MustCompile(`(?m)^func \(\w* ?\*?` + regexp.QuoteMeta(typ) + `(?:\[[^\]]*\])?\) ` +
		regexp.QuoteMeta(method) + `[(\[]`).MatchString(src)
}

// checkAnchor returns a problem description, or "" when the anchor holds.
func checkAnchor(root fs.FS, file, name string) string {
	data, err := fs.ReadFile(root, file)
	if err != nil {
		return "anchor " + file + ":" + name + ": file does not exist"
	}

	if !declares(string(data), name) {
		return "anchor " + file + ":" + name + ": no such func or method declaration in the file"
	}

	return ""
}

// steps splits the numbered list of doc into one text block per step; a step
// runs until the next numbered line or heading.
func steps(doc string) []string {
	var (
		out []string
		cur []string
		in  bool
	)

	flush := func() {
		if in {
			out = append(out, strings.Join(cur, "\n"))
		}

		cur, in = nil, false
	}

	for line := range strings.SplitSeq(doc, "\n") {
		switch {
		case stepRe.MatchString(line):
			flush()

			in = true
			cur = []string{line}
		case strings.HasPrefix(line, "#"):
			flush()
		case in:
			cur = append(cur, line)
		}
	}

	flush()

	return out
}

// check returns every problem of one guide page: missing anchors, missing ADR
// files, and numbered steps without an anchor or an ADR link.
func check(root fs.FS, name, doc string) []string {
	var problems []string

	for _, m := range anchorRe.FindAllStringSubmatch(doc, -1) {
		if p := checkAnchor(root, m[1], m[2]); p != "" {
			problems = append(problems, name+": "+p)
		}
	}

	for _, m := range adrRe.FindAllStringSubmatch(doc, -1) {
		target := "docs/adr/" + strings.TrimPrefix(m[1], "../adr/")

		_, err := fs.Stat(root, target)
		if err != nil {
			problems = append(problems, name+": ADR link "+m[1]+" points to a missing file")
		}
	}

	for i, s := range steps(doc) {
		first, _, _ := strings.Cut(s, "\n")

		if !anchorRe.MatchString(s) {
			problems = append(problems, name+": step "+strconv.Itoa(i+1)+" has no code anchor: "+first)
		}

		if !adrRe.MatchString(s) {
			problems = append(problems, name+": step "+strconv.Itoa(i+1)+" has no ADR link: "+first)
		}
	}

	return problems
}

func read(t *testing.T, name string) string {
	t.Helper()

	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}

	return string(data)
}

func TestGuideAnchorsAndLinks(t *testing.T) {
	root := repoFS(t)

	for _, name := range []string{"README.md", "transaction.md", "reorg.md"} {
		for _, p := range check(root, name, read(t, name)) {
			t.Error(p)
		}
	}
}

func TestPagesHaveDiagramAndSteps(t *testing.T) {
	for _, name := range []string{"transaction.md", "reorg.md"} {
		doc := read(t, name)

		if !strings.Contains(doc, "```mermaid\nsequenceDiagram") {
			t.Errorf("%s: no mermaid sequenceDiagram", name)
		}

		if len(steps(doc)) == 0 {
			t.Errorf("%s: no numbered steps", name)
		}
	}
}

// required lists the anchors each page must cover, in this order.
var required = map[string][]string{
	"transaction.md": {
		"cmd/bwg/transfer.go:buildTransfer",
		"internal/domain/transaction.go:SignInput",
		"internal/server/server.go:Server.handleSubmitTx",
		"internal/chain/service.go:Service.Submit",
		"internal/mempool/mempool.go:Pool.Add",
		"internal/p2p/gossip.go:Node.AnnounceTx",
		"internal/p2p/gossip.go:Node.offer",
		"internal/p2p/gossip.go:Node.Inv",
		"internal/p2p/gossip.go:Node.ReceiveTx",
		"internal/chain/service.go:Service.MinePending",
		"internal/chain/service.go:Service.fit",
		"internal/chain/service.go:Service.mineLocked",
		"internal/consensus/pow/pow.go:ProofOfWork.Seal",
		"internal/domain/rules.go:ValidateBlockRules",
		"internal/domain/utxo.go:BlockDelta",
		"internal/storage/bolt/indexed.go:Repository.Commit",
		"internal/mempool/mempool.go:Pool.Prune",
		"internal/p2p/gossip.go:Node.ReceiveBlock",
		"internal/chain/service.go:Service.AcceptBlock",
		"internal/server/server.go:Server.handleGetTxProof",
		"internal/chain/service.go:Service.TxProof",
	},
	"reorg.md": {
		"internal/p2p/sync.go:Node.Sync",
		"internal/p2p/sync.go:Node.syncFrom",
		"internal/p2p/sync.go:Node.forkPoint",
		"internal/p2p/sync.go:Node.winningHeaders",
		"internal/p2p/sync.go:Node.fetchBodies",
		"internal/chain/service.go:Service.ReplaceChain",
		"internal/chain/service.go:forkPoint",
		"internal/storage/bolt/indexed.go:Repository.Reorg",
		"internal/chain/service.go:orphaned",
		"cmd/bwg/p2p.go:syncLedger.ReplaceChain",
	},
}

func TestRequiredAnchorsInOrder(t *testing.T) {
	for name, want := range required {
		doc := read(t, name)

		var got []string

		for _, m := range anchorRe.FindAllStringSubmatch(doc, -1) {
			got = append(got, m[1]+":"+m[2])
		}

		pos := 0

		for _, w := range want {
			i := slices.Index(got[pos:], w)
			if i < 0 {
				t.Errorf("%s: anchor %s missing or out of order", name, w)

				continue
			}

			pos += i + 1
		}
	}
}

func TestReorgNamesADRs(t *testing.T) {
	doc := read(t, "reorg.md")

	for _, adr := range []string{"../adr/0019-initial-sync-and-forks.md", "../adr/0029-reorg-by-cumulative-work.md"} {
		if !strings.Contains(doc, "]("+adr+")") {
			t.Errorf("reorg.md does not link %s", adr)
		}
	}
}

func TestIndexLinksBothPages(t *testing.T) {
	idx := read(t, "README.md")

	for _, page := range []string{"transaction.md", "reorg.md"} {
		if !strings.Contains(idx, "]("+page+")") {
			t.Errorf("README.md does not link %s", page)
		}
	}

	root, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(string(root), "](docs/guide/README.md)") {
		t.Error("root README.md does not link docs/guide/README.md")
	}
}

// TestCheckerFails proves the checker rejects each kind of drift.
func TestCheckerFails(t *testing.T) {
	root := repoFS(t)

	good := "1. Admit. `internal/chain/service.go:Service.Submit` and [ADR-0028](../adr/0028-mempool.md).\n"
	if p := check(root, "x.md", good); len(p) != 0 {
		t.Fatalf("valid page reported: %v", p)
	}

	cases := map[string]string{
		"renamed method":  strings.ReplaceAll(good, "Service.Submit", "Service.Submitx"),
		"missing file":    strings.ReplaceAll(good, "service.go", "servicex.go"),
		"missing ADR":     strings.ReplaceAll(good, "0028-mempool.md", "0028-mempoolx.md"),
		"step no anchor":  "1. Admit. [ADR-0028](../adr/0028-mempool.md).\n",
		"step no ADR":     "1. Admit. `internal/chain/service.go:Service.Submit`.\n",
		"unknown func":    strings.ReplaceAll(good, "Service.Submit", "submit"),
		"wrong type":      strings.ReplaceAll(good, "Service.Submit", "Pool.Submit"),
		"second step bad": good + "2. Second. [ADR-0028](../adr/0028-mempool.md).\n",
	}

	for name, doc := range cases {
		if p := check(root, "x.md", doc); len(p) == 0 {
			t.Errorf("%s: checker found no problem", name)
		}
	}
}
