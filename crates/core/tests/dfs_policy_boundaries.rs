use std::collections::VecDeque;

use dbcop_core::consistency::constrained_linearization::{
    BranchOrdering, ConstrainedLinearizationSolver, DfsSearchOptions, DominancePruning,
    HeuristicPortfolio, NogoodLearning, PrincipalVariationOrdering, TieBreaking,
};
use hashbrown::{HashMap, HashSet};

const EDGES: [(u8, u8); 3] = [(1, 4), (2, 4), (3, 5)];

struct PolicySolver {
    options: DfsSearchOptions,
    impossible: bool,
    placed: Vec<u8>,
    forwards: usize,
    backwards: usize,
}

impl PolicySolver {
    const fn new(options: DfsSearchOptions, impossible: bool) -> Self {
        Self {
            options,
            impossible,
            placed: Vec::new(),
            forwards: 0,
            backwards: 0,
        }
    }
}

impl ConstrainedLinearizationSolver for PolicySolver {
    type Vertex = u8;

    fn search_options(&self) -> DfsSearchOptions {
        self.options
    }

    fn get_root(&self) -> Self::Vertex {
        0
    }

    fn vertices(&self) -> Vec<Self::Vertex> {
        (1..=5).collect()
    }

    fn children_of(&self, source: &Self::Vertex) -> Option<Vec<Self::Vertex>> {
        let children: Vec<_> = EDGES
            .iter()
            .filter_map(|(a, b)| (a == source).then_some(*b))
            .collect();
        (!children.is_empty()).then_some(children)
    }

    fn branch_score(&self, _: &[Self::Vertex], vertex: &Self::Vertex) -> i64 {
        i64::from(*vertex)
    }

    fn allow_next(&self, linearization: &[Self::Vertex], vertex: &Self::Vertex) -> bool {
        assert_eq!(linearization, self.placed);
        match vertex {
            3 if self.impossible => self.placed.contains(&5),
            4 => self.placed.contains(&3),
            5 => self.placed.contains(&4),
            _ => true,
        }
    }

    fn should_prune(&self, linearization: &[Self::Vertex], _: usize) -> bool {
        linearization.first().is_some_and(|vertex| *vertex != 1)
    }

    fn frontier_signature(&self, frontier_hash: u128, _: &[Self::Vertex]) -> u128 {
        frontier_hash
            ^ self
                .placed
                .iter()
                .fold(0_u128, |mask, vertex| mask | (1_u128 << (64 + *vertex)))
    }

    fn forward_book_keeping(&mut self, linearization: &[Self::Vertex]) {
        assert_eq!(&linearization[..linearization.len() - 1], self.placed);
        self.placed.push(*linearization.last().unwrap());
        self.forwards += 1;
    }

    fn backtrack_book_keeping(&mut self, linearization: &[Self::Vertex]) {
        assert_eq!(linearization, self.placed);
        self.placed.pop();
        self.backwards += 1;
    }
}

fn reference_orders(impossible: bool) -> Vec<Vec<u8>> {
    fn enumerate(prefix: &mut Vec<u8>, result: &mut Vec<Vec<u8>>) {
        if prefix.len() == 5 {
            result.push(prefix.clone());
            return;
        }
        for vertex in 1..=5 {
            if !prefix.contains(&vertex) {
                prefix.push(vertex);
                enumerate(prefix, result);
                prefix.pop();
            }
        }
    }
    let mut orders = Vec::new();
    enumerate(&mut Vec::new(), &mut orders);
    orders.retain(|order| {
        let position = |vertex| order.iter().position(|value| *value == vertex).unwrap();
        order[0] == 1
            && EDGES.iter().all(|(a, b)| position(*a) < position(*b))
            && position(3) < position(4)
            && position(4) < position(5)
            && (!impossible || position(5) < position(3))
    });
    orders
}

#[test]
fn policy_modes_and_budgeted_restarts_preserve_exhaustive_results_and_state() {
    for impossible in [false, true] {
        let expected = reference_orders(impossible);
        assert_eq!(expected.is_empty(), impossible);
        for branch_ordering in [
            BranchOrdering::AsProvided,
            BranchOrdering::HighScoreFirst,
            BranchOrdering::LowScoreFirst,
        ] {
            for prefer_allowed_first in [false, true] {
                for accelerated in [false, true] {
                    let options = DfsSearchOptions {
                        branch_ordering,
                        prefer_allowed_first,
                        memoize_frontier: accelerated,
                        nogood_learning: if accelerated {
                            NogoodLearning::Enabled
                        } else {
                            NogoodLearning::Disabled
                        },
                        dominance_pruning: if accelerated {
                            DominancePruning::Enabled
                        } else {
                            DominancePruning::Disabled
                        },
                        enable_killer_history: accelerated,
                        tie_breaking: if accelerated {
                            TieBreaking::Randomized
                        } else {
                            TieBreaking::Deterministic
                        },
                        restart_max_attempts: if accelerated { 7 } else { 0 },
                        restart_node_budget: Some(1),
                        heuristic_portfolio: if accelerated {
                            HeuristicPortfolio::Enabled
                        } else {
                            HeuristicPortfolio::Disabled
                        },
                        principal_variation_ordering: if accelerated {
                            PrincipalVariationOrdering::Enabled
                        } else {
                            PrincipalVariationOrdering::Disabled
                        },
                    };
                    let mut solver = PolicySolver::new(options, impossible);
                    if let Some(order) = solver.get_linearization() {
                        assert!(
                            expected.contains(&order),
                            "invalid witness with {options:?}: {order:?}"
                        );
                        assert_eq!(solver.placed, order);
                        assert_eq!(solver.forwards - solver.backwards, order.len());
                    } else {
                        assert!(expected.is_empty(), "lost witness with {options:?}");
                        assert_eq!(solver.placed, [] as [u8; 0]);
                        assert_eq!(solver.forwards, solver.backwards);
                    }
                    if accelerated {
                        assert!(
                            solver.backwards > 0,
                            "budgeted attempts must unwind before the exhaustive pass"
                        );
                    }
                }
            }
        }
    }
}

#[test]
fn direct_dfs_restores_frontier_parent_counts_hash_and_solver_after_failure() {
    let mut solver = PolicySolver::new(DfsSearchOptions::default(), true);
    let mut frontier = VecDeque::from([1, 2, 3]);
    let mut parents: HashMap<_, _> = [(1, 0), (2, 0), (3, 0), (4, 2), (5, 1)].into();
    let initial_parents = parents.clone();
    let mut hash = frontier
        .iter()
        .fold(0, |hash, vertex| hash ^ solver.zobrist_value(vertex));
    let initial_hash = hash;
    let mut linearization = Vec::new();
    assert!(!solver.do_dfs(
        &mut frontier,
        &mut parents,
        &mut linearization,
        &mut HashSet::new(),
        &mut hash
    ));
    assert_eq!(
        frontier.into_iter().collect::<HashSet<_>>(),
        HashSet::from([1, 2, 3])
    );
    assert_eq!(parents, initial_parents);
    assert_eq!(hash, initial_hash);
    assert_eq!(linearization, [] as [u8; 0]);
    assert_eq!(solver.placed, [] as [u8; 0]);
    assert_eq!(solver.forwards, solver.backwards);
}
