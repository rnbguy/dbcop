# Checker improvement plan

Status: sections 1 and 3 now have an executable bounded campaign and a fixed
SAT/UNSAT latency baseline. Search optimizations remain proposals, not
implementation changes. This is the active improvement plan; runnable commands,
recorded measurements and model boundaries are in
[development.md](development.md).

## Decisions

1. Add targeted, diverse Apalache traces alongside exhaustive small-domain TLC.
2. Establish exact state identity and the fixed-width argument before adding
   pruning. Keep the public solver trait and no_std compatibility unchanged.
3. Benchmark fixed satisfiable and unsatisfiable inputs before changing search.
4. Profile repeated work, then optimize it. Treat chess heuristics as ordering
   suggestions, never permission to discard an unproved branch.
5. Restore the stronger decomposition bound only with correct projection and
   witness composition across articulation sessions.

## What the existing validation establishes

The completed canonical TLC campaign passed its six gates. The subsequent
source-current search-trace check accepted 189 actual captures from 31 histories
and 1,517 stable search nodes across default, deterministic-exhaustive and
budgeted-restart profiles. Corrupted observations and terminal-consumption
negative controls were rejected. This is bounded compatibility evidence, not a
proof for arbitrary histories or hash collisions.

Real Apalache v0.62.4 server tests passed all seven levels. The directed gate
cross-classified 20 distinct observed histories through TLC, Rust core and the
independent reference; the SQL matrix did the same for 75 distinct histories.
Those counts are distinct within each test, not a globally deduplicated corpus.

The current Apalache backend chooses a successor for one concrete backend
operation. Each request initializes a fresh symbolic session, takes the
operation step, queries Result, and disposes the session. Go supplies the
workload and accumulated history. This is not yet an autonomous bounded
generator of complete replica/interleaving traces. Physical replica delivery is
modeled separately in ReplicaHistories.tla; a MySQL connection is a history
session, not a replica.

The fresh native exhaustive incremental regression also passed its domain of
253,537 generated histories, with up to two sessions, three transactions total,
four events total and two variables, across all seven levels. Its successor
probes and lifecycle checks are native/reference comparisons, not TLC runs.

## 1. Apalache short-trace campaign

Implemented in `crates/core/tests/tla/ReplicaTraces.tla`,
`mockserver/backend/apalache_trace_campaign_test.go` and
`mockserver/backend/apalache_trace_validation_test.go`. The operational model
retains the existing Begin/Read/Write/Commit/Deliver rules with one ordered
session per replica, no aborts and no isolation admission filter.

The campaign uses the already-provisioned v0.62.4 server API rather than a new
CLI runner: load, initialize, unroll, query Result, then enumerate `nextModel`
projections until certified exhaustion. Result contains full completed session
histories but omits delivery schedules and auxiliary model choices. This
enumerates distinct projected histories, not all schedules. Fixed-capacity
storage avoids the nested-sequence growth observed in the initial encoding.

User-selected budget: ten seconds for the complete first-model generation,
including load and unrolling, then ten seconds for each subsequent model.
Validation callbacks do not consume generation time; cleanup has an independent
five-second deadline. Safely disposed timeouts reduce depth and
transaction/event bounds. The original bound stays inconclusive. Unknown
allocations or cleanup failure stop the campaign rather than creating hidden
overlapping solver work.

Profiles cover one to three replicas at depths three/four, unrestricted
two-transaction/two-event histories at depth six with adaptive reduction, and
three depth-eight workload partitions: write skew, competing snapshot writers
and changing external read sources. Partitions constrain event/source patterns,
not checker answers. A targeted timeout remains inconclusive rather than
silently dropping the events that define its case.

An unmodified Zobrist implementation is retained. Exact source/local read
validation precedes native batch classification and persistent
trial/reset/commit replay. The existing TLC bridge independently compares all
seven levels, the Rust reference and successful witnesses. Small one-transaction
and targeted projections also match independently constructed complete history
sets.

