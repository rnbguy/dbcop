use alloc::vec::Vec;
use core::fmt::Debug;
use core::hash::Hash;

use hashbrown::{HashMap, HashSet};

/// Directed graph backed by an adjacency map.
///
/// Each vertex of type `T` maps to the set of its outgoing neighbors.
/// Vertices are added implicitly when they appear in an edge, or explicitly
/// via [`add_vertex`](Self::add_vertex). Self-loops are permitted.
///
/// Used throughout `dbcop_core` to represent session order, visibility
/// relations, and write-read dependencies between transactions.
#[cfg_attr(feature = "serde", derive(::serde::Serialize, ::serde::Deserialize))]
#[derive(Default, Debug, Clone, PartialEq, Eq)]
pub struct DiGraph<T>
where
    T: Hash + Eq + Clone + Debug,
{
    /// Maps each vertex to the set of vertices it has edges to.
    pub adj_map: HashMap<T, HashSet<T>>,
}

impl<T> DiGraph<T>
where
    T: Hash + Eq + Clone + Debug,
{
    /// Inserts a directed edge from `source` to `target`.
    ///
    /// Both vertices are added to the graph if not already present.
    pub fn add_edge(&mut self, source: T, target: T) {
        self.adj_map
            .entry(source)
            .or_default()
            .insert(target.clone());
        self.adj_map.entry(target).or_default();
    }

    /// Inserts directed edges from `source` to every vertex in `targets`.
    pub fn add_edges(&mut self, source: T, targets: &[T]) {
        let entry = self.adj_map.entry(source).or_default();
        entry.extend(targets.iter().cloned());
    }

    /// Adds a vertex with no outgoing edges (if not already present).
    pub fn add_vertex(&mut self, source: T) {
        self.adj_map.entry(source).or_default();
    }

    /// Returns `true` if an edge from `source` to `target` exists.
    pub fn has_edge(&self, source: &T, target: &T) -> bool {
        self.adj_map
            .get(source)
            .is_some_and(|neighbor| neighbor.contains(target))
    }

    /// Detects if the graph contains a cycle using Kahn's algorithm.
    /// Time complexity: O(V+E)
    #[must_use]
    pub fn has_cycle(&self) -> bool {
        self.topological_sort().is_none()
    }

    /// Returns `true` if the graph has no cycles.
    #[must_use]
    pub fn is_acyclic(&self) -> bool {
        !self.has_cycle()
    }

    /// Returns a valid topological ordering of vertices if the graph is acyclic,
    /// or None if the graph contains a cycle.
    /// Uses Kahn's algorithm with time complexity O(V+E).
    #[must_use]
    pub fn topological_sort(&self) -> Option<Vec<T>> {
        let mut in_degree: HashMap<T, usize> = HashMap::new();

        // Initialize in-degrees for all vertices
        for vertex in self.adj_map.keys() {
            in_degree.entry(vertex.clone()).or_insert(0);
        }

        // Calculate in-degrees
        for neighbors in self.adj_map.values() {
            for neighbor in neighbors {
                *in_degree.entry(neighbor.clone()).or_insert(0) += 1;
            }
        }

        // Collect all vertices with in-degree 0
        let mut queue: Vec<T> = in_degree
            .iter()
            .filter(|(_, &degree)| degree == 0)
            .map(|(vertex, _)| vertex.clone())
            .collect();

        let mut result = Vec::new();

        // Process vertices with in-degree 0
        while let Some(vertex) = queue.pop() {
            result.push(vertex.clone());

            // Reduce in-degree of neighbors
            if let Some(neighbors) = self.adj_map.get(&vertex) {
                for neighbor in neighbors {
                    if let Some(degree) = in_degree.get_mut(neighbor) {
                        *degree -= 1;
                        if *degree == 0 {
                            queue.push(neighbor.clone());
                        }
                    }
                }
            }
        }

        // If all vertices were processed, graph is acyclic
        if result.len() == self.adj_map.len() {
            Some(result)
        } else {
            None
        }
    }

    /// Returns an edge `(a, b)` that participates in a cycle, or `None` if acyclic.
    ///
    /// Uses Kahn's algorithm to strip acyclic vertices, then picks an edge
    /// among the remaining (all of which lie on cycles).
    /// Time complexity: O(V+E).
    #[must_use]
    pub fn find_cycle_edge(&self) -> Option<(T, T)> {
        let mut in_degree: HashMap<T, usize> = HashMap::new();

        for vertex in self.adj_map.keys() {
            in_degree.entry(vertex.clone()).or_insert(0);
        }
        for neighbors in self.adj_map.values() {
            for neighbor in neighbors {
                *in_degree.entry(neighbor.clone()).or_insert(0) += 1;
            }
        }

        let mut queue: Vec<T> = in_degree
            .iter()
            .filter(|(_, &deg)| deg == 0)
            .map(|(v, _)| v.clone())
            .collect();

        let mut removed: HashSet<T> = HashSet::new();

        while let Some(vertex) = queue.pop() {
            removed.insert(vertex.clone());
            if let Some(neighbors) = self.adj_map.get(&vertex) {
                for neighbor in neighbors {
                    if let Some(deg) = in_degree.get_mut(neighbor) {
                        *deg -= 1;
                        if *deg == 0 {
                            queue.push(neighbor.clone());
                        }
                    }
                }
            }
        }

        // All vertices not removed are on cycles. Pick the first edge among them.
        for (src, neighbors) in &self.adj_map {
            if removed.contains(src) {
                continue;
            }
            for dst in neighbors {
                if !removed.contains(dst) {
                    return Some((src.clone(), dst.clone()));
                }
            }
        }

        None
    }

    /// Returns true if there is a path from `source` to `target` in the graph.
    #[allow(dead_code)]
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

    /// Computes the transitive closure of the graph.
    ///
    /// Returns a new graph where an edge `(u, v)` exists if and only if
    /// `v` is reachable from `u` in the original graph.
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

    /// Merges all edges from `other` into this graph.
    ///
    /// Returns `true` if any new edge was added.
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

    /// Returns all edges as a list of (source, target) pairs.
    #[must_use]
    pub fn to_edge_list(&self) -> Vec<(T, T)> {
        let mut edges = Vec::new();
        for (src, dsts) in &self.adj_map {
            for dst in dsts {
                edges.push((src.clone(), dst.clone()));
            }
        }
        edges
    }

    /// Extends an already transitively-closed graph with new edges, maintaining
    /// the closure property incrementally.
    ///
    /// For each new edge `(u, v)`, finds all ancestors of `u` (via backward
    /// scan) and all descendants of `v` (via forward BFS), then adds edges
    /// from every ancestor to every descendant.
    ///
    /// Returns `true` if any edge was added.
    ///
    /// Precondition: `self` should already be transitively closed for correct
    /// incremental behavior. An empty graph is trivially closed.
    pub fn incremental_closure<I: IntoIterator<Item = (T, T)>>(&mut self, new_edges: I) -> bool {
        let mut changed = false;
        for (u, v) in new_edges {
            let mut ancestors = HashSet::new();
            let mut stack: Vec<T> = Vec::new();
            stack.push(u.clone());
            while let Some(node) = stack.pop() {
                if ancestors.insert(node.clone()) {
                    for (src, dsts) in &self.adj_map {
                        if dsts.contains(&node) {
                            stack.push(src.clone());
                        }
                    }
                }
            }
            let mut descendants = HashSet::new();
            let mut stack: Vec<T> = Vec::new();
            stack.push(v.clone());
            while let Some(node) = stack.pop() {
                if descendants.insert(node.clone()) {
                    if let Some(dsts) = self.adj_map.get(&node) {
                        for d in dsts {
                            stack.push(d.clone());
                        }
                    }
                }
            }
            for a in &ancestors {
                for d in &descendants {
                    if !self.has_edge(a, d) {
                        self.add_edge(a.clone(), d.clone());
                        changed = true;
                    }
                }
            }
        }
        changed
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

    #[test]
    fn test_topological_sort_acyclic() {
        let mut graph: DiGraph<u32> = DiGraph::default();
        graph.add_edge(1, 2);
        graph.add_edge(2, 3);
        graph.add_edge(1, 3);

        let topo = graph.topological_sort();
        assert!(topo.is_some());

        let order = topo.unwrap();
        assert_eq!(order.len(), 3);

        let pos_1 = order.iter().position(|&x| x == 1).unwrap();
        let pos_2 = order.iter().position(|&x| x == 2).unwrap();
        let pos_3 = order.iter().position(|&x| x == 3).unwrap();

        assert!(pos_1 < pos_2);
        assert!(pos_2 < pos_3);
        assert!(pos_1 < pos_3);
    }

    #[test]
    fn test_topological_sort_cyclic() {
        let mut graph: DiGraph<u32> = DiGraph::default();
        graph.add_edge(1, 2);
        graph.add_edge(2, 3);
        graph.add_edge(3, 1);

        let topo = graph.topological_sort();
        assert!(topo.is_none());
    }

    #[test]
    fn test_topological_sort_empty() {
        let graph: DiGraph<u32> = DiGraph::default();
        let topo = graph.topological_sort();
        assert!(topo.is_some());
        assert_eq!(topo.unwrap().len(), 0);
    }

    #[test]
    fn test_incremental_closure_from_empty() {
        let edges: [(u32, u32); 4] = [(0, 1), (1, 2), (0, 3), (3, 4)];

        let mut full = DiGraph::default();
        for (a, b) in edges {
            full.add_edge(a, b);
        }
        let expected = full.closure();

        let mut incremental: DiGraph<u32> = DiGraph::default();
        incremental.incremental_closure(edges);

        assert_eq!(incremental, expected);
    }

    #[test]
    fn test_incremental_closure_extends_closed_graph() {
        let mut graph: DiGraph<u32> = DiGraph::default();
        graph.add_edge(0, 1);
        graph.add_edge(1, 2);
        graph = graph.closure();

        let changed = graph.incremental_closure([(2u32, 3)]);
        assert!(changed);

        let mut expected: DiGraph<u32> = DiGraph::default();
        expected.add_edge(0, 1);
        expected.add_edge(1, 2);
        expected.add_edge(2, 3);
        let expected = expected.closure();

        assert_eq!(graph, expected);
    }

    #[test]
    fn test_incremental_closure_no_change() {
        let mut graph: DiGraph<u32> = DiGraph::default();
        graph.add_edge(0, 1);
        graph.add_edge(1, 2);
        graph = graph.closure();

        let changed = graph.incremental_closure([(0u32, 2)]);
        assert!(!changed);
    }
}
