---------------------------- MODULE OracleStep ----------------------------
EXTENDS RequestData

VARIABLES
    \* @type: $history;
    observed,
    \* @type: Seq({kind: Int, key: Int, initial: Bool, version: Int});
    staged,
    \* @type: Int -> Int;
    ranking,
    \* @type: {version: Int, initial: Bool, local: Bool};
    selected,
    \* @type: Int;
    operation

\* @type: {version: Int, initial: Bool, local: Bool};
NoSource == [version |-> 0, initial |-> FALSE, local |-> FALSE]

Init ==
    /\ observed = Committed
    /\ staged = Live
    /\ ranking = [t \in {0} |-> 0]
    /\ selected = NoSource
    /\ operation = -1
    /\ ClosureInit

\* @type: Set(Int);
LocalWrites == {i \in DOMAIN Live : Live[i].kind = 1 /\ Live[i].key = ReadKey}

\* @type: ({version: Int, initial: Bool, local: Bool}) => Bool;
LatestLocal(s) ==
    /\ s.local /\ ~s.initial
    /\ \E i \in LocalWrites :
           /\ Live[i].version = s.version
           /\ \A j \in LocalWrites : j <= i


\* Keep assignments outside action branches so the explorer exposes one TakeStep.
Next == TakeStep::
    /\ operation' = RequestKind
    /\ selected' \in
           IF RequestKind = 0 THEN {Candidates[i] : i \in DOMAIN Candidates}
           ELSE {NoSource}
    /\ observed' =
           IF RequestKind = 2 \/ (RequestKind = 0 /\ ~selected'.local)
           THEN OrderHistory(selected') ELSE observed
    /\ staged' = IF RequestKind = 1 THEN Append(staged, ProposedWrite) ELSE staged
    /\ ranking' \in [Ids(OrderHistory(selected')) -> Ids(OrderHistory(selected'))]
    /\ ClosureStep(IF RequestKind = 2 \/ (RequestKind = 0 /\ ~selected'.local)
                   THEN OrderHistory(selected') ELSE <<>>,
                   IF RequestKind = 2 \/ (RequestKind = 0 /\ ~selected'.local)
                   THEN ProjectionSites(ProjectedWrites) ELSE {})
    /\ IF RequestKind = 2 \/ (RequestKind = 0 /\ ~selected'.local)
       THEN /\ IF RequestKind = 0 THEN LocalWrites = {} ELSE TRUE
            /\ ValidWriteProjection(OrderHistory(selected'), ProjectedWrites)
            /\ ValidOrderFromFacts(OrderHistory(selected'), ProjectionSites(ProjectedWrites),
                                   ClosureReads, ClosureWriters, ClosureEdges,
                                   Level, ranking', ClosureReach)
       ELSE IF RequestKind = 0 THEN LatestLocal(selected') ELSE RequestKind = 1

\* Auxiliary rankings, derived facts and causal-closure cells are never observations.
Result == [kind |-> operation, version |-> selected.version,
           initial |-> selected.initial, local |-> selected.local]
=============================================================================
