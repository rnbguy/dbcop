use core::fmt::Debug;
use core::hash::Hash;

use hashbrown::{HashMap, HashSet};

#[derive(Default, Debug)]
pub struct UGraph<T>
where
    T: Hash + Eq + Clone + Debug,
{
    pub adj_map: HashMap<T, HashSet<T>>,
}

impl<T> UGraph<T>
where
    T: Hash + Eq + Clone + Debug,
{
    pub fn add_edge(&mut self, source: T, target: T) {
        self.adj_map
            .entry(source.clone())
            .or_default()
            .insert(target.clone());
        self.adj_map.entry(target).or_default().insert(source);
    }

    pub fn add_edges<N>(&mut self, source: &T, targets: N)
    where
        N: IntoIterator<Item = T>,
    {
        for target in targets {
            self.adj_map
                .entry(source.clone())
                .or_default()
                .insert(target.clone());
            self.adj_map
                .entry(target)
                .or_default()
                .insert(source.clone());
        }
    }

    pub fn add_vertex(&mut self, vertex: T) {
        self.adj_map.entry(vertex).or_default();
    }
}
