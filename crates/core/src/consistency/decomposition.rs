use alloc::collections::BTreeSet;
use alloc::vec::Vec;
use core::hash::Hash;

use hashbrown::HashSet;

use crate::graph::biconnected_component::BiconnectedComponentWalker;
use crate::graph::ugraph::UGraph;
use crate::history::atomic::AtomicTransactionPO;

/// Builds a communication (conflict) graph from an atomic transaction partial order.
///
/// The communication graph represents which sessions interact through shared variables.
/// Two sessions are connected if they both access (read or write) at least one common variable.
///
/// # Arguments
///
/// * `po` - The atomic transaction partial order containing variable access information
///
/// # Returns
///
/// A `UGraph<u64>` where vertices are session IDs and edges connect sessions that share variables.
/// This graph is used for biconnected component decomposition (Theorem 5.2 in the paper).
///
/// # Paper Reference
///
/// Paper Section 5, Definition 5.1: Comm(h) = vertices are sessions, edge iff two sessions
/// read/write a common variable. Theorem 5.2: h satisfies X iff for every biconnected
/// component C of Comm(h), h↓C satisfies X.
#[must_use]
pub fn communication_graph<Variable>(po: &AtomicTransactionPO<Variable>) -> UGraph<u64>
where
    Variable: Clone + Eq + Hash,
{
    let mut graph: UGraph<u64> = UGraph::default();

    // Build a map of variable -> set of sessions that access it
    let mut var_to_sessions: hashbrown::HashMap<Variable, HashSet<u64>> =
        hashbrown::HashMap::default();

    // Iterate through all transactions and collect variable accesses per session
    for (txn_id, txn_info) in &po.history.0 {
        let session_id = txn_id.session_id;

        // Add vertices for all sessions
        graph.add_vertex(session_id);

        // Track writes
        for variable in &txn_info.writes {
            var_to_sessions
                .entry(variable.clone())
                .or_default()
                .insert(session_id);
        }

        // Track reads
        for variable in txn_info.reads.keys() {
            var_to_sessions
                .entry(variable.clone())
                .or_default()
                .insert(session_id);
        }
    }

    // For each variable, connect all sessions that access it
    for sessions in var_to_sessions.values() {
        let sessions_vec: Vec<u64> = sessions.iter().copied().collect();
        for i in 0..sessions_vec.len() {
            for j in (i + 1)..sessions_vec.len() {
                graph.add_edge(sessions_vec[i], sessions_vec[j]);
            }
        }
    }

    graph
}

/// Find connected components of a communication graph.
///
/// Returns a [`Vec`] of [`BTreeSet`]s, each containing the session IDs in
/// one connected component. Isolated sessions appear as singletons.
#[must_use]
pub fn connected_components(graph: &UGraph<u64>) -> Vec<BTreeSet<u64>> {
    let mut visited: HashSet<u64> = HashSet::default();
    let mut components: Vec<BTreeSet<u64>> = Vec::new();

    let mut all_vertices: Vec<u64> = graph.adj_map.keys().copied().collect();
    all_vertices.sort_unstable();

    for start in all_vertices {
        if visited.contains(&start) {
            continue;
        }
        let mut component: BTreeSet<u64> = BTreeSet::new();
        let mut stack: Vec<u64> = Vec::new();
        stack.push(start);
        while let Some(v) = stack.pop() {
            if !visited.insert(v) {
                continue;
            }
            component.insert(v);
            if let Some(neighbors) = graph.adj_map.get(&v) {
                for &n in neighbors {
                    if !visited.contains(&n) {
                        stack.push(n);
                    }
                }
            }
        }
        components.push(component);
    }
    components
}

/// Find biconnected components of a communication graph.
///
/// Returns vertex-sets (session IDs) for all biconnected blocks plus
/// singleton/pair non-groups produced by the decomposition walker.
#[must_use]
pub fn biconnected_components(graph: &UGraph<u64>) -> Vec<BTreeSet<u64>> {
    let (_, components, non_group) = BiconnectedComponentWalker::get_vertex_components(graph);

    let mut result: Vec<BTreeSet<u64>> = components.into_iter().chain(non_group).collect();
    result.sort_unstable();
    result.dedup();
    result
}

#[cfg(test)]
mod tests {
    use hashbrown::HashMap;

    use super::*;
    use crate::history::atomic::types::{
        AtomicTransactionHistory, AtomicTransactionInfo, TransactionId,
    };

