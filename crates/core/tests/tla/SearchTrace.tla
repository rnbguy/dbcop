-------------------------- MODULE SearchTrace --------------------------
EXTENDS CaseData, FiniteSets, TLC

(***************************************************************************
Cirstea, Kuppe, Loillier and Merz, 2404.16075v2, sections 4.1/4.2:
compatibility is existence of ONE behavior consuming the complete observation
sequence and its terminal result. Dead ends on other matching branches are
not rejection. Report emits a match only at that accepting state; neither
deadlocks, universal liveness, nor diameter are used as acceptance criteria.

Only PickCandidate is unobserved. It speculates before the next CandidateTrial
is matched, so a wrong speculative branch really dead-ends. It cannot change
bookkeeping. Every forward/undo phase requires its own recorded observation.
The graph below validates engine mechanics, not the isolation axioms.
***************************************************************************)

V == 0..(Len(Vertices) - 1)
Info(v) == Vertices[v + 1]
AsSet(s) == {s[i] : i \in DOMAIN s}
DropLast(s) == SubSeq(s, 1, Len(s) - 1)
Unique(s) == Cardinality(AsSet(s)) = Len(s)
TxnSet == {Info(v).txn : v \in V}
ExternalReads == ExternalSites(History)
ReadSites(t) == {s \in ExternalReads : s[1] = t}
ReadPlaced(p, t) == \E v \in AsSet(p) :
    Info(v).txn = t /\ Info(v).phase \in {0, 2}
WritePlaced(p, t) == \E v \in AsSet(p) :
    Info(v).txn = t /\ Info(v).phase \in {1, 2}
CommitIndices(p, x) == {i \in DOMAIN p :
    Info(p[i]).phase \in {1, 2} /\ x \in Writes(History, Info(p[i]).txn)}
LastWriter(p, x) ==
    LET indices == CommitIndices(p, x)
    IN IF indices = {} THEN 0
       ELSE Info(p[CHOOSE i \in indices : \A j \in indices : j <= i]).txn
Pending(p, x) == {s[1] : s \in {r \in ExternalReads :
    /\ Event(History, r).key = x
    /\ ~ReadPlaced(p, r[1])
    /\ Source(History, r[1], r[2]) = LastWriter(p, x)}}