Generation artifacts retain numeric inputs, model/input hashes, raw RPC
responses, histories and exhaustion/timeout results. Logs report processed
profile cells as a percentage, actual versus requested bounds, unique histories
and classification masks; they do not estimate remaining SMT work. The external
SMT seed is not configured and is not confused with a workload seed.

CLI `--max-error`/`--view` remain documented upstream alternatives, not features
claimed exercised by this server campaign. Views or renamed-source coverage
features are not semantic equivalence proofs. Larger targeted profiles for
causal chains or every adjacent-level separator require their own completed
bounds; no sampled absence is a negative verdict.

Atomic change: test(core): add diverse bounded apalache trace comparison

## 2. Exact state identity and the polynomial bound

The paper's Algorithm 2 is itself a memoized prefix DFS. DFS does not contradict
polynomial time at fixed session-order width k. For session lengths n_i, the
number of session-prefix cuts is at most M = product_i(n_i + 1), bounded by (n +
1)^k. Read/write phase splitting preserves width, with the analogous bound
product_i(2*n_i + 1). The paper gives O(n^(k+3)) for serializability and
width-preserving reductions for Prefix and Snapshot Isolation.

For the implementation, prove that two reachable prefixes with the same cut have
the same future legality and bookkeeping: pending readers are determined by
placed writers and unplaced readers; SI open writers by placed read phases and
unplaced write phases. For a fixed dependency DAG, the minimal unplaced frontier
determines the unplaced upward closure and hence the cut. Under those
invariants, exact memoization bounds expanded cuts by M and candidate
transitions by k*M; account separately for polynomial bookkeeping cost.

This is an obligation for the built-in solvers, not a theorem about arbitrary
providers of ConstrainedLinearizationSolver, disabled memoization or every
heuristic. Two fixed-budget restarts before a memoized exhaustive attempt do not
invalidate the cut bound. Bounding physical replicas implies bounded k only when
the session/replica relationship actually guarantees it.

Relevant files: constrained_linearization.rs, prefix.rs, serializable.rs,
snapshot_isolation.rs, and the existing fingerprint/frontier regressions in
crates/sat/tests/.

Use hashes to select buckets, not as the sole proof of state equality. Audit
seen, failed-signature, failed-frontier and jump-depth caches together. Before
changing their representation, add deliberate collision controls and compare
with the exact reference. Do not remove the semantic signature merely because a
finite capture found no same-cut/different-state pair.

A direct finite check of the real SER solver used independent preludes followed
by a two-session write-skew contradiction. Only memoization varied; other search
accelerators were disabled. All 18 measurements matched independently computed
cut-grid and interleaving counts. Selected node-entry counts:

| Sessions | Prelude transactions/session | Distinct cuts | Without memo | With memo |
| -------- | ---------------------------- | ------------- | ------------ | --------- |
| 2        | 8                            | 81            | 48,619       | 145       |
| 3        | 4                            | 150           | 289,002      | 366       |
| 4        | 2                            | 144           | 75,331       | 409       |

These are node-entry counts on a controlled family, not measured speedups or an
unbounded proof. Duplicate memo hits still enter a node before pruning.

Atomic change: fix(core): make failed-state identity collision safe

## 3. Fixed corpus, counters and heuristic ablations

The benchmark now constructs deterministic SAT and UNSAT families for all three
NPC levels, at two history shapes, and measures public core/SAT decisions on the
same inputs. Seed 42 is the default and `DBCOP_BENCH_SEED` overrides it
explicitly. Frozen Apalache cases additionally cross-check the alternative
implementations. This is an end-to-end latency baseline, not yet a heuristic
ablation.

Further measurement should cover shared-reader pressure, articulation overlap
and disjoint components, then add expanded-node, memo-hit, legality-call,
maximum-depth, allocation and memory counters only where directly measured. Keep
compilation and setup outside timing. Compare existing options one at a time; do
not add configuration fields before a demonstrated need. The present change does
not modify DFS ordering, pruning, Zobrist hashes or public traits.

Current source findings to test, not performance claims:

