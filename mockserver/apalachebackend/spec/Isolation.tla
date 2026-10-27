---------------------------- MODULE Isolation ----------------------------
EXTENDS Integers, Sequences

(***************************************************************************
Input h is a flat sequence of records:
  [session: Int, height: Int, committed: Bool,
   events: Seq([kind: Int, key: Int, initial: Bool, version: Int])].
kind 0 is read; kind 1 is write. The initial flag distinguishes Read(None)
from Read(Some(0)); writes always have initial = FALSE. Transaction IDs are
1..Len(h); 0 is the implicit initial writer, not an element of h.

The semantic predicates below follow Figure 2, Table 1 and Section 2.1 of
Biswas and Enea, https://arxiv.org/html/1908.04509. RR is the project's
additional RC plus repeatable-external-sources level. The 2024 trace paper
supplies a validation method, not these isolation axioms.

This module assumes the record/sequence types above. WellFormed checks raw
history validity, separately from existence of a valid commit order. In
particular, cycles in SO union WR are inconsistent, not structurally invalid.
One ordered history session represents one replica. Fixing replica count k
therefore fixes session count here; this is not a general replica/client
mapping, nor a complexity claim about an implementation or finite campaign.
***************************************************************************)

\* @typeAlias: history = Seq({session: Int, height: Int, committed: Bool, events: Seq({kind: Int, key: Int, initial: Bool, version: Int})});
Isolation_typedefs == TRUE