Active(p) == UNION {{<<x, t>> : t \in Pending(p, x)} : x \in Keys(History)}
ActiveKeys(p) == {x \in Keys(History) : Pending(p, x) # {}}
Open(p) == IF Level = 5 THEN UNION {Writes(History, t) :
    t \in {u \in TxnSet : ReadPlaced(p, u) /\ ~WritePlaced(p, u)}} ELSE {}

ParentCounts(p) == [i \in 1..Len(Vertices) |->
    Cardinality({edge \in Graph :
        edge[2] = i - 1 /\ edge[1] \notin AsSet(p)})]
Ready(p) == {v \in V \ AsSet(p) :
    \A edge \in Graph : edge[2] = v => edge[1] \in AsSet(p)}
Legal(p, v) ==
    LET t == Info(v).txn
        phase == Info(v).phase
    IN /\ IF phase \in {1, 2}
          THEN \A x \in Writes(History, t) : Pending(p, x) \ {t} = {}
          ELSE TRUE
       /\ IF Level = 5 /\ phase = 0
          THEN Writes(History, t) \cap Open(p) = {}
          ELSE TRUE

(* Raw semantic dependencies, deliberately NOT the supplied engine graph. *)
RawReady(p, v) ==
    LET t == Info(v).txn
        phase == Info(v).phase
    IN /\ IF phase = 1 THEN ReadPlaced(p, t) ELSE TRUE
       /\ IF phase \in {0, 2} THEN
             /\ \A edge \in Edges(History) :
                    (edge[2] = t /\ edge[1] # 0 /\ edge[1] \in TxnSet)
                        => WritePlaced(p, edge[1])
             /\ \A s \in ReadSites(t) :
                    Source(History, t, s[2]) = LastWriter(p, Event(History, s).key)
          ELSE TRUE

CommitBefore(p, a, b) == \E i, j \in DOMAIN p :
    /\ i < j
    /\ Info(p[i]).txn = a /\ Info(p[i]).phase \in {1, 2}
    /\ Info(p[j]).txn = b /\ Info(p[j]).phase \in {1, 2}
Rank(order) == [t \in Ids(History) |->
    IF t = 0 THEN 0 ELSE CHOOSE i \in DOMAIN order : order[i] = t]
RECURSIVE OrderExists(_, _)
OrderExists(order, p) ==
    IF Len(order) = Len(History)
    THEN ValidOrder(History, Level, Rank(order), Reach)
    ELSE \E t \in (DOMAIN History) \ AsSet(order) :
        /\ \A edge \in Edges(History) :
               edge[2] = t => (edge[1] = 0 \/ edge[1] \in AsSet(order))
        /\ \A u \in (DOMAIN History) \ AsSet(order) : ~CommitBefore(p, u, t)
        /\ OrderExists(Append(order, t), p)

(* Complete, finite continuation search. Prunes are not justified by a cache
   signature or by repeating the observed Rust exploration. Empty raw
   transactions omitted by preprocessing are independently inserted by
   OrderExists; the canonical ValidOrder predicate remains the final gate. *)
RECURSIVE HasCompletion(_)
HasCompletion(p) ==
    IF Len(p) = Cardinality(V) THEN OrderExists(<<>>, p)
    ELSE \E v \in V \ AsSet(p) :
        /\ RawReady(p, v) /\ Legal(p, v)
        /\ HasCompletion(Append(p, v))

AttemptDepth(i) ==
    Cardinality({j \in DOMAIN Observations : j < i /\ Observations[j].tag = 0})
    - Cardinality({j \in DOMAIN Observations : j < i /\ Observations[j].tag = 11})
ObservationShape ==
    /\ Len(Observations) > 0
    /\ Cardinality({i \in DOMAIN Observations : Observations[i].tag = 12}) = 1
    /\ Observations[Len(Observations)].tag = 12
    /\ \A i \in DOMAIN Observations :
           /\ Observations[i].tag \in 0..12
           /\ AttemptDepth(i) =
               (IF Observations[i].tag \in {0, 12} THEN 0 ELSE 1)

CaptureShape ==
    /\ Level \in 4..6
    /\ WellFormed(History) /\ Repeatable(History)
    /\ \A v \in V :
           /\ Info(v).txn \in DOMAIN History
           /\ Info(v).phase \in (IF Level = 6 THEN {2} ELSE {0, 1})
    /\ \A v, w \in V : Info(v) = Info(w) => v = w
    /\ \A t \in DOMAIN History :
           Len(History[t].events) # 0 =>
               \A phase \in (IF Level = 6 THEN {2} ELSE {0, 1}) :
                   \E v \in V : Info(v) = [txn |-> t, phase |-> phase]
    /\ Graph \subseteq V \X V
    /\ ObservationShape

VARIABLES cursor, placed, stack, mode, hidden, attempt, finalAttempt,
          tainted, ended, lastFound, terminal, returned
vars == <<cursor, placed, stack, mode, hidden, attempt, finalAttempt,
          tainted, ended, lastFound, terminal, returned>>

(* Modes: 0 outside attempt, 1 node entry, 2 node ready, 3 candidates,
   4 before forward, 5 after forward, 6 returning, 7 after undo. *)
Init ==
    /\ cursor = 1 /\ placed = <<>> /\ stack = <<>> /\ mode = 0
    /\ hidden = -1 /\ attempt = -1 /\ finalAttempt = FALSE
    /\ tainted = FALSE /\ ended = FALSE /\ lastFound = FALSE
    /\ terminal = FALSE /\ returned = FALSE

Current == Observations[cursor]
Advance == cursor' = cursor + 1
Meta(o) == /\ o.attempt = attempt /\ o.final_attempt = finalAttempt
           /\ o.budget_hit = tainted
State(o, p) ==
    /\ o.path = p
    /\ Unique(o.active) /\ AsSet(o.active) = Active(p)
    /\ Unique(o.active_keys) /\ AsSet(o.active_keys) = ActiveKeys(p)
    /\ Unique(o.open) /\ AsSet(o.open) = Open(p)
Mechanics(o) ==
    /\ Unique(o.frontier) /\ AsSet(o.frontier) = Ready(placed)
    /\ o.parents = ParentCounts(placed)
Top == stack[Len(stack)]
ReplaceTop(frame) == [stack EXCEPT ![Len(stack)] = frame]
SemanticFailure == tainted \/ ~HasCompletion(placed)

Start(o) ==
    /\ o.tag = 0 /\ mode = 0 /\ ~terminal
    /\ IF attempt = -1 THEN o.attempt = 0
       ELSE /\ ended /\ ~lastFound /\ tainted /\ ~finalAttempt
            /\ o.attempt = attempt + 1
    /\ ~o.budget_hit /\ State(o, <<>>)
    /\ attempt' = o.attempt /\ finalAttempt' = o.final_attempt
    /\ placed' = <<>> /\ stack' = <<>> /\ mode' = 1
    /\ hidden' = -1 /\ tainted' = FALSE /\ ended' = FALSE
    /\ lastFound' = FALSE /\ returned' = FALSE
    /\ UNCHANGED terminal /\ Advance

Node(o) ==
    /\ o.tag = 1 /\ mode = 1 /\ Meta(o)
    /\ State(o, placed) /\ Mechanics(o)
    /\ stack' = Append(stack, [prefix |-> placed, active |-> Active(placed),
           keys |-> ActiveKeys(placed), open |-> Open(placed), remaining |-> {}])
    /\ mode' = 2 /\ Advance
    /\ UNCHANGED <<placed, hidden, attempt, finalAttempt, tainted,
                   ended, lastFound, terminal, returned>>

