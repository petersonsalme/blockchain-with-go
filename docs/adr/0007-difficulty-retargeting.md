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

## What we learned
- Decision item 6 holds only when one difficulty step changes the block time by less than the width of the dead band. Difficulty counts leading **hex** zeros, so one step multiplies the expected work by 16, and the band `[target/2, 2*target]` is only 4× wide. For about half of all hash rates no difficulty lands inside the band, and the difficulty then alternates ±1 on every window. The convergence test models block time as `2^d`, so it does not exercise this case. Measured in the ENG-19 review with the real `ExpectedDifficulty`, N=10, a 10s target and time = 16^d / hash rate: 100k H/s held at 5, 1M H/s held at 6, and 300k H/s alternated `5 6 5 6 ...`.
- With the defaults, a fast machine settles on a difficulty whose mean mining time can exceed `MINING_TIMEOUT` (10s). The measurement was ~1.45M H/s, d=6, mean ≈ 11.6s, so ≈42% of `POST /` return `504`. Slow windows bring the difficulty back down, so the chain does not stall.
- The design is unchanged. Known limitation: ±1 oscillation and `504`s under the defaults remain possible until the difficulty becomes finer-grained (a numeric or bit-based target) or the defaults are realigned with `MINING_TIMEOUT`. Both are tracked as a follow-up story.
