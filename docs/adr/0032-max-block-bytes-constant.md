# ADR-0032: MAX_BLOCK_BYTES becomes a consensus constant

Status: Accepted (ENG-351; supersedes the `MAX_BLOCK_BYTES` variable of the README and the exception noted in ADR-0016)

## Context
- The block size limit is a consensus rule: `domain.ValidateBlockRules` rejects a block larger than the limit with `block_too_large`, on the mining path and on every block a node accepts from a peer (`AcceptBlock`, initial sync).
- `domain.MaxBlockBytes()` reads it from the `MAX_BLOCK_BYTES` environment variable on every call (`internal/domain/rules.go`, default 1 MiB). Two nodes started with different values disagree on the validity of the same block: a block of 900 KiB mined by a node with 2 MiB is rejected by a node with 512 KiB, so the network splits without any fork in the chain-work sense (ADR-0010).
- It is also the only configuration read outside `cmd/bwg` (ADR-0016) and the only global state in `internal/domain`, which the architecture rules exclude.
- The same value bounds the HTTP request body of `POST /` (`2 × limit`) and the body response of initial sync (`16 × 2 × limit`, ADR-0019); those bounds are local, but they must follow the rule.

## Options
1. **Compile-time constant in `internal/domain` (chosen).** `const MaxBlockBytes = 1 << 20`. Every node of the same build applies the same rule, like `InitialSubsidy` and `HalvingInterval` (ADR-0015). Cost: one constant, the env read removed, tests that lowered the limit through the variable rewritten. Changing the value later is a hard fork, done by a new ADR and a new release, as for every other consensus constant.
2. **Field of the genesis block.** The limit travels with the chain and two networks may choose different values. Cost: a change of the hashed shape of `Block` (new genesis hash, every existing data directory invalid), a new configuration variable for the genesis creator, and every validator must read the genesis before validating anything. Much larger than the problem, and the project has one network.
3. **Keep the variable, check it at the handshake.** The hello (ADR-0013) carries the limit and peers with another value are refused. Cost: a p2p protocol change, and the rule is still read in `internal/domain`; it only turns a silent split into a refused connection.

## Decision
- `internal/domain` declares `const MaxBlockBytes = 1 << 20` (1,048,576 bytes, the previous default, so every existing chain stays valid). `MaxBlockBytes()`, `DefaultMaxBlockBytes` and `MaxBlockBytesEnvVar` are removed; `internal/domain` imports neither `os` nor `strconv` for it and reads no environment variable.
- `ValidateBlockRules` always validates against the constant. `ValidateSize(b, limit)` keeps its parameter so the rule itself stays unit-testable with small limits.
- The mining size check, the pool selection budget, the `POST /` body bound and the sync response bound all derive from `domain.MaxBlockBytes`. No production constructor, option or flag takes another limit. A test that needs a smaller mining budget may only use an unexported field set from an `export_test.go` file, which is not compiled into the binary.
- `bwg node` refuses to start when `MAX_BLOCK_BYTES` is set to a non-blank value, with an error that names the variable and this ADR, so an operator who relied on it learns that it no longer applies instead of running with a limit they did not choose.

## Consequences
- Nodes of the same build cannot disagree on the size rule; changing it is a release-level hard fork with its own ADR.
- Deployments that set `MAX_BLOCK_BYTES` stop at startup until the variable is removed; none of the committed compose files or scripts set it.
- Tests can no longer shrink the accepted block size through the environment: tests of the accept path build blocks above 1 MiB (a padded field), and `ValidateSize` keeps the small-limit cases.
- The README configuration table loses the `MAX_BLOCK_BYTES` row and the docs that mention it name the constant instead; ADR-0016's sentence about the on-demand read becomes historical.