Cache(o) ==
    /\ o.tag = 2 /\ mode = 2 /\ Meta(o)
    /\ State(o, placed) /\ Mechanics(o) /\ Ready(placed) # {}
    /\ Unique([i \in DOMAIN o.candidates |-> o.candidates[i][1]])
    /\ {o.candidates[i][1] : i \in DOMAIN o.candidates} = Ready(placed)
    /\ \A i \in DOMAIN o.candidates :
           o.candidates[i][2] = Legal(placed, o.candidates[i][1])
    /\ stack' = ReplaceTop([Top EXCEPT !.remaining = Ready(placed)])
    /\ mode' = 3 /\ returned' = FALSE /\ Advance
    /\ UNCHANGED <<placed, hidden, attempt, finalAttempt, tainted,
                   ended, lastFound, terminal>>

PickCandidate ==
    /\ mode = 3 /\ hidden = -1 /\ Top.remaining # {}
    /\ hidden' \in Top.remaining
    /\ UNCHANGED <<cursor, placed, stack, mode, attempt, finalAttempt,
                   tainted, ended, lastFound, terminal, returned>>

Trial(o) ==
    /\ o.tag = 3 /\ mode = 3 /\ hidden # -1 /\ Meta(o)
    /\ State(o, placed) /\ Mechanics(o)
    /\ o.selected = hidden
    /\ o.candidates = <<<<hidden, Legal(placed, hidden)>>>>
    /\ stack' = ReplaceTop([Top EXCEPT !.remaining = @ \ {hidden}])
    /\ mode' = IF Legal(placed, hidden) THEN 4 ELSE 3
    /\ hidden' = IF Legal(placed, hidden) THEN hidden ELSE -1
    /\ returned' = FALSE /\ Advance
    /\ UNCHANGED <<placed, attempt, finalAttempt, tainted, ended,
                   lastFound, terminal>>

BeforeForward(o) ==
    /\ o.tag = 4 /\ mode = 4 /\ Meta(o)
    /\ o.selected = hidden
    /\ State([o EXCEPT !.path = placed], placed)
    /\ o.path = Append(placed, hidden)
    /\ RawReady(placed, hidden)
    /\ mode' = 5 /\ Advance
    /\ UNCHANGED <<placed, stack, hidden, attempt, finalAttempt, tainted,
                   ended, lastFound, terminal, returned>>

AfterForward(o) ==
    /\ o.tag = 5 /\ mode = 5 /\ Meta(o)
    /\ o.selected = hidden /\ State(o, Append(placed, hidden))
    /\ placed' = Append(placed, hidden) /\ hidden' = -1 /\ mode' = 1
    /\ Advance
    /\ UNCHANGED <<stack, attempt, finalAttempt, tainted, ended,
                   lastFound, terminal, returned>>

Prune(o) ==
    /\ o.tag = 8 /\ mode = 2
    /\ o.attempt = attempt /\ o.final_attempt = finalAttempt
    /\ State(o, placed) /\ o.reason \in 1..5
    /\ IF o.reason = 1
       THEN /\ o.budget_hit /\ ~finalAttempt
            /\ tainted' = TRUE
       ELSE /\ Meta(o) /\ SemanticFailure /\ UNCHANGED tainted
    /\ mode' = 6 /\ returned' = FALSE /\ Advance
    /\ UNCHANGED <<placed, stack, hidden, attempt, finalAttempt,
                   ended, lastFound, terminal>>

