use std::collections::{HashMap, HashSet};

use chrono::{DateTime, Duration, Local};
use dbcop_core::history::raw::types::{Event, Session, Transaction};
use rand::distr::{Distribution, Uniform};
use rand::RngExt;
use rayon::iter::{IntoParallelIterator, ParallelIterator};
use serde::{Deserialize, Serialize};
use typed_builder::TypedBuilder;

#[derive(Clone, Debug, Default, Deserialize, Serialize, TypedBuilder)]
pub struct HistParams {
    pub id: u64,
    pub n_node: u64,
    pub n_variable: u64,
    pub n_transaction: u64,
    pub n_event: u64,
}

#[derive(Deserialize, Serialize, Debug)]
pub struct History {
    params: HistParams,
    info: String,
    start: DateTime<Local>,
    end: DateTime<Local>,
    data: Vec<Session<u64, u64>>,
}

impl History {
    #[must_use]
    pub const fn new(
        params: HistParams,
        info: String,
        start: DateTime<Local>,
        end: DateTime<Local>,
        data: Vec<Session<u64, u64>>,
    ) -> Self {
        Self {
            params,
            info,
            start,
            end,
            data,
        }
    }

    #[must_use]
    pub const fn get_id(&self) -> u64 {
        self.params.id
    }

    #[must_use]
    pub const fn get_data(&self) -> &Vec<Session<u64, u64>> {
        &self.data
    }

    #[must_use]
    pub const fn get_params(&self) -> &HistParams {
        &self.params
    }

    #[must_use]
    pub fn get_cloned_params(&self) -> HistParams {
        self.params.clone()
    }

    #[must_use]
    pub fn get_duration(&self) -> Duration {
        self.end - self.start
    }
}

/// Generate a single history with `n_node` sessions, each containing
/// `n_transaction` transactions of `n_event` events over `n_variable` variables.
///
/// # Coherence invariant
///
/// Every generated read is coherent: each `Read { variable, version: Some(v) }`
/// is backed by a `Write { variable, version: v }` that exists in the history.
///
/// This is achieved by:
/// 1. Inserting an init transaction (in the first session) that writes every
///    variable at version 0, so reads always have a valid version to observe.
/// 2. Tracking `latest_writes` -- a map from variable to its most recently
///    written version -- and sampling reads from it instead of generating
///    arbitrary (possibly non-existent) versions.
///
/// All generated transactions are committed.
///
/// # Panics
///
/// Panics if `n_variable` is zero (cannot create a uniform distribution over
/// an empty range).
#[must_use]
pub fn generate_single_history(
    n_node: u64,
    n_variable: u64,
    n_transaction: u64,
    n_event: u64,
) -> Vec<Session<u64, u64>> {
    let mut counters: HashMap<u64, u64> = HashMap::new();
    let mut latest_writes: HashMap<u64, u64> = (0..n_variable).map(|v| (v, 0)).collect();
    let mut random_generator = rand::rng();
    let read_variable_range = Uniform::new(0, n_variable).unwrap();

    (0..n_node)
        .enumerate()
        .map(|(node_idx, _)| {
            let mut txns: Vec<Transaction<u64, u64>> = Vec::new();

            if node_idx == 0 {
                txns.push(Transaction {
                    events: (0..n_variable).map(|var| Event::write(var, 0)).collect(),
                    committed: true,
                });
            }

            for _ in 0..n_transaction {
                let readable = latest_writes.clone();
                let mut read_vars: HashSet<u64> = HashSet::new();
                let events = (0..n_event)
                    .map(|_| {
                        let variable = read_variable_range.sample(&mut random_generator);
                        let want_read = random_generator.random::<bool>();
                        if want_read && read_vars.insert(variable) {
                            Event::read(variable, readable[&variable])
                        } else {
                            let version = {
                                let entry = counters.entry(variable).or_default();
                                *entry += 1;
                                *entry
                            };
                            latest_writes.insert(variable, version);
                            Event::write(variable, version)
                        }
                    })
                    .collect();
                txns.push(Transaction {
                    events,
                    committed: true,
                });
            }

            txns
        })
        .collect::<Vec<_>>()
}

#[must_use]
pub fn generate_mult_histories(
    n_hist: u64,
    n_node: u64,
    n_variable: u64,
    n_transaction: u64,
    n_event: u64,
) -> Vec<History> {
    (0..n_hist)
        .into_par_iter()
        .map(|i_hist| {
            let start_time = Local::now();
            let hist = generate_single_history(n_node, n_variable, n_transaction, n_event);
            let end_time = Local::now();
            History {
                params: HistParams {
                    id: i_hist,
                    n_node,
                    n_variable,
                    n_transaction,
                    n_event,
                },
                info: "generated".to_string(),
                start: start_time,
                end: end_time,
                data: hist,
            }
        })
        .collect()
}
