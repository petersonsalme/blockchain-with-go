# ADR-0034: Block timestamps must be later than the median time past of the last 11 blocks

Status: Accepted (ENG-353; replaces the "not before its parent" timestamp rule of `domain.ValidateTimestamp`)

## Context
- `domain.ValidateTimestamp(b, parent, now)` has two rules: the timestamp is not before the parent's (`timestamp_before_parent`) and not more than `MaxFutureDrift` (2h) ahead of the validator's clock (`timestamp_too_far_in_future`).
- The parent rule lets one block hold the whole network back. A miner whose clock (or will) is 2h ahead mines a block at `now + 2h`; it is valid. Every honest miner then stamps its next block with `time.Now()`, which is before that parent, so its own block fails `ValidateBlockRules` in `mineLocked` and nobody can extend the chain for up to 2h, except the miner who caused it.
- Timestamps feed the difficulty retarget (ADR-0008, ADR-0012: `pow.ExpectedDifficulty` measures `parent.Timestamp − history[len−1−n].Timestamp`). A rule that one block can game makes the window measurable by one miner; the time-warp attack moves the window's endpoints to lower the difficulty.
- The reference answer (Bitcoin, BIP 113's "median time past") is a rule on a window instead of on one block: a new timestamp must be strictly later than the median of the last 11 timestamps. One outlier cannot move a median of 11; it takes 6 of the last 11 blocks.
- Every caller already has the full history (`ReplaceChain` the candidate prefix, `AcceptBlock` and `mineLocked` `repo.All()`); pruned heights keep their headers and timestamps (ADR-0031).

## Options
1. **Add `timestamp > MTP(11)` next to the parent rule.** Cost: smallest change, no API code removed. Gain is almost nil: a chain that passed the parent rule is non-decreasing, so the parent is already ≥ the median, and the new rule only adds "strictly"; the 2h stall above stays.
2. **Replace the parent rule with `timestamp > MTP(11)` (chosen).** Cost: `ValidateTimestamp` and `ValidateBlockRules` take the history instead of the parent (three call sites in `internal/chain/service.go`), the code `timestamp_before_parent` leaves the HTTP API for `timestamp_not_after_median_time_past`, and test fixtures with equal timestamps must move forward. Gain: one outlier cannot stall honest miners nor move the rule, and the rule is the one the literature studies.
3. **Option 2 plus a lower `MaxFutureDrift` or a network-adjusted time.** Cost: a peer clock-offset protocol (p2p change) for a learning network of a few nodes. Out of proportion; can be a later ADR.

## Decision
- `domain.MedianTimeSpan = 11`. `domain.MedianTimePast(history []*Block) time.Time` takes the timestamps of the last `min(11, len(history))` blocks of `history` (oldest to newest), sorts them ascending and returns the element at index `len/2`; with an empty history it returns the zero `time.Time`. It depends on nothing but its argument.
- `domain.ValidateTimestamp(b *Block, history []*Block, now time.Time)` rejects `b` when `!b.Timestamp.After(MedianTimePast(history))` with the new code `timestamp_not_after_median_time_past` (`CodeTimestampNotAfterMedianTimePast`; details `timestamp` and `median_time_past`, RFC 3339 nano), then applies the unchanged `MaxFutureDrift` rule. The check against the parent alone and `CodeTimestampBeforeParent` are removed.
- `domain.ValidateBlockRules(b *Block, history []*Block, now time.Time)` keeps its order (size, merkle root, timestamp); `history` ends with `b`'s parent.
- The block generator (`chain.GenerateBlockWithContext`) stamps `max(time.Now(), MedianTimePast(history) + 1ns)`, so an honest miner whose clock is behind the median still produces a valid block (ADR-0020's future-genesis check keeps a fresh chain from starting in the future).
- Retarget is unchanged: `pow.ExpectedDifficulty` already treats a negative span as "no information" and keeps the difficulty.

## Consequences
- A block may carry a timestamp earlier than its parent as long as it is later than the median; explorers and the dashboard must not assume monotonic block times.
- Blocks with equal timestamps stop being valid once the median reaches them; existing chains made by `bwg` stamp `time.Now()` with nanoseconds, so they stay valid, but test fixtures built with one fixed time must advance it per block.
- The HTTP `422` for a too-early block carries `timestamp_not_after_median_time_past` instead of `timestamp_before_parent`; the README error table follows.
- ADR-0020's remark that a future genesis makes children fail `timestamp_before_parent` stays true with the new code (the median of a one-block history is the genesis time); the older ADR text is left as history.