Learn(o) ==
    /\ o.tag = 9 /\ mode = 3 /\ hidden = -1 /\ Top.remaining = {}
    /\ Meta(o) /\ State(o, placed)
    /\ IF tainted THEN o.reason = 1
       ELSE o.reason = 6 /\ ~HasCompletion(placed)
    /\ mode' = 6 /\ returned' = FALSE /\ Advance
    /\ UNCHANGED <<placed, stack, hidden, attempt, finalAttempt,
                   tainted, ended, lastFound, terminal>>

BeforeUndo(o) ==
    /\ o.tag = 6 /\ mode = 6 /\ Len(placed) > 0 /\ Meta(o)
    /\ State(o, placed) /\ o.selected = placed[Len(placed)]
    /\ mode' = 7 /\ Advance
    /\ UNCHANGED <<placed, stack, hidden, attempt, finalAttempt,
                   tainted, ended, lastFound, terminal, returned>>

AfterUndo(o) ==
    /\ o.tag = 7 /\ mode = 7 /\ Meta(o)
    /\ o.path = placed /\ o.selected = placed[Len(placed)]
    /\ State([o EXCEPT !.path = DropLast(placed)], DropLast(placed))
    /\ LET restored == stack[Len(stack) - 1]
       IN /\ restored.prefix = DropLast(placed)
          /\ AsSet(o.active) = restored.active
          /\ AsSet(o.active_keys) = restored.keys
          /\ AsSet(o.open) = restored.open
    /\ placed' = DropLast(placed) /\ stack' = DropLast(stack)
    /\ mode' = 3 /\ returned' = TRUE /\ hidden' = -1 /\ Advance
    /\ UNCHANGED <<attempt, finalAttempt, tainted, ended, lastFound, terminal>>

Backjump(o) ==
    /\ o.tag = 10 /\ mode = 3 /\ returned /\ Meta(o)
    /\ State(o, placed) /\ o.jump_depth < Len(placed)
    /\ SemanticFailure /\ mode' = 6 /\ returned' = FALSE /\ Advance
    /\ UNCHANGED <<placed, stack, hidden, attempt, finalAttempt,
                   tainted, ended, lastFound, terminal>>

End(o) ==
    /\ o.tag = 11 /\ ~ended /\ Meta(o) /\ State(o, placed)
    /\ IF o.found
       THEN /\ mode = 2 /\ Len(placed) = Cardinality(V)
            /\ OrderExists(<<>>, placed)
       ELSE /\ mode = 6 /\ placed = <<>>
            /\ tainted \/ ~HasCompletion(<<>>)
    /\ mode' = 0 /\ ended' = TRUE /\ lastFound' = o.found
    /\ stack' = <<>> /\ hidden' = -1 /\ returned' = FALSE /\ Advance
    /\ UNCHANGED <<placed, attempt, finalAttempt, tainted, terminal>>

Terminal(o) ==
    /\ o.tag = 12 /\ mode = 0 /\ ended /\ ~terminal
    /\ Meta(o) /\ o.found = lastFound /\ State(o, placed)
    /\ IF o.found THEN Len(placed) = Cardinality(V)
       ELSE ~tainted /\ ~HasCompletion(<<>>)
    /\ cursor = Len(Observations)
    /\ terminal' = TRUE /\ Advance
    /\ UNCHANGED <<placed, stack, mode, hidden, attempt, finalAttempt,
                   tainted, ended, lastFound, returned>>

Consume ==
    /\ CaptureShape /\ cursor \in DOMAIN Observations /\ ~terminal
    /\ LET o == Current
       IN Start(o) \/ Node(o) \/ Cache(o) \/ Trial(o) \/ BeforeForward(o)
          \/ AfterForward(o) \/ Prune(o) \/ Learn(o) \/ BeforeUndo(o)
          \/ AfterUndo(o) \/ Backjump(o) \/ End(o) \/ Terminal(o)
Next == Consume \/ (CaptureShape /\ cursor \in DOMAIN Observations /\ PickCandidate)

Report ==
    /\ IF cursor = 1 THEN PrintT("DBCOP_TLC_INPUT|0|1") ELSE TRUE
    /\ IF cursor = 1 /\ ~CaptureShape THEN PrintT("DBCOP_TLC_INVALID|1") ELSE TRUE
    /\ IF terminal /\ cursor = Len(Observations) + 1
       THEN PrintT("DBCOP_TLC_MATCH|1|" \o ToString(Level)) ELSE TRUE
=============================================================================
