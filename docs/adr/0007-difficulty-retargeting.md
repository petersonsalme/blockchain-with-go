# ADR-0007: Difficulty retargeting with history-aware consensus

Status: Accepted (board, 2026-10-21, ENG-19)

## Context
- PoW difficulty is fixed by `POW_DIFFICULTY`, and `pow.Verify` accepts any `b.Difficulty` that the hash satisfies.
- ENG-19 asks for retargeting every N blocks towards `TARGET_BLOCK_TIME`, inside the PoW `Consensus`, and for rejecting blocks whose difficulty is not the one the rule expects.
- The `Consensus` port only exposes the parent: `Seal(ctx, b)` and `Verify(prev, b)`. Retargeting needs the timestamps of the last N blocks.

## Options
A. The port receives the history: `Seal(ctx, history, b)` and `Verify(history, b)`, with `history` ordered oldest to newest and its last element being the parent. Cost: mechanical signature and test changes in `consensus`, `pow`, `poa`, `consensustest` and `chain`; `Mine` reads `repo.All()`.
B. Keep the port and inject a `BlockRepository` into PoW. Cost: `consensus` depends on `storage`, `Verify` depends on external state and cannot validate a chain that is not in the repository, and the wiring in `cmd/node` becomes circular.
C. An optional `DifficultyAdjuster` queried by `chain.Service`. Cost: the rule leaks into `chain`, against ENG-19 item 3.

## Decision
Option A.
1. `Consensus.Seal(ctx, history []*domain.Block, b)` and `Consensus.Verify(history []*domain.Block, b)`; the parent is `history[len(history)-1]`. The block generator in `chain` receives `history` instead of `prev`. PoA uses only the parent.
2. `pow.ProofOfWork` computes `ExpectedDifficulty(history)`:
   - the child of genesis uses the initial difficulty (`POW_DIFFICULTY`);
   - at height `h` with `h % N == 0` and `h >= N+1`: `actual = ts(h-1) - ts(h-1-N)`, `target = N * TARGET_BLOCK_TIME`;
     - `actual < target/2` → parent + 1, only while parent < `MaxDifficulty`;
     - `actual > 2*target` → parent − 1, only while parent > `MinDifficulty`;
     - otherwise the parent's difficulty;
   - every other height uses the parent's difficulty.
3. `Seal` mines with the expected difficulty. `Verify` returns `consensus.ErrInvalidDifficulty` when `b.Difficulty != ExpectedDifficulty(history)`, checked before the proof of work.
4. Configuration, read through `pow.NewProofOfWorkFromEnv` and triggered only from `cmd/node`:
   - `POW_RETARGET_INTERVAL` (N, default `10`; `0` disables retargeting, so the expected difficulty is always the initial one);
   - `TARGET_BLOCK_TIME` (Go duration, default `10s`);
   - invalid values fall back to the defaults, like `POW_DIFFICULTY`.
5. `MinDifficulty = 1` and `MaxDifficulty = 8` are constants. Adjustment never crosses them; an initial difficulty outside them is kept.
6. The factor-2 dead band is fixed: it prevents ±1 oscillation every window and makes convergence testable.

## Consequences
- A block whose hash satisfies its recorded difficulty but whose difficulty is off-rule is now invalid.
- `Mine` reads the whole chain on every call (O(n); with `DATA_DIR` set this is a bbolt read).
- `ExpectedDifficulty` is pure (no clock, environment or repository), so any chain can be re-verified, which a future `replaceChain` can reuse.
- Miners still choose block timestamps, so time-warp within the ENG-18 timestamp bounds remains possible. Known limitation, out of scope.
- README §3 and the environment variable table, and the composition-root line in AGENTS.md, must describe the new variables.