- Counter-moves are recorded only while Found propagates immediately to the
  caller; the per-attempt table does not then guide another search decision.
- The default three attempts try the three untried portfolio modes. Adaptive
  reselection requires further attempts; do not describe the default as a
  trained portfolio.
- Whole failed-signature caching is not generalized nogood/first-UIP learning.
- Cached jump depths are not an explanation graph establishing clause learning.
- Correct serializable.rs documentation: its theorem reference currently points
  at the Prefix reduction, and active_write tracks pending readers, not the
  inverse condition described by its comment.

Gate: all configurations agree with independent semantics. Remove or change a
heuristic only after ablation; preserve completeness and the fixed-width bound.

Atomic change: perf(core): benchmark deterministic positive and negative search

## 4. Reduce repeated work before sophisticated learning

Profile ordered_frontier, order_frontier, children_of, frontier_signature and
forward/backtrack_book_keeping in the existing engine and concrete solvers.
Candidates include cached adjacency, dense vertex indexing, incremental reader
obligations/signatures and reversible updates. Verify allocations before
claiming a collect or sort allocates: Rust can reuse an owned allocation.

Choose one measured hotspot per change. Preserve all validation and undo
invariants, use existing trait hooks and no_std collections, and require the
same corpus verdicts and TLC trace compatibility. Consider explicit DFS frames
only if long-history stack depth is demonstrated to be a problem; no stack
failure has been reproduced in this analysis.

Atomic change: perf(core): remove the measured search hotspot

## 5. Articulation-aware decomposition

The paper's Theorem 5.2 gives component decision equivalence, and Corollary 5.3
bounds time by O(B*n^(b+3)) for B biconnected components of maximum size b.
Current core and SAT paths solve disjoint components independently, but fall
back to the full history for articulation-overlap components. That preserves
correctness; it does not establish the stronger fixed-component-size bound for
that fallback. It is not a refutation of the theorem or proof of exponential
runtime on every such input.

Relevant files: consistency/decomposition.rs, consistency/mod.rs and
crates/sat/src/lib.rs. First specify semantic projection with external-writer
context, then witness composition across articulation sessions. A raw session
clone or concatenation of independently found witnesses is not enough. Compare
component and whole-history decisions and validate the merged global witness.
Keep the existing overlap fallback until both obligations are met.

Atomic change: perf(core): compose articulation component witnesses

## 6. Techniques to defer or reject

Reason-based propagation, first-UIP learning, VSIDS-style activity and LCG are
promising only after defining decision literals, implication reasons, undo scope
and entailed conflicts. Whole-state cache entries are not explanations. Preserve
the polynomial cut-state argument when considering non-prefix repair.

Partial-order reduction needs a commutativity/independence argument for these
particular operations and observations. A graph-feature coverage abstraction is
not that proof. Parallel portfolios or learned ordering come later, after a
reproducible sequential baseline.

Transfer move ordering, verified transpositions and restart guidance from chess.
Do not transfer null-move pruning, futility pruning, selective reductions,
approximate scores or racy transposition-table semantics into an UNSAT decision.
Stockfish explicitly tolerates inconsistent/racy table entries; a checker cannot
accept the corresponding false rejection risk. Alpha-beta is not a drop-in
pruner for this existential feasibility problem.

## Paper numerical verdict

The recovered post-fix campaign has 700 planned/attempted runs, ten seeds per
policy/case/level cell, 695 evaluated outcomes and 678 clean outcomes.
Independent reaggregation matched all 70 stored summary rows, including
denominator rules. This is the historical 20261016 snapshot, not a benchmark
rerun after the typed boundary refactor. Its source archive SHA-256 is
`e5016199b70a3f5951f04fe8315f6f375323bf558df809457f166fa5aca0c453`.

