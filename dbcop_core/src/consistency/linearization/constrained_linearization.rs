use alloc::collections::vec_deque::VecDeque;
use alloc::vec::Vec;
use core::fmt::Debug;
use core::hash::Hash;

use hashbrown::{HashMap, HashSet};

// Zobrist hash: XOR two u64 hashes with different seeds -> u128
fn zobrist_value<T: Hash>(v: &T) -> u128 {
    use core::hash::{BuildHasher, Hasher};

    use hashbrown::DefaultHashBuilder;

    let builder = DefaultHashBuilder::default();
    let mut h1 = builder.build_hasher();
    0u64.hash(&mut h1);
    v.hash(&mut h1);
    let lo = h1.finish();

    let mut h2 = builder.build_hasher();
    1u64.hash(&mut h2);
    v.hash(&mut h2);
    let hi = h2.finish();

    (u128::from(hi) << 64) | u128::from(lo)
}

pub trait ConstrainedLinearizationSolver {
    type Vertex: Hash + Ord + Eq + Clone + Debug;

    fn get_root(&self) -> Self::Vertex;

    fn children_of(&self, source: &Self::Vertex) -> Option<Vec<Self::Vertex>>;

    fn allow_next(&self, linearization: &[Self::Vertex], v: &Self::Vertex) -> bool;

    fn vertices(&self) -> Vec<Self::Vertex>;

    fn forward_book_keeping(&mut self, linearization: &[Self::Vertex]);
    fn backtrack_book_keeping(&mut self, linearization: &[Self::Vertex]);

    fn do_dfs(
        &mut self,
        non_det_choices: &mut VecDeque<Self::Vertex>,
        active_parent: &mut HashMap<Self::Vertex, usize>,
        linearization: &mut Vec<Self::Vertex>,
        seen: &mut HashSet<u128>,
        frontier_hash: &mut u128,
    ) -> bool {
        // println!("explored {}", seen.len());
        if !seen.insert(*frontier_hash) {
            // seen is not modified
            // non-det choices are already explored
            false
        } else if non_det_choices.is_empty() {
            true
        } else {
            let curr_non_det_choices = non_det_choices.len();
            for _ in 0..curr_non_det_choices {
                if let Some(u) = non_det_choices.pop_front() {
                    if self.allow_next(linearization, &u) {
                        // access it again
                        if let Some(vs) = self.children_of(&u) {
                            for v in vs {
                                let entry = active_parent
                                    .get_mut(&v)
                                    .expect("all vertices are expected in active parent");
                                *entry -= 1;
                                if *entry == 0 {
                                    non_det_choices.push_back(v);
                                }
                            }
                        }

                        linearization.push(u.clone());
                        *frontier_hash ^= zobrist_value(&u);

                        self.forward_book_keeping(linearization);

                        if self.do_dfs(
                            non_det_choices,
                            active_parent,
                            linearization,
                            seen,
                            frontier_hash,
                        ) {
                            return true;
                        }

                        self.backtrack_book_keeping(linearization);

                        linearization.pop();
                        *frontier_hash ^= zobrist_value(&u);

                        if let Some(vs) = self.children_of(&u) {
                            for v in vs {
                                let entry = active_parent
                                    .get_mut(&v)
                                    .expect("all vertices are expected in active parent");
                                *entry += 1;
                            }
                        }
                        non_det_choices.drain(curr_non_det_choices - 1..);
                    }
                    non_det_choices.push_back(u);
                }
            }
            false
        }
    }

    fn get_linearization(&mut self) -> Option<Vec<Self::Vertex>> {
        let mut non_det_choices: VecDeque<Self::Vertex> = VecDeque::default();
        let mut active_parent: HashMap<Self::Vertex, usize> = HashMap::default();
        let mut linearization: Vec<Self::Vertex> = Vec::default();
        let mut seen: HashSet<u128> = HashSet::default();
        let mut frontier_hash: u128 = 0;

        // do active_parent counting
        for u in self.vertices() {
            {
                active_parent.entry(u.clone()).or_insert(0);
            }
            if let Some(vs) = self.children_of(&u) {
                for v in vs {
                    let entry = active_parent.entry(v).or_insert(0);
                    *entry += 1;
                }
            }
        }

        // take vertices with zero active_parent as non-det choices
        active_parent.iter().for_each(|(n, v)| {
            if *v == 0 {
                non_det_choices.push_back(n.clone());
                frontier_hash ^= zobrist_value(n);
            }
        });

        self.do_dfs(
            &mut non_det_choices,
            &mut active_parent,
            &mut linearization,
            &mut seen,
            &mut frontier_hash,
        );

        if linearization.is_empty() {
            None
        } else {
            Some(linearization)
        }
    }
}
