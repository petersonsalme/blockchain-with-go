# Contributing

This is an **educational, personal learning project** and a pilot for AI-agent tooling. Many changes are AI-generated suggestions (see the disclaimer in the [README](README.md)). It is maintained on personal time, so reviews may be slow.

## Before opening a PR

Follow the rules in [AGENTS.md](AGENTS.md):

```
go test ./...
gofmt -s -l .
go build -o /dev/null ./cmd/bwg
```

- Keep the architecture: dependencies point inward (`domain` imports nothing internal), and no global state.
- Commit messages follow Conventional Commits (`feat:`, `fix:`, `docs:`, `refactor:`, `test:`), in English.
- Mutation testing on the diff, run from the root without a package path and after `go clean -testcache`:

  ```
  go run github.com/go-gremlins/gremlins/cmd/gremlins@v0.6.0 unleash --timeout-coefficient 20 --diff <base>
  ```

  Zero `LIVED` or `NOT COVERED` mutants on the changed lines. Paste the output in the PR description.

## AI assistance

Say in the PR whether AI wrote or suggested the change. You remain responsible for understanding and reviewing what you submit.

## Security

Do not open public issues for vulnerabilities. See [SECURITY.md](SECURITY.md).
