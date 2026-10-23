# ADR-0012: Proof-of-work difficulty counts leading zero bits

Status: Proposed (Spec-Opus, 2026-10-23, ENG-99). Amends ADR-0008 (decision items 2, 5 and 6: the unit of `Difficulty`, the bounds and the defaults).

## Context
- ADR-0008 "What we learned": `Difficulty` counts leading **hex** zeros, so one step multiplies the expected work by 16, while the dead band `[target/2, 2*target]` is only 4× wide. For about half of all hash rates no difficulty lands inside the band and the difficulty alternates ±1 every window (300k H/s: `5 6 5 6 ...`).
- With the defaults (`TARGET_BLOCK_TIME=10s`, `MINING_TIMEOUT=10s`), even a stable difficulty can have a mean mining time above `MINING_TIMEOUT`: ~1.45M H/s settles at d=6, mean ≈ 11.6s, ≈42% of `POST /` return `504`.
- A stable difficulty exists for every hash rate only if one step changes the block time by less than the band width (4×).

## Options
A. **Leading zero bits.** `Difficulty` = number of leading zero bits of the SHA-256 hash. One step = 2× the work, inside the 4× band, so for any hash rate some difficulty lands in the band and stays there. Cost: the meaning of a persisted, hashed field changes (same type and shape, new unit); `POW_DIFFICULTY`, `MinDifficulty`, `MaxDifficulty` and `DefaultDifficulty` are rescaled (×4); `solvesDifficulty` decodes nibbles; tests that assert hex prefixes are rewritten. No new field, no change to the retarget rule.
B. **Numeric target (Bitcoin-style, `hash <= target` with proportional retarget).** Arbitrary granularity. Cost: a new representation (`math/big` or a compact encoding) of a hashed field, a different retarget rule (proportional, clamped), ADR-0008 rewritten rather than amended, much larger test and review surface. More than a learning project needs (ADR-0003).
C. **Keep hex zeros, realign the defaults only.** Raise `MINING_TIMEOUT` or lower `MaxDifficulty`/`TARGET_BLOCK_TIME`, document the oscillation. Cost: the ±1 oscillation stays for half of all hash rates; with alternation a slow window can take up to ~8× the target, so a safe `MINING_TIMEOUT` would be ~80s at a 10s target.

## Decision
Option A, plus realigned defaults.
1. `Difficulty` counts the leading zero **bits** of the hash, read as the 256-bit big-endian number its 64 hex characters encode. `solvesDifficulty(hash, d)` is true iff the hash has at least `d` leading zero bits; `d <= 0` is always solved; `d > 256`, or a hash that is not 64 lowercase hex characters, is never solved.
2. Rescaled constants: `DefaultDifficulty = 8` (the old 2 hex zeros), `MinDifficulty = 1`, `MaxDifficulty = 32` (the old 8 hex zeros). `POW_DIFFICULTY` is read in bits.
3. The retarget rule of ADR-0008 item 2 is unchanged (same window, same factor-2 dead band, ±1 per window, same bounds semantics), now on bits.
4. `DefaultTargetBlockTime = 2s`; `MINING_TIMEOUT` keeps its 10s default. At equilibrium the mean block time is in `[target/2, 2*target]`, so with the defaults the worst-case mean is 4s and `P(504) <= e^-2.5 ≈ 8%`; at the band centre it is below 1%. README documents the rule of thumb `MINING_TIMEOUT >= 5 × TARGET_BLOCK_TIME`.
5. No migration. Chains already stored under `DATA_DIR` keep their recorded `Difficulty` values, now read as bits; their hashes still satisfy them (k hex zeros ⇒ 4k zero bits ≥ k). A node restarted on an old data dir may expect a different difficulty for the next block than the one the old rule would have; deleting `DATA_DIR` is the documented upgrade path (ADR-0003: learning project).

## Consequences
- For any hash rate between the speeds that `MinDifficulty` and `MaxDifficulty` allow, retargeting settles on one difficulty and stays there once the measured window is inside the band (deterministic model `time = 2^d / hashrate`). Noise in real block times can still move it near the band edges.
- Convergence is slower in steps: from 8 bits to ~21 bits (≈1.45M H/s at 2s) takes ~13 windows. Unchanged per-window behaviour.
- `Block.Difficulty` keeps its type, JSON name and position in the hash; `openapi.yaml` only changes its description (if it has one).
- The ADR-0008 status line gets "Amended by ADR-0012"; README §3 and the variable table describe bits and the new default.

## What we learned
(empty until a measurement surprises us)
