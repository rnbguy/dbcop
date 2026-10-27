------------------------- MODULE ReplicaHistories -------------------------
EXTENDS Integers, Sequences, FiniteSets, TLC, CaseData

(* One ordered session per replica; no aborts and no isolation admission. *)
VARIABLES done, live, active, visible, eventCount
vars == <<done, live, active, visible, eventCount>>
Replicas == 1..ReplicaCount
Keys == 0..1

(* Balanced folds keep TLC's evaluator stack logarithmic in output length. *)
RECURSIVE Flatten(_)
Flatten(ss) ==
    IF Len(ss) = 0 THEN <<>>
    ELSE IF Len(ss) = 1 THEN ss[1]
    ELSE LET middle == Len(ss) \div 2
         IN Flatten(SubSeq(ss, 1, middle)) \o
            Flatten(SubSeq(ss, middle + 1, Len(ss)))
RECURSIVE Csv(_)
Csv(xs) ==
    IF Len(xs) = 0 THEN ""
    ELSE IF Len(xs) = 1 THEN ToString(xs[1])
    ELSE LET middle == Len(xs) \div 2
         IN Csv(SubSeq(xs, 1, middle)) \o "," \o
            Csv(SubSeq(xs, middle + 1, Len(xs)))
Vector(xs) == ToString(Len(xs)) \o ":" \o Csv(xs)

Committed == UNION {{<<r, t>> : t \in 1..Len(done[r])} : r \in Replicas}
Started == Cardinality(Committed) + Cardinality({r \in Replicas : active[r]})
LocalWrites(r, key) ==
    {e \in 1..Len(live[r]) : live[r][e].kind = 1 /\ live[r][e].key = key}
FinalWrites(tx, key) ==
    {e \in 1..Len(done[tx[1]][tx[2]]) :
        /\ done[tx[1]][tx[2]][e].kind = 1
        /\ done[tx[1]][tx[2]][e].key = key
        /\ ~\E later \in (e + 1)..Len(done[tx[1]][tx[2]]) :
                  /\ done[tx[1]][tx[2]][later].kind = 1
                  /\ done[tx[1]][tx[2]][later].key = key}
Writer(tx) == \E key \in Keys : FinalWrites(tx, key) # {}
ReadSources(r, key) ==
    IF LocalWrites(r, key) # {} THEN
        LET last == CHOOSE e \in LocalWrites(r, key) :
                         \A earlier \in LocalWrites(r, key) : earlier <= e
        IN {[initial |-> FALSE, version |-> live[r][last].version]}
    ELSE {[initial |-> TRUE, version |-> 0]} \cup
         UNION {{[initial |-> FALSE,
                  version |-> done[tx[1]][tx[2]][e].version] :
                      e \in FinalWrites(tx, key)} : tx \in visible[r]}

Init ==
    /\ done = [r \in Replicas |-> <<>>]
    /\ live = [r \in Replicas |-> <<>>]
    /\ active = [r \in Replicas |-> FALSE]
    /\ visible = [r \in Replicas |-> {}]
    /\ eventCount = 0

Begin(r) ==
    /\ ~active[r]
    /\ Started < MaxTransactions
    /\ active' = [active EXCEPT ![r] = TRUE]
    /\ UNCHANGED <<done, live, visible, eventCount>>

Read(r, key, source) ==
    /\ active[r]
    /\ eventCount < MaxEvents
    /\ source \in ReadSources(r, key)
    /\ live' = [live EXCEPT ![r] = Append(@,
            [kind |-> 0, key |-> key,
             initial |-> source.initial, version |-> source.version])]
    /\ eventCount' = eventCount + 1
    /\ UNCHANGED <<done, active, visible>>

Write(r, key) ==
    /\ active[r]
    /\ eventCount < MaxEvents
    (* Global event positions make every write ID positive and unique. *)
    /\ live' = [live EXCEPT ![r] = Append(@,
            [kind |-> 1, key |-> key, initial |-> FALSE,
             version |-> eventCount + 1])]
    /\ eventCount' = eventCount + 1
    /\ UNCHANGED <<done, active, visible>>

Commit(r) ==
    /\ active[r]
    /\ done' = [done EXCEPT ![r] = Append(@, live[r])]
    /\ live' = [live EXCEPT ![r] = <<>>]
    /\ active' = [active EXCEPT ![r] = FALSE]
    /\ visible' = [visible EXCEPT ![r] = @ \cup {<<r, Len(done[r]) + 1>>}]
    /\ UNCHANGED eventCount

Deliver(tx, r) ==
    /\ tx \in Committed
    /\ Writer(tx)
    /\ tx \notin visible[r]
    /\ visible' = [visible EXCEPT ![r] = @ \cup {tx}]
    /\ UNCHANGED <<done, live, active, eventCount>>

Next ==
    \/ \E r \in Replicas : Begin(r) \/ Commit(r)
    \/ \E r \in Replicas, key \in Keys :
           Write(r, key) \/ (\E source \in ReadSources(r, key) : Read(r, key, source))
    \/ \E tx \in Committed, r \in Replicas : Deliver(tx, r)

SessionCounts == [r \in Replicas |-> Len(done[r])]
TransactionCounts ==
    Flatten([r \in Replicas |->
        [t \in 1..Len(done[r]) |-> Len(done[r][t])]])
EventFields ==
    Flatten([r \in Replicas |->
        Flatten([t \in 1..Len(done[r]) |->
            Flatten([e \in 1..Len(done[r][t]) |->
                <<r, t - 1, e - 1, done[r][t][e].kind, done[r][t][e].key,
                  IF done[r][t][e].initial THEN 1 ELSE 0,
                  done[r][t][e].version>>])])])

(* Delivering remaining writers changes no observation, so emit only at the
   fully delivered representative of each completed history. Actions remain
   unchanged: every completed history can always reach this representative. *)
CommittedWriters == {tx \in Committed : Writer(tx)}

(* This invariant never gates actions. TLC simulation also inspects generated
   successors; emissions are not only states selected into saved traces. *)
EmitHistories ==
    IF (\A r \in Replicas : ~active[r]) /\
       (\A r \in Replicas : CommittedWriters \subseteq visible[r]) THEN
        PrintT("DBCOP_TLC_HISTORY|" \o ToString(ReplicaCount) \o "|" \o
               Vector(SessionCounts) \o "|" \o Vector(TransactionCounts) \o
               "|" \o Vector(EventFields))
    ELSE TRUE
=============================================================================
