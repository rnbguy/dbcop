use core::fmt::Debug;
use core::hash::Hash;

use hashbrown::{HashMap, HashSet};

#[derive(Default, Debug, Clone)]
pub struct DiGraph<T>
where
    T: Hash + Eq + Clone + Debug,
{
    pub adj_map: HashMap<T, HashSet<T>>,
}

impl<T> DiGraph<T>
where
    T: Hash + Eq + Clone + Debug,
{
    pub fn add_edge(&mut self, source: T, target: T) {
        self.adj_map
            .entry(source)
            .or_default()
            .insert(target.clone());
        self.adj_map.entry(target).or_default();
    }

    pub fn add_edges(&mut self, source: T, targets: &[T]) {
        let entry = self.adj_map.entry(source).or_default();
        entry.extend(targets.iter().cloned());
    }

    pub fn add_vertex(&mut self, source: T) {
        self.adj_map.entry(source).or_default();
    }

    pub fn has_edge(&self, source: &T, target: &T) -> bool {
        self.adj_map
            .get(source)
            .is_some_and(|neighbor| neighbor.contains(target))
    }

    #[must_use]
    pub fn has_cycle(&self) -> bool {
        self.adj_map
            .keys()
            .any(|source| self.is_reachable_helper(source, source, &mut [].into()))
    }

    #[must_use]
    pub fn is_acyclic(&self) -> bool {
        !self.has_cycle()
    }

    /// Returns true if there is a path from `source` to `target` in the graph.
    fn is_reachable_helper(&self, source: &T, target: &T, reachable: &mut HashSet<T>) -> bool {
        if let Some(neighbors) = self.adj_map.get(source) {
            for neighbor in neighbors {
                if neighbor == target
                    || (reachable.insert(neighbor.clone())
                        && self.is_reachable_helper(neighbor, target, reachable))
                {
                    return true;
                }
            }
        }
        false
    }

    /// Mutates `reachable` to contain all vertices reachable from `source`.
    fn find_all_reachable_helper(&self, source: &T, mut reachable: HashSet<T>) -> HashSet<T> {
        if let Some(neighbors) = self.adj_map.get(source) {
            for neighbor in neighbors {
                if reachable.insert(neighbor.clone()) {
                    reachable = self.find_all_reachable_helper(neighbor, reachable);
                }
            }
        }
        reachable
    }

    #[must_use]
    pub fn closure(&self) -> Self {
        Self {
            adj_map: self
                .adj_map
                .keys()
                .map(|source| {
                    (
                        source.clone(),
                        self.find_all_reachable_helper(source, [].into()),
                    )
                })
                .collect(),
        }
    }

    pub fn union(&mut self, other: &Self) -> bool {
        let mut change = false;
        for (source, other_neighbors) in &other.adj_map {
            let neighbors = self.adj_map.entry(source.clone()).or_default();
            let old_size = neighbors.len();
            neighbors.extend(other_neighbors.iter().cloned());
            change |= neighbors.len() != old_size;
        }
        change
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_simple_graph() {
        let mut graph: DiGraph<u32> = DiGraph::default();
        graph.add_edge(1, 2);
        graph.add_edge(2, 3);
        graph.add_edge(3, 4);
        graph.add_edge(4, 5);

        assert!(graph.has_edge(&1, &2));
        assert!(graph.has_edge(&2, &3));
        assert!(graph.has_edge(&3, &4));
        assert!(graph.has_edge(&4, &5));
        assert!(!graph.has_edge(&1, &3));
        assert!(!graph.has_edge(&2, &4));
        assert!(!graph.has_edge(&3, &5));

        assert!(!graph.has_cycle());

        let closure = graph.closure();

        assert_eq!(closure.adj_map[&1], [2, 3, 4, 5].into());
        assert_eq!(closure.adj_map[&2], [3, 4, 5].into());
        assert_eq!(closure.adj_map[&3], [4, 5].into());
        assert_eq!(closure.adj_map[&4], [5].into());
        assert_eq!(closure.adj_map[&5], [].into());
    }

    #[test]
    fn test_cycle() {
        let mut graph: DiGraph<u32> = DiGraph::default();
        graph.add_edge(1, 2);
        graph.add_edge(2, 3);
        graph.add_edge(3, 4);
        graph.add_edge(4, 5);
        graph.add_edge(5, 1);

        assert!(graph.has_cycle());
    }

    #[test]
    fn test_union_cycle() {
        let mut graph1: DiGraph<u32> = DiGraph::default();
        graph1.add_edge(1, 2);
        graph1.add_edge(2, 3);
        graph1.add_edge(3, 4);
        graph1.add_edge(4, 5);
        assert!(!graph1.has_cycle());

        let mut graph2: DiGraph<u32> = DiGraph::default();
        graph2.add_edge(5, 6);
        graph2.add_edge(6, 7);
        graph2.add_edge(7, 8);
        graph2.add_edge(8, 1);

        assert!(!graph2.has_cycle());

        assert!(graph1.union(&graph2));

        assert!(graph1.has_cycle());
    }
}
