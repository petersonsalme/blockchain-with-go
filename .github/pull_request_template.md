## Summary

<!-- What changes and why, in a few lines. -->

## AI assistance disclosure

This is a personal learning project and a pilot for AI-agent tooling. Changes may be AI-generated suggestions.

- [ ] This change was written or suggested by an AI agent (name the tool/agent: ...)
- [ ] This change was written by me without AI assistance
- [ ] I reviewed the change myself before opening this PR

## Checklist

- [ ] `go test ./...` passes
- [ ] `gofmt -s -l .` reports nothing
- [ ] `go build -o /dev/null ./cmd/bwg` succeeds
- [ ] Dependencies still point inward (`domain` imports nothing internal; `chain` does not import `server` or `net/http`)
- [ ] No global state added; dependencies are injected through constructors
- [ ] README updated if behavior, API or configuration changed
- [ ] Commit messages follow Conventional Commits

## Mutation testing

Run from the repository root, without a package path, after `go clean -testcache`:

```
go run github.com/go-gremlins/gremlins/cmd/gremlins@v0.6.0 unleash --timeout-coefficient 20 --diff <base>
```

Paste the output below. Zero `LIVED` or `NOT COVERED` mutants on the changed lines; `TIMED OUT` does not count as killed.

```
<gremlins output>
```

## Notes for the reviewer

<!-- Trade-offs, follow-ups, anything non-obvious. -->

> Educational project. Not production code and not affiliated with the author's employer or clients.
