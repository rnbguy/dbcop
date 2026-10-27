-------------------------- MODULE ReplicaTraces --------------------------
EXTENDS Integers, Sequences, FiniteSets, Apalache, TraceData

(***************************************************************************
One ordered session per replica, keys 0/1, no aborts, and no isolation filter.
This is ReplicaHistories' operational generator without TLC output effects.
TraceData supplies nonnegative integer literals ReplicaCount, MaxTransactions,
MaxEvents, MaxDepth and TargetPattern. No sequence initializers are needed.

Internal transaction IDs are allocated at Begin and never reused. Zero is a
permanent empty dummy, not a transaction or a write version. Transaction/event
storage, latest-write caches and visibility are fixed-domain functions: nested
Append/EXCEPT sequences otherwise accumulate growing symbolic capacities at
every explorer step. Only Result constructs sequences, using the official
Apalache module's constant-capacity MkSeq/FunAsSeq and symbolic SubSeq lengths.
That module also supplies pure TLA+ definitions for TLC; no recursive output
formatting or printing is involved. Result's sessions are ITF sequences.

Unrestricted behavior takes MaxDepth operational/padding steps after Init.
Targeted behavior includes two commuting Begin steps in Init, counted toward
MaxDepth, and takes MaxDepth - 2 further steps. The last step leaves every
transaction committed. At unrestricted depth zero Init has the empty history,
including every empty session. Empty transactions retain Begin/Commit.

Completeness means all distinct Result histories reachable at a completed
state within these transaction/event/depth bounds, not all schedules and not
all axiomatic histories. Idle is permitted only when every replica is
inactive. Every shorter completed behavior can therefore be padded to exactly
MaxDepth without changing history, event IDs, or availability. Conversely,
removing idle steps leaves a legal completed operational behavior. No final
all-delivered restriction is needed because visibility is not an observation.

Result omits allocation IDs, action choices, visibility and schedule state.
Enumerating its models projects away schedules; it does not enumerate them.
An interrupted enumeration is not exhaustive, even though this model is finite.
***************************************************************************)

\* @typeAlias: traceEvent = {kind: Int, key: Int, initial: Bool, version: Int};
\* @typeAlias: traceSource = {initial: Bool, version: Int};
ReplicaTraces_typedefs == TRUE

VARIABLES
    \* @type: Int -> Int;
    active,
    \* @type: Int -> Int;
    sessionCount,
    \* @type: Int -> (Int -> Int);
    sessionTxn,
    \* @type: Int -> Bool;
    committed,
    \* @type: Int -> (Int -> Bool);
    visible,
    \* @type: Int -> (Int -> Int);
    lastWrite,
    \* @type: Int -> Int;
    eventLength,
    \* @type: Int -> (Int -> $traceEvent);
    events,
    \* @type: Int;
    startedCount,
    \* @type: Int;
    eventCount,
    \* @type: Int;
    step,
    \* @type: Int;
    operation,
    \* @type: Int;
    replica,
    \* @type: Int;
    key,
    \* @type: $traceSource;
    source,
    \* @type: Int;
    writer

vars == <<active, sessionCount, sessionTxn, committed, visible, lastWrite,
          eventLength, events, startedCount, eventCount, step,
          operation, replica, key, source, writer>>

Replicas == 1..ReplicaCount
Transactions == 1..MaxTransactions
StorageTransactions == 0..MaxTransactions
EventSlots == 1..MaxEvents
Keys == 0..1

\* @type: $traceSource;
RootSource == [initial |-> TRUE, version |-> 0]

\* @type: $traceEvent;
EmptyEvent == [kind |-> 0, key |-> 0, initial |-> TRUE, version |-> 0]

\* @type: Set(Int);
Committed == {t \in Transactions : committed[t]}

Completed == \A r \in Replicas : active[r] = 0

\* @type: (Int) => Bool;
IsWriter(tx) == \E k \in Keys : lastWrite[tx][k] > 0

(* The cache is exactly the latest preceding local write, or, after commit,
   the final write of a key. Reads never change it; committed cells are frozen.
   Root remains available whenever there is no preceding local write. *)
\* @type: (Int, Int) => Set($traceSource);
ReadSources(r, k) ==
    IF lastWrite[active[r]][k] > 0 THEN
        {[initial |-> FALSE, version |-> lastWrite[active[r]][k]]}
    ELSE {RootSource} \cup
         {[initial |-> FALSE, version |-> lastWrite[tx][k]] :
             tx \in {t \in Committed : visible[r][t] /\ lastWrite[t][k] > 0}}


(* Numeric workload partitions, not isolation verdicts: 0 unrestricted,
   1 write skew, 2 competing snapshot writers, 3 changing external source. *)
