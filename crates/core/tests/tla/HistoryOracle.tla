--------------------------- MODULE HistoryOracle ---------------------------
EXTENDS Isolation, CaseData, TLC
VARIABLES caseId, order
vars == <<caseId, order>>

Init ==
    /\ caseId \in 1..Len(Cases)
    /\ order = <<0>>

Next ==
    /\ WellFormed(Cases[caseId])
    /\ \E t \in Ids(Cases[caseId]) \ {order[i] : i \in 1..Len(order)} :
           /\ \A edge \in Edges(Cases[caseId]) :
                  edge[2] = t => edge[1] \in {order[i] : i \in DOMAIN order}
           /\ order' = Append(order, t)
    /\ UNCHANGED caseId

Rank == [t \in Ids(Cases[caseId]) |->
           CHOOSE i \in 0..Len(Cases[caseId]) : order[i + 1] = t]

Report ==
    /\ PrintT("DBCOP_TLC_INPUT|0|" \o ToString(Len(Cases)))
    /\ IF ~WellFormed(Cases[caseId])
          THEN PrintT("DBCOP_TLC_INVALID|" \o ToString(caseId))
          ELSE IF Len(order) = Len(Cases[caseId]) + 1
                  THEN \A level \in 0..6 :
                      IF ValidOrder(Cases[caseId], level, Rank, Reach(caseId))
                          THEN PrintT("DBCOP_TLC_MATCH|" \o ToString(caseId) \o
                                      "|" \o ToString(level))
                          ELSE TRUE
                  ELSE TRUE
=============================================================================
