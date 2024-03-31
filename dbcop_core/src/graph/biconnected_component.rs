//! [Biconnected component](https://en.wikipedia.org/wiki/Biconnected_component)

use alloc::collections::BTreeSet;
use alloc::vec::Vec;
use core::cmp::min;
use core::fmt::Debug;
use core::hash::Hash;
use core::iter::Iterator;

use hashbrown::{HashMap, HashSet};

use crate::graph::ugraph::UGraph;

#[derive(Debug)]
pub struct BiconnectedComponentWalker<'a, T>
where
    T: Hash + Eq + Clone + Debug,
{
    graph: &'a UGraph<T>,
    visited: HashSet<T>,
    depth: HashMap<T, u64>,
    lowpoint: HashMap<T, u64>,
    parent: HashMap<T, T>,
    dfs_stack: Vec<T>,
    components: HashSet<BTreeSet<T>>,
    articulation_points: HashSet<T>,
    non_group: HashSet<BTreeSet<T>>,
}

impl<'a, T> BiconnectedComponentWalker<'a, T>
where
    T: Hash + Eq + Clone + Debug + Ord,
{
    /// # Panics
    ///
    /// Doesn't panic because invariants are maintained.
    #[must_use]
    pub fn get_vertex_components(
        graph: &'a UGraph<T>,
    ) -> (HashSet<T>, HashSet<BTreeSet<T>>, HashSet<BTreeSet<T>>) {
        let mut solver = Self {
            graph,
            visited: HashSet::new(),
            depth: HashMap::new(),
            lowpoint: HashMap::new(),
            parent: HashMap::new(),
            dfs_stack: Vec::new(),
            components: HashSet::new(),
            articulation_points: HashSet::new(),
            non_group: HashSet::new(),
        };

        for vertex in solver.graph.adj_map.keys() {
            match graph
                .adj_map
                .get(vertex)
                .map(HashSet::len)
                .unwrap_or_default()
            {
                0 => {
                    // singleton vertex
                    solver.non_group.insert([vertex.clone()].into());
                }
                1 => {
                    let partner = solver
                        .graph
                        .adj_map
                        .get(vertex)
                        .expect("has neighbors")
                        .iter()
                        .next()
                        .expect("one neighbor");

                    if graph
                        .adj_map
                        .get(partner)
                        .map(HashSet::len)
                        .unwrap_or_default()
                        == 1
                    {
                        // partner also has 1 neighbor
                        // it is a pair of vertices
                        solver
                            .non_group
                            .insert([vertex.clone(), partner.clone()].into());
                    }
                    // skip the leaf vertex of connected sub-graph of size >= 3
                    // we will process it via its neighbor
                }
                _ => {
                    // non-leaf vertex of connected sub-graph of size >= 3
                    // has at least 2 neighbors
                    solver.vertex_components_helper(vertex, 0);
                }
            }
        }

        (
            solver.articulation_points,
            solver.components,
            solver.non_group,
        )
    }

    fn vertex_components_helper(&mut self, vertex: &T, depth: u64) {
        // The original pseudocode from [Wikipedia](https://en.wikipedia.org/wiki/Biconnected_component#Pseudocode)
        //
        // GetArticulationPoints(i, d)
        //     visited[i] := true
        //     depth[i] := d
        //     low[i] := d
        //     childCount := 0
        //     isArticulation := false
        //     for each ni in adj[i] do
        //         if not visited[ni] then
        //             parent[ni] := i
        //             GetArticulationPoints(ni, d + 1)
        //             childCount := childCount + 1
        //             if low[ni] ≥ depth[i] then
        //                 isArticulation := true
        //             low[i] := Min (low[i], low[ni])
        //         else if ni ≠ parent[i] then
        //             low[i] := Min (low[i], depth[ni])
        //     if (parent[i] ≠ null and isArticulation) or (parent[i] = null and childCount > 1) then
        //         Output i as articulation point
        //
        // this has modified to return the components and non-triangles as well
        // this runs on vertices that has at least 2 neighbors
        // other vertices are skipped

        if !self.visited.insert(vertex.clone()) {
            return;
        }
        self.depth.insert(vertex.clone(), depth);
        self.lowpoint.insert(vertex.clone(), depth);
        self.dfs_stack.push(vertex.clone());

        for neighbor in self
            .graph
            .adj_map
            .get(vertex)
            .iter()
            .flat_map(|neighbors| neighbors.iter())
        {
            if !self.visited.contains(neighbor) {
                self.parent.insert(neighbor.clone(), vertex.clone());
                self.vertex_components_helper(neighbor, depth + 1);
                if self.lowpoint[neighbor] >= self.depth[vertex] {
                    let mut component = BTreeSet::new();
                    while let Some(v) = self.dfs_stack.pop() {
                        if &v == vertex {
                            component.insert(v);
                            break;
                        }
                        component.insert(v.clone());
                    }
                    self.components.insert(component);
                    // put back the vertex, there maybe more components
                    self.dfs_stack.push(vertex.clone());
                    self.articulation_points.insert(vertex.clone());
                }
                *self.lowpoint.get_mut(vertex).unwrap() =
                    min(self.lowpoint[vertex], self.lowpoint[neighbor]);
            } else if Some(neighbor) != self.parent.get(vertex) {
                // neighbor != parent[vertex] ensures the parent is not counted as a child
                // as it is an undirected graph
                *self.lowpoint.get_mut(vertex).unwrap() =
                    min(self.lowpoint[vertex], self.depth[neighbor]);
            }
        }

        if depth == 0 {
            assert_eq!(
                self.dfs_stack.pop().as_ref(),
                Some(vertex),
                "at root, stack should contain only the root"
            );
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_pair() {
        let mut graph = UGraph::default();

        graph.add_edge(0, 1);

        let (articulation_points, components, non_group) =
            BiconnectedComponentWalker::get_vertex_components(&graph);

        assert_eq!(articulation_points, [].into());
        assert_eq!(components, [].into());
        assert_eq!(non_group, [[0, 1].into()].into());
    }

    #[test]
    fn test_biconnected_component() {
        let mut graph = UGraph::default();

        graph.add_edge(1, 0);
        graph.add_edge(0, 2);
        graph.add_edge(2, 1);
        graph.add_edge(0, 3);
        graph.add_edge(3, 5);
        graph.add_edge(3, 4);
        graph.add_vertex(6);

        let (articulation_points, components, non_group) =
            BiconnectedComponentWalker::get_vertex_components(&graph);

        assert_eq!(articulation_points, [0, 3].into());

        assert_eq!(
            components,
            [
                [0, 1, 2].into(),
                [3, 4].into(),
                [0, 3].into(),
                [5, 3].into(),
                [0, 2, 1].into(),
            ]
            .into()
        );

        assert_eq!(non_group, [[6].into()].into());
    }

    #[test]
    fn test_wikipedia() {
        // the example from wikipedia
        let mut graph = UGraph::default();

        graph.add_edges(&0, [1, 9]);
        graph.add_edges(&1, [2, 6, 8]);
        graph.add_edges(&2, [3, 4]);
        graph.add_edges(&3, [4]);
        graph.add_edges(&4, [5]);
        graph.add_edges(&5, [6]);
        graph.add_edges(&6, [7]);
        graph.add_edges(&9, [10]);
        graph.add_edges(&10, [11, 12]);
        graph.add_edges(&11, [13]);
        graph.add_edges(&12, [13]);

        let (articulation_points, components, non_group) =
            BiconnectedComponentWalker::get_vertex_components(&graph);

        assert_eq!(articulation_points, [0, 1, 6, 9, 10].into());

        assert_eq!(
            components,
            [
                [0, 1].into(),
                [1, 8].into(),
                [6, 7].into(),
                [9, 10].into(),
                [0, 9].into(),
                [10, 11, 12, 13].into(),
                [1, 2, 3, 4, 5, 6].into(),
            ]
            .into()
        );

        assert_eq!(non_group, [].into());
    }
}