\* @type: (Int, Int) => $traceEvent;
ObservedEvent(r, e) == events[sessionTxn[r][1]][e]

TargetComplete ==
    IF TargetPattern = 0 THEN TRUE
    ELSE /\ sessionCount[1] = 1 /\ sessionCount[2] = 1
         /\ IF TargetPattern = 3 THEN
               /\ eventLength[sessionTxn[1][1]] = 1
               /\ eventLength[sessionTxn[2][1]] = 2
               /\ ObservedEvent(1, 1).kind = 1
               /\ ObservedEvent(2, 1).kind = 0 /\ ObservedEvent(2, 1).initial
               /\ ObservedEvent(2, 2).kind = 0 /\ ~ObservedEvent(2, 2).initial
               /\ ObservedEvent(2, 1).key = ObservedEvent(1, 1).key
               /\ ObservedEvent(2, 2).key = ObservedEvent(1, 1).key
               /\ ObservedEvent(2, 2).version = ObservedEvent(1, 1).version
            ELSE
               /\ \A r \in Replicas :
                     /\ eventLength[sessionTxn[r][1]] = 2
                     /\ ObservedEvent(r, 1).kind = 0 /\ ObservedEvent(r, 1).initial
                     /\ ObservedEvent(r, 2).kind = 1
               /\ IF TargetPattern = 1 THEN
                     /\ ObservedEvent(1, 1).key = ObservedEvent(2, 2).key
                     /\ ObservedEvent(2, 1).key = ObservedEvent(1, 2).key
                     /\ ObservedEvent(1, 2).key # ObservedEvent(2, 2).key
                  ELSE
                     /\ ObservedEvent(1, 1).key = ObservedEvent(1, 2).key
                     /\ ObservedEvent(2, 1).key = ObservedEvent(1, 2).key
                     /\ ObservedEvent(2, 2).key = ObservedEvent(1, 2).key

(* Each targeted partition has exactly one transaction per replica. Begin
   observes nothing, so both Begins can commute to this fixed prefix; internal
   allocation IDs are omitted from Result. Their two steps still count toward
   MaxDepth. Root-only partitions also need no Deliver: removing it changes
   neither root reads nor event positions, and final Idle supplies any padding.
   Event guards retain exactly prefixes of the final target shapes. These
   reductions apply only to the declared targets, not unrestricted generation. *)
