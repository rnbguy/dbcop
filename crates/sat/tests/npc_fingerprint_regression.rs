use std::collections::VecDeque;

use dbcop_core::consistency::constrained_linearization::{
    ConstrainedLinearizationSolver, DfsSearchOptions, TieBreaking,
};
use dbcop_core::consistency::prefix::PrefixConsistencySolver;
use dbcop_core::consistency::saturation::causal::check_causal_read;
use dbcop_core::consistency::serializable::SerializabilitySolver;
use dbcop_core::consistency::snapshot_isolation::SnapshotIsolationSolver;
use dbcop_core::history::atomic::types::TransactionId;
use dbcop_core::history::raw::types::{Event, Session, Transaction};
use dbcop_core::{check, Consistency};
use dbcop_sat::{check_prefix, check_serializable, check_snapshot_isolation};

// A deterministic branch order explores the conflicting writer before the valid witness.
// Keep memoization, nogood learning, and dominance pruning enabled.
struct ReverseCandidates<S>(S);

impl<S: ConstrainedLinearizationSolver> ConstrainedLinearizationSolver for ReverseCandidates<S> {
    type Vertex = S::Vertex;

    fn search_options(&self) -> DfsSearchOptions {
        DfsSearchOptions {
            enable_killer_history: false,
            tie_breaking: TieBreaking::Deterministic,
            restart_max_attempts: 0,
            ..self.0.search_options()
        }
    }

    fn get_root(&self) -> Self::Vertex {
        self.0.get_root()
    }

    fn children_of(&self, source: &Self::Vertex) -> Option<Vec<Self::Vertex>> {
        self.0.children_of(source)
    }

    fn vertices(&self) -> Vec<Self::Vertex> {
        self.0.vertices()
    }

    fn allow_next(&self, linearization: &[Self::Vertex], vertex: &Self::Vertex) -> bool {
        self.0.allow_next(linearization, vertex)
    }

    fn forward_book_keeping(&mut self, linearization: &[Self::Vertex]) {
        self.0.forward_book_keeping(linearization);
    }

    fn backtrack_book_keeping(&mut self, linearization: &[Self::Vertex]) {
        self.0.backtrack_book_keeping(linearization);
    }

    fn frontier_signature(&self, frontier_hash: u128, linearization: &[Self::Vertex]) -> u128 {
        self.0.frontier_signature(frontier_hash, linearization)
    }

    fn ordered_frontier_candidates(
        &self,
        frontier: &VecDeque<Self::Vertex>,
        linearization: &[Self::Vertex],
        _options: DfsSearchOptions,
    ) -> Vec<(Self::Vertex, bool)> {
        let mut candidates: Vec<_> = frontier
            .iter()
            .map(|vertex| (vertex.clone(), self.allow_next(linearization, vertex)))
            .collect();
        candidates.sort_by(|a, b| b.1.cmp(&a.1).then_with(|| b.0.cmp(&a.0)));
        candidates
    }
}

fn minimized_serial_history() -> Vec<Session<u64, u64>> {
    let w = Event::write;
    let r = Event::read;
    let tx = Transaction::committed;
    // Serial witness: (1,0), (2,0), (2,1), (2,2), (3,0), (3,1), (3,2).
    // Reads of keys 0 and 3 have equal reader sets, formerly lost under independent XOR.
    vec![
        vec![tx(vec![w(3, 2), w(0, 1)])],
        vec![
            tx(vec![w(1, 6), w(2, 8)]),
            tx(vec![r(0, 1), r(3, 2)]),
            tx(vec![r(2, 8), r(0, 1), r(3, 2)]),
        ],
        vec![
            tx(vec![w(2, 9), w(1, 8)]),
            tx(vec![r(3, 2), r(0, 1)]),
            tx(vec![w(0, 2), r(1, 8)]),
        ],
    ]
}

#[test]
fn shared_reader_sets_preserve_the_valid_serial_witness() {
    let history = minimized_serial_history();
    assert!(check_serializable(&history).is_ok());
    assert!(check_prefix(&history).is_ok());
    assert!(check_snapshot_isolation(&history).is_ok());
    for level in [
        Consistency::Serializable,
        Consistency::Prefix,
        Consistency::SnapshotIsolation,
    ] {
        assert!(
            check(&history, level).is_ok(),
            "known serial history failed {level:?}"
        );
    }
    assert!(ReverseCandidates(SerializabilitySolver::from(
        check_causal_read(&history).unwrap()
    ))
    .get_linearization()
    .is_some());
    assert!(ReverseCandidates(PrefixConsistencySolver::from(
        check_causal_read(&history).unwrap()
    ))
    .get_linearization()
    .is_some());
    assert!(ReverseCandidates(SnapshotIsolationSolver::from(
        check_causal_read(&history).unwrap()
    ))
    .get_linearization()
    .is_some());
}

#[test]
fn removing_shared_readers_changes_the_state_signature() {
    let history = minimized_serial_history();
    let mut solver = SerializabilitySolver::from(check_causal_read(&history).unwrap());
    let first = TransactionId {
        session_id: 2,
        session_height: 1,
    };
    let second = TransactionId {
        session_id: 2,
        session_height: 2,
    };
    for key in [0, 3] {
        solver.active_write.insert(key, [first, second].into());
    }
    let before = solver.frontier_signature(0, &[]);
    for key in [0, 3] {
        solver.active_write.get_mut(&key).unwrap().remove(&first);
    }
    assert_ne!(solver.frontier_signature(0, &[]), before);
}

#[test]
fn conflicting_shared_readers_still_reject_lost_updates() {
    let history = vec![
        vec![Transaction::committed(vec![
            Event::write(0, 1),
            Event::write(1, 1),
        ])],
        vec![Transaction::committed(vec![
            Event::read(0, 1),
            Event::read(1, 1),
            Event::write(0, 2),
        ])],
        vec![Transaction::committed(vec![
            Event::read(0, 1),
            Event::read(1, 1),
            Event::write(0, 3),
        ])],
    ];
    assert!(check_prefix(&history).is_ok());
    assert!(check(&history, Consistency::Prefix).is_ok());
    assert!(check_serializable(&history).is_err());
    assert!(check(&history, Consistency::Serializable).is_err());
    assert!(check_snapshot_isolation(&history).is_err());
    assert!(check(&history, Consistency::SnapshotIsolation).is_err());
}