    #[test]
    fn test_communication_graph_two_clusters() {
        // Create a 3-session history with 2 clusters:
        // Sessions 1 and 2 share variable "x"
        // Session 3 is isolated (only accesses variable "y")
        let mut history_map = HashMap::new();

        // Session 1, transaction 0: writes "x"
        history_map.insert(
            TransactionId {
                session_id: 1,
                session_height: 0,
            },
            AtomicTransactionInfo {
                reads: HashMap::new(),
                writes: {
                    let mut set = HashSet::new();
                    set.insert("x");
                    set
                },
            },
        );

        // Session 2, transaction 0: reads "x" from session 1
        history_map.insert(
            TransactionId {
                session_id: 2,
                session_height: 0,
            },
            AtomicTransactionInfo {
                reads: {
                    let mut map = HashMap::new();
                    map.insert(
                        "x",
                        TransactionId {
                            session_id: 1,
                            session_height: 0,
                        },
                    );
                    map
                },
                writes: HashSet::new(),
            },
        );

        // Session 3, transaction 0: writes "y" (isolated)
        history_map.insert(
            TransactionId {
                session_id: 3,
                session_height: 0,
            },
            AtomicTransactionInfo {
                reads: HashMap::new(),
                writes: {
                    let mut set = HashSet::new();
                    set.insert("y");
                    set
                },
            },
        );

        let history = AtomicTransactionHistory(history_map);
        let po = AtomicTransactionPO::from(history);
        let comm_graph = communication_graph(&po);

        // Verify graph structure
        // Sessions 1 and 2 should be connected (share variable "x")
        assert!(
            comm_graph
                .adj_map
                .get(&1)
                .is_some_and(|neighbors| neighbors.contains(&2)),
            "Sessions 1 and 2 should be connected"
        );
        assert!(
            comm_graph
                .adj_map
                .get(&2)
                .is_some_and(|neighbors| neighbors.contains(&1)),
            "Sessions 2 and 1 should be connected (undirected)"
        );

        // Session 3 should not be connected to 1 or 2
        assert!(
            !comm_graph
                .adj_map
                .get(&3)
                .is_some_and(|neighbors| neighbors.contains(&1)),
            "Sessions 3 and 1 should not be connected"
        );
        assert!(
            !comm_graph
                .adj_map
                .get(&3)
                .is_some_and(|neighbors| neighbors.contains(&2)),
            "Sessions 3 and 2 should not be connected"
        );

        // All sessions should exist as vertices
        assert!(
            comm_graph.adj_map.contains_key(&1),
            "Session 1 should exist"
        );
        assert!(
            comm_graph.adj_map.contains_key(&2),
            "Session 2 should exist"
        );
        assert!(
            comm_graph.adj_map.contains_key(&3),
            "Session 3 should exist"
        );
    }

    #[test]
    fn test_communication_graph_single_session() {
        // Single session should have no edges
        let mut history_map = HashMap::new();

        history_map.insert(
            TransactionId {
                session_id: 1,
                session_height: 0,
            },
            AtomicTransactionInfo {
                reads: HashMap::new(),
                writes: {
                    let mut set = HashSet::new();
                    set.insert("x");
                    set
                },
            },
        );

        let history = AtomicTransactionHistory(history_map);
        let po = AtomicTransactionPO::from(history);
        let comm_graph = communication_graph(&po);

        // Session 1 should exist but have no neighbors
        assert!(
            comm_graph.adj_map.contains_key(&1),
            "Session 1 should exist"
        );
        assert!(
            comm_graph.adj_map.get(&1).is_some_and(HashSet::is_empty),
            "Session 1 should have no neighbors"
        );
    }

    #[test]
    fn test_communication_graph_fully_connected() {
        // Three sessions all accessing the same variable
        let mut history_map = HashMap::new();

        for session_id in 1..=3 {
            history_map.insert(
                TransactionId {
                    session_id,
                    session_height: 0,
                },
                AtomicTransactionInfo {
                    reads: HashMap::new(),
                    writes: {
                        let mut set = HashSet::new();
                        set.insert("x");
                        set
                    },
                },
            );
        }

        let history = AtomicTransactionHistory(history_map);
        let po = AtomicTransactionPO::from(history);
        let comm_graph = communication_graph(&po);

        // All sessions should be connected to each other
        for i in 1..=3 {
            for j in 1..=3 {
                if i != j {
                    assert!(
                        comm_graph
                            .adj_map
                            .get(&i)
                            .is_some_and(|neighbors| neighbors.contains(&j)),
                        "Sessions {i} and {j} should be connected",
                    );
                }
            }
        }
    }

    #[test]
    fn test_biconnected_components_path_graph() {
        let mut graph: UGraph<u64> = UGraph::default();
        graph.add_edge(1, 2);
        graph.add_edge(2, 3);

        let components = biconnected_components(&graph);
        assert_eq!(components, vec![[1, 2].into(), [2, 3].into()]);
    }
}