(* DOMAIN avoids Apalache's unsupported symbolic integer-range bounds. *)
\* @type: ($history) => Set(Int);
Ids(h) == {0} \cup DOMAIN h

\* @type: ($history) => Set(<<Int, Int>>);
Sites(h) ==
    UNION { {<<t, e>> : e \in DOMAIN h[t].events} : t \in DOMAIN h }

\* @type: ($history, <<Int, Int>>) => {kind: Int, key: Int, initial: Bool, version: Int};
Event(h, site) == h[site[1]].events[site[2]]

\* @type: ($history) => Set(<<Int, Int>>);
WriteSites(h) == {s \in Sites(h) : Event(h, s).kind = 1}

\* @type: ($history) => Set(Int);
Keys(h) == {Event(h, s).key : s \in Sites(h)}

\* ws is the exact raw write-site projection; online callers validate it below.
\* @type: ($history, Set(<<Int, Int>>), Int) => Set(Int);
WritesFromSites(h, ws, t) ==
    IF t = 0 THEN Keys(h)
    ELSE {Event(h, s).key : s \in {w \in ws : w[1] = t}}

\* @type: ($history, Set(<<Int, Int>>), Int, Int) => Set(Int);
PrecedingWritesFromWrites(h, ws, t, e) ==
    {s[2] : s \in {w \in ws :
        /\ w[1] = t
        /\ w[2] < e
        /\ Event(h, w).key = h[t].events[e].key}}

\* @type: ($history, Set(<<Int, Int>>), Int, Int) => Bool;
ExternalFromWrites(h, ws, t, e) ==
    /\ h[t].events[e].kind = 0
    /\ PrecedingWritesFromWrites(h, ws, t, e) = {}

\* @type: ($history, Set(<<Int, Int>>), Int, Int) => Set(<<Int, Int>>);
MatchingWritesFromWrites(h, ws, t, e) ==
    {s \in ws :
        /\ Event(h, s).key = h[t].events[e].key
        /\ Event(h, s).version = h[t].events[e].version}

(* Every CHOOSE is guarded; invalid missing sources never select a site. *)
\* @type: ($history, Set(<<Int, Int>>), Int, Int) => Int;
SourceFromWrites(h, ws, t, e) ==
    IF PrecedingWritesFromWrites(h, ws, t, e) # {} THEN t
    ELSE IF h[t].events[e].initial THEN 0
    ELSE LET matches == MatchingWritesFromWrites(h, ws, t, e)
         IN IF matches = {} THEN 0
            ELSE (CHOOSE s \in matches : TRUE)[1]

\* @type: ($history, Set(<<Int, Int>>), Int, Int) => Bool;
LastWriteFromWrites(h, ws, t, e) ==
    ~\E s \in ws :
        /\ s[1] = t
        /\ s[2] > e
        /\ Event(h, s).key = h[t].events[e].key

\* @type: ($history, Set(<<Int, Int>>), Int, Int) => Bool;
ValidReadFromWrites(h, ws, t, e) ==
    LET read == h[t].events[e]
        preceding == PrecedingWritesFromWrites(h, ws, t, e)
        matches == MatchingWritesFromWrites(h, ws, t, e)
    IN IF preceding # {} THEN
           LET last == CHOOSE i \in preceding :
                           \A j \in preceding : j <= i
           IN /\ ~read.initial
              /\ read.version = h[t].events[last].version
       ELSE IF read.initial THEN TRUE
       ELSE IF matches = {} THEN read.version = 0
       ELSE
           (* Check every matching site even on duplicate-write input. *)
           \A s \in matches :
               /\ s[1] # t
               /\ h[s[1]].committed
               /\ LastWriteFromWrites(h, ws, s[1], s[2])

\* @type: ($history, Set(<<Int, Int>>)) => Bool;
WellFormedFromWrites(h, ws) ==
    /\ \A t \in DOMAIN h : h[t].committed
    /\ \A t, u \in DOMAIN h :
           (h[t].session = h[u].session /\ h[t].height = h[u].height)
               => t = u
    /\ \A s \in Sites(h) : Event(h, s).kind \in 0..1
    /\ \A s \in ws : ~Event(h, s).initial
    /\ \A s, r \in ws :
           (Event(h, s).key = Event(h, r).key /\
            Event(h, s).version = Event(h, r).version) => s = r
    /\ \A s \in Sites(h) :
           IF Event(h, s).kind = 0 THEN ValidReadFromWrites(h, ws, s[1], s[2])
           ELSE TRUE

\* @type: ($history) => Set(<<Int, Int>>);
SessionOrder(h) ==
    {<<0, t>> : t \in DOMAIN h} \cup
    {p \in (DOMAIN h) \X (DOMAIN h) :
        /\ h[p[1]].session = h[p[2]].session
        /\ h[p[1]].height < h[p[2]].height}

\* @type: ($history, Set(<<Int, Int>>)) => Set(<<Int, Int>>);
ExternalSitesFromWrites(h, ws) ==
    {s \in Sites(h) : ExternalFromWrites(h, ws, s[1], s[2])}

\* @type: ($history, Set(<<Int, Int>>)) => Set({txn: Int, position: Int, key: Int, source: Int});
ReadFacts(h, ws) ==
    {[txn |-> s[1], position |-> s[2], key |-> Event(h, s).key,
      source |-> SourceFromWrites(h, ws, s[1], s[2])] :
        s \in ExternalSitesFromWrites(h, ws)}

\* @type: ($history, Set(<<Int, Int>>)) => Set(<<Int, Int>>);
WriterFacts(h, ws) ==
    ({0} \X Keys(h)) \cup {<<s[1], Event(h, s).key>> : s \in ws}

\* @type: ($history, Set({txn: Int, position: Int, key: Int, source: Int})) => Set(<<Int, Int>>);
EdgesFromFacts(h, reads) ==
    SessionOrder(h) \cup {<<r.source, r.txn>> : r \in reads}

(* Start with Edges(h), then unroll once per middle in Ids(h), including 0.
   This is transitive, not reflexive, closure. No client computes the edges
   or the closure. ValidOrder's reach argument must be that unrolled result. *)
\* @type: (Set(Int), Set(<<Int, Int>>), Int) => Set(<<Int, Int>>);
ReachStep(ids, rel, middle) ==
    rel \cup {p \in ids \X ids :
                 <<p[1], middle>> \in rel /\ <<middle, p[2]>> \in rel}

\* @type: (Set({txn: Int, position: Int, key: Int, source: Int})) => Bool;
RepeatableFromFacts(reads) ==
    \A s, r \in reads :
        (s.txn = r.txn /\ s.key = r.key) => s.source = r.source

(* Figure 2(a): the earlier external read may be of ANY variable. *)
\* @type: (Set({txn: Int, position: Int, key: Int, source: Int}), Int, Int, Int) => Bool;
EarlierReadFromFacts(reads, c, e, b) ==
    \E r \in reads : r.txn = c /\ r.position < e /\ r.source = b

(* Figure 2(d): co* ; (SO union WR), where co* is rank <=. *)
\* @type: ($history, Set(<<Int, Int>>), Int -> Int, Int, Int) => Bool;
PrefixPremiseFromEdges(h, edges, rank, b, c) ==
    \E q \in Ids(h) :
        rank[b] <= rank[q] /\ <<q, c>> \in edges

(* Figure 2(e): co* ; conflict ; co, with conflict a shared write key.
   c is an external reader, hence non-root; enumerate its actual write facts,
   not the root's all-key arena behind a symbolic transaction lookup. *)
\* @type: ($history, Set(<<Int, Int>>), Int -> Int, Int, Int) => Bool;
ConflictPremiseFromFacts(h, writers, rank, b, c) ==
    \E d \in Ids(h) :
        /\ rank[b] <= rank[d]
        /\ rank[d] < rank[c]
        /\ \E w \in writers : w[1] = c /\ <<d, w[2]>> \in writers

(* Figure 2 and Table 1: 0 RC, 1 RR, 2 RA, 3 CC, 4 PC, 5 SI, 6 SER. *)
\* @type: ($history, Set({txn: Int, position: Int, key: Int, source: Int}), Set(<<Int, Int>>), Set(<<Int, Int>>), Int, Int -> Int, Set(<<Int, Int>>), Int, Int, Int) => Bool;
PremiseFromFacts(h, reads, writers, edges, level, rank, reach, b, c, e) ==
    CASE level \in 0..1 -> EarlierReadFromFacts(reads, c, e, b)
      [] level = 2 -> <<b, c>> \in edges
      [] level = 3 -> <<b, c>> \in reach
      [] level = 4 -> PrefixPremiseFromEdges(h, edges, rank, b, c)
      [] level = 5 -> PrefixPremiseFromEdges(h, edges, rank, b, c) \/
                      ConflictPremiseFromFacts(h, writers, rank, b, c)
      [] level = 6 -> rank[b] < rank[c]
      [] OTHER -> FALSE

\* Facts must be the exact TLA-derived ReadFacts/WriterFacts/EdgesFromFacts.
\* @type: ($history, Set(<<Int, Int>>), Set({txn: Int, position: Int, key: Int, source: Int}), Set(<<Int, Int>>), Set(<<Int, Int>>), Int, Int -> Int, Set(<<Int, Int>>)) => Bool;
ValidOrderFromFacts(h, ws, reads, writers, edges, level, rank, reach) ==
    IF ~WellFormedFromWrites(h, ws) THEN FALSE
    ELSE IF level \notin 0..6 THEN FALSE
    ELSE IF rank \notin [Ids(h) -> Ids(h)] THEN FALSE
    ELSE
        /\ rank[0] = 0
        /\ \A a, b \in Ids(h) : rank[a] = rank[b] => a = b
        /\ \A p \in edges : rank[p[1]] < rank[p[2]]
        /\ IF level = 0 THEN TRUE ELSE RepeatableFromFacts(reads)
        \* Concrete transaction/event indices avoid record-set joins in the SMT rewriter.
        /\ \A b \in Ids(h) :
               \A c \in DOMAIN h :
                   LET enforce ==
                       \A e \in DOMAIN h[c].events :
                           IF ExternalFromWrites(h, ws, c, e) THEN
                               LET a == SourceFromWrites(h, ws, c, e)
                               IN (b # a /\ h[c].events[e].key \in WritesFromSites(h, ws, b) /\
                                   IF level \in 0..1 THEN EarlierReadFromFacts(reads, c, e, b) ELSE TRUE) =>
                                       rank[b] < rank[a]
                           ELSE TRUE
                   IN IF level \in 0..1 THEN enforce
                      ELSE PremiseFromFacts(h, reads, writers, edges, level, rank, reach, b, c, 0) => enforce

\* These entry points derive their projection in TLA+, including for TLC.
\* @type: ($history, Int) => Set(Int);
Writes(h, t) == WritesFromSites(h, WriteSites(h), t)
\* @type: ($history, Int, Int) => Int;
Source(h, t, e) == SourceFromWrites(h, WriteSites(h), t, e)
\* @type: ($history) => Bool;
WellFormed(h) == WellFormedFromWrites(h, WriteSites(h))
\* @type: ($history) => Set(<<Int, Int>>);
ExternalSites(h) == ExternalSitesFromWrites(h, WriteSites(h))
\* @type: ($history) => Set(<<Int, Int>>);
Edges(h) == EdgesFromFacts(h, ReadFacts(h, WriteSites(h)))
\* @type: ($history) => Bool;
Repeatable(h) == RepeatableFromFacts(ReadFacts(h, WriteSites(h)))
\* @type: ($history, Int, Int -> Int, Set(<<Int, Int>>)) => Bool;
ValidOrder(h, level, rank, reach) ==
    LET ws == WriteSites(h)
        reads == ReadFacts(h, ws)
        writers == WriterFacts(h, ws)
    IN ValidOrderFromFacts(h, ws, reads, writers, EdgesFromFacts(h, reads),
                          level, rank, reach)

\* @type: (Seq({txn: Int, position: Int, event: {kind: Int, key: Int, initial: Bool, version: Int}})) => Set(<<Int, Int>>);
ProjectionSites(p) == {<<p[i].txn, p[i].position>> : i \in DOMAIN p}

\* Validate the syntax-only online projection before any semantic use.
\* @type: ($history, Seq({txn: Int, position: Int, event: {kind: Int, key: Int, initial: Bool, version: Int}})) => Bool;
ValidWriteProjection(h, p) ==
    /\ \A i \in DOMAIN p :
           IF p[i].txn \notin DOMAIN h THEN FALSE
           ELSE IF p[i].position \notin DOMAIN h[p[i].txn].events THEN FALSE
           ELSE /\ p[i].event = h[p[i].txn].events[p[i].position]
                /\ p[i].event.kind = 1
    /\ \A i, j \in DOMAIN p :
           (p[i].txn = p[j].txn /\ p[i].position = p[j].position) => i = j
    /\ \A s \in Sites(h) :
           Event(h, s).kind = 1 =>
               \E i \in DOMAIN p : p[i].txn = s[1] /\ p[i].position = s[2]

=============================================================================
