use dbcop_core::consistency::constrained_linearization::{
    BranchOrdering, ConstrainedLinearizationSolver, DfsSearchOptions, DominancePruning,
    NogoodLearning, TieBreaking,
};
use dbcop_core::history::raw::types::{Event, Transaction};
use dbcop_core::{check, Consistency};
use dbcop_sat::{check_prefix, check_serializable, check_snapshot_isolation};

// Vertex 1 must precede the interval from 2 to 3. Trying 2 first fails with
// frontier {1, 3}; after placing 1 then 2, frontier {3} still has a valid suffix.
struct IntervalSolver {
    active: bool,
}

impl ConstrainedLinearizationSolver for IntervalSolver {
    type Vertex = u64;

    fn search_options(&self) -> DfsSearchOptions {
        DfsSearchOptions {
            memoize_frontier: false,
            nogood_learning: NogoodLearning::Disabled,
            enable_killer_history: false,
            dominance_pruning: DominancePruning::Enabled,
            tie_breaking: TieBreaking::Deterministic,
            branch_ordering: BranchOrdering::HighScoreFirst,
            ..DfsSearchOptions::default()
        }
    }

    fn get_root(&self) -> Self::Vertex {
        0
    }

    fn vertices(&self) -> Vec<Self::Vertex> {
        vec![1, 2, 3]
    }

    fn children_of(&self, source: &Self::Vertex) -> Option<Vec<Self::Vertex>> {
        if *source == 2 {
            Some(vec![3])
        } else {
            None
        }
    }

    fn allow_next(&self, linearization: &[Self::Vertex], vertex: &Self::Vertex) -> bool {
        match vertex {
            1 => !self.active,
            2 => true,
            3 => linearization.contains(&1),
            _ => false,
        }
    }

    fn frontier_signature(&self, frontier_hash: u128, _: &[Self::Vertex]) -> u128 {
        frontier_hash ^ u128::from(self.active)
    }

    fn forward_book_keeping(&mut self, linearization: &[Self::Vertex]) {
        match linearization.last() {
            Some(2) => self.active = true,
            Some(3) => self.active = false,
            _ => {}
        }
    }

    fn backtrack_book_keeping(&mut self, linearization: &[Self::Vertex]) {
        match linearization.last() {
            Some(2) => self.active = false,
            Some(3) => self.active = true,
            _ => {}
        }
    }
}

#[test]
fn failed_superset_does_not_reject_a_valid_interval_order() {
    let mut solver = IntervalSolver { active: false };
    assert_eq!(solver.get_linearization(), Some(vec![1, 2, 3]));
}

#[test]
fn serial_history_with_a_smaller_si_frontier_remains_valid() {
    let tx = Transaction::committed;
    let w = Event::write;
    let r = Event::read;
    // Minimized from capture seed 301. A serial witness is
    // (2,0), (1,0), (1,1), (2,1), (3,0), (3,1).
    let history: Vec<Vec<Transaction<u64, u64>>> = vec![
        vec![tx(vec![w(2, 1)]), tx(vec![r(2, 1), w(0, 1)])],
        vec![tx(vec![]), tx(vec![w(0, 2)])],
        vec![tx(vec![r(2, 1), w(2, 2)]), tx(vec![r(0, 2), w(0, 3)])],
    ];
    for (level, sat) in [
        (Consistency::Serializable, check_serializable(&history)),
        (Consistency::Prefix, check_prefix(&history).map(|_| ())),
        (
            Consistency::SnapshotIsolation,
            check_snapshot_isolation(&history).map(|_| ()),
        ),
    ] {
        let core = check(&history, level);
        assert!(
            sat.is_ok(),
            "SAT rejected serial history at {level:?}: {sat:?}"
        );
        assert!(
            core.is_ok(),
            "core rejected serial history at {level:?}: {core:?}"
        );
    }
}