Init ==
    /\ ReplicaCount >= 0
    /\ MaxTransactions >= 0
    /\ MaxEvents >= 0
    /\ MaxDepth >= 0
    /\ active = [r \in Replicas |-> IF TargetPattern = 0 THEN 0 ELSE r]
    /\ sessionCount = [r \in Replicas |-> 0]
    /\ sessionTxn = [r \in Replicas |->
                       [h \in Transactions |-> IF TargetPattern # 0 /\ h = 1 THEN r ELSE 0]]
    /\ committed = [t \in StorageTransactions |-> FALSE]
    /\ visible = [r \in Replicas |-> [t \in StorageTransactions |-> FALSE]]
    /\ lastWrite = [t \in StorageTransactions |-> [k \in Keys |-> 0]]
    /\ eventLength = [t \in StorageTransactions |-> 0]
    /\ events = [t \in StorageTransactions |-> [e \in EventSlots |-> EmptyEvent]]
    /\ startedCount = IF TargetPattern = 0 THEN 0 ELSE ReplicaCount
    /\ eventCount = 0
    /\ step = IF TargetPattern = 0 THEN 0 ELSE ReplicaCount
    /\ operation = 0
    /\ replica = 0
    /\ key = 0
    /\ source = RootSource
    /\ writer = 0

(* Action codes: 0 Idle, 1 Begin, 2 Read, 3 Write, 4 Commit, 5 Deliver.
   Choices are assigned outside guards/updates so the explorer exposes one
   broad TakeStep transition. Every irrelevant choice has exactly one value. *)
Next == TakeStep::
    /\ step < MaxDepth
    /\ step' = step + 1
    /\ operation' \in IF TargetPattern = 0 THEN 0..5
                     ELSE IF TargetPattern = 3 THEN {0, 2, 3, 4, 5}
                     ELSE {0, 2, 3, 4}
    /\ replica' \in IF operation' = 0 THEN {0} ELSE Replicas
    /\ key' \in IF operation' \in {2, 3} THEN Keys ELSE {0}
    /\ source' \in IF operation' = 2
                   THEN IF TargetPattern \in {1, 2} THEN {RootSource}
                        ELSE ReadSources(replica', key')
                   ELSE {RootSource}
    /\ writer' \in IF operation' = 5 THEN Committed ELSE {0}
    /\ CASE operation' = 0 -> Completed
         [] operation' = 1 -> active[replica'] = 0 /\ startedCount < MaxTransactions
         [] operation' = 2 ->
               /\ active[replica'] # 0 /\ eventCount < MaxEvents
               /\ IF TargetPattern = 0 THEN TRUE
                  ELSE IF TargetPattern = 3
                       THEN replica' = 2 /\ eventLength[active[replica']] < 2 /\
                            (eventLength[active[replica']] = 0 <=> source'.initial)
                       ELSE eventLength[active[replica']] = 0
         [] operation' = 3 ->
               /\ active[replica'] # 0 /\ eventCount < MaxEvents
               /\ IF TargetPattern = 0 THEN TRUE
                  ELSE IF TargetPattern = 3
                       THEN replica' = 1 /\ eventLength[active[replica']] = 0
                       ELSE /\ eventLength[active[replica']] = 1
                            /\ IF TargetPattern = 1
                               THEN key' # ObservedEvent(replica', 1).key
                               ELSE key' = ObservedEvent(replica', 1).key
         [] operation' = 4 ->
               /\ active[replica'] # 0
               /\ IF TargetPattern = 0 THEN TRUE
                  ELSE eventLength[active[replica']] =
                       IF TargetPattern = 3 /\ replica' = 1 THEN 1 ELSE 2
         [] operation' = 5 -> IsWriter(writer') /\ ~visible[replica'][writer']
    /\ active' = IF TargetPattern = 0 /\ operation' = 1
                 THEN [active EXCEPT ![replica'] = startedCount + 1]
                 ELSE IF operation' = 4
                 THEN [active EXCEPT ![replica'] = 0]
                 ELSE active
    /\ sessionCount' = IF operation' = 4
                       THEN [sessionCount EXCEPT ![replica'] = @ + 1]
                       ELSE sessionCount
    /\ sessionTxn' = IF TargetPattern = 0 /\ operation' = 1
                     THEN [sessionTxn EXCEPT ![replica'][sessionCount[replica'] + 1] =
                                                startedCount + 1]
                     ELSE sessionTxn
    /\ committed' = IF operation' = 4
                    THEN [committed EXCEPT ![active[replica']] = TRUE]
                    ELSE committed
    /\ visible' = IF operation' = 4
                  THEN [visible EXCEPT ![replica'][active[replica']] = TRUE]
                  ELSE IF operation' = 5
                  THEN [visible EXCEPT ![replica'][writer'] = TRUE]
                  ELSE visible
    /\ lastWrite' = IF operation' = 3
                    THEN [lastWrite EXCEPT ![active[replica']][key'] = eventCount + 1]
                    ELSE lastWrite
    /\ eventLength' = IF operation' \in {2, 3}
                      THEN [eventLength EXCEPT ![active[replica']] = @ + 1]
                      ELSE eventLength
    /\ events' = IF operation' \in {2, 3}
                 THEN [events EXCEPT ![active[replica']][eventLength[active[replica']] + 1] =
                         [kind |-> IF operation' = 2 THEN 0 ELSE 1,
                          key |-> key',
                          initial |-> IF operation' = 2 THEN source'.initial ELSE FALSE,
                          version |-> IF operation' = 2 THEN source'.version ELSE eventCount + 1]]
                 ELSE events
    /\ startedCount' = IF TargetPattern = 0 /\ operation' = 1 THEN startedCount + 1 ELSE startedCount
    (* Only events advance IDs; writes have unique positive global positions. *)
    /\ eventCount' = IF operation' \in {2, 3} THEN eventCount + 1 ELSE eventCount
    /\ (step' = MaxDepth => \A r \in Replicas : active'[r] = 0)
    /\ (step' = MaxDepth => TargetComplete')

(* These invariants are available to an independent TLC wrapper. *)
BudgetInvariant ==
    /\ step \in 0..MaxDepth
    /\ eventCount \in 0..MaxEvents
    /\ startedCount \in 0..MaxTransactions
TerminalComplete == step # MaxDepth \/ Completed

\* @type: (Int) => Seq($traceEvent);
TransactionEvents(t) == FunAsSeq(events[t], eventLength[t], MaxEvents)

\* @type: (Int) => Seq(Seq($traceEvent));
SessionEvents(r) ==
    LET AtHeight(h) == TransactionEvents(sessionTxn[r][h])
    IN SubSeq(MkSeq(MaxTransactions, AtHeight), 1, sessionCount[r])

(* MkSeq evaluates every capacity slot before SubSeq trims unused slots.
   Unused session links therefore reference the defined empty dummy txn 0. *)
\* @type: Seq(Seq(Seq($traceEvent)));
done == MkSeq(ReplicaCount, SessionEvents)

\* @type: {sessions: Seq(Seq(Seq($traceEvent)))};
Result == [sessions |-> done]
=============================================================================