The uniform subset still exposes SmallBank A13 and Wikipedia A15/A16 under weak
levels, and TPC-C A1/A8 under causal consistency. This qualitatively
corroborates weak-isolation testing value. Missing A14/A17 and lower TPC-C
counts do not refute the paper: Voter's cap/population, fixtures, workload
mixes, sampling and SQL semantics differ. All ten uniform RC TPC-C runs had
workload errors, so those counts are not ten clean workload runs. A12 also
appears once under SER and is not an isolation diagnostic without excluding
DECIMAL/FLOAT arithmetic effects. The stock SmallBank Amalgamate defect needs
the separately repaired/excluded variants, not an application-specific backend
workaround.

MonkeyDB reports 100 runs/configuration, ten-second workloads, two/three
sessions and small fixtures. Its statement is nine of twelve RC TPC-C assertions
above 60 percent, not nine causal assertions above that rate. The published
plots do not supply exact underlying per-bar counts here. Its A13-A17 geometric
means must not be compared with an average of our per-assertion frequencies.

The 2019 complexity paper's database counts instead classify each inconsistent
history by its weakest violated criterion. Its Table 2 totals recompute to
546/1000 and 937/1000 for CockroachDB, and 198/250 and 233/250 for Galera across
the two workload configurations. Those are not MonkeyDB assertion frequencies.
No matched-protocol rerun or theorem refutation has been established.

## Verification gate for implementation changes

Run the existing full Rust suite, relevant real TLC/Apalache gates, actual
changed-path smoke checks and the full Go suite when affected. Keep tests
consumer-visible and deterministic; no source-text or wiring-only tests.

```bash
cargo test --release --workspace --all-features
cargo build -p dbcop_core --no-default-features
cargo clippy --workspace --all-targets --all-features -- -D warnings
cargo +nightly fmt --check --all
```

Use the real-service commands in development.md for external prerequisites. Do
not merge cache/profile data from changed sources. Report overall and per-file
coverage separately, with explicit integration skips and measurement limits.
Each numbered implementation change is a separate reviewable commit; no commit
or push is authorized by this proposal.

## Primary references

- [Biswas and Enea: On the Complexity of Checking Transactional Consistency](https://arxiv.org/html/1908.04509),
  Algorithm 2, Theorem 4.2, Corollaries 4.3/4.9/4.11, Theorem 5.2 and Corollary
  5.3.
- [MonkeyDB](https://arxiv.org/html/2103.02830v1), evaluation and experiment
  configurations.
- [Cirstea et al.: validating implementations against TLA+ specifications](https://arxiv.org/html/2404.16075v2),
  complete-trace compatibility rather than acceptance of an arbitrary matching
  prefix.
- [Apalache execution modes](https://apalache-mc.org/docs/apalache/running.html)
  and
  [counterexample enumeration with views](https://apalache-mc.org/docs/apalache/principles/enumeration.html).
- [Stockfish transposition table](https://github.com/official-stockfish/Stockfish/blob/effdc3fd3f6de6e75fe1374a51a7a7eed66f880b/src/tt.h)
  and
  [search](https://github.com/official-stockfish/Stockfish/blob/effdc3fd3f6de6e75fe1374a51a7a7eed66f880b/src/search.cpp).
- [Leela Chess Zero](https://github.com/LeelaChessZero/lc0/tree/58e780090b8ffb765743b73a794f994c3a8c9476),
  policy/value-guided search rather than exact UNSAT certification.
- [CaDiCaL](https://github.com/arminbiere/cadical/tree/c60730422e758ef1cebe7aeddf2dda31c996bf04)
  and
  [Pumpkin](https://github.com/ConSol-Lab/Pumpkin/tree/9b7fcc10ae695663e807701815ff9bf17c89ef87),
  explanation-based exact search.
- [GenMC](https://plv.mpi-sws.org/genmc/),
  model-checking/partial-order-reduction precedent, not a proof that a reduction
  transfers here.
- [Graph Backtracking](https://arxiv.org/html/2605.28220), non-prefix repair
  requiring new invariants before use in this engine.

Research used both primary sources and actual Treg searches, including the
registered Jina tool and a zero-cost publication-router query. No performance
claim follows from the literature alone, and no upstream implementation code is
copied by this plan.
