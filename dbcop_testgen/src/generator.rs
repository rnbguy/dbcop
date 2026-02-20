use std::collections::HashMap;

use chrono::{DateTime, Duration, Local};
use dbcop_core::history::raw::types::{Event, Session, Transaction};
use rand::distributions::{Distribution, Uniform};
use rand::Rng;
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

#[must_use]
pub fn generate_single_history(
    n_node: u64,
    n_variable: u64,
    n_transaction: u64,
    n_event: u64,
) -> Vec<Session<u64, u64>> {
    let mut counters = HashMap::new();
    let mut random_generator = rand::thread_rng();
    let read_variable_range = Uniform::from(0..n_variable);
    // let jump = (n_variable as f64 / n_node as f64).ceil();
    (0..n_node)
        .map(|_i_node| {
            // let i = i_node * jump;
            // let j = std::cmp::min((i_node + 1) * jump, n_variable);
            // let write_variable_range = Uniform::from(i..j);
            (0..n_transaction)
                .map(|_| Transaction {
                    events: (0..n_event)
                        .map(|_| {
                            if random_generator.gen() {
                                let variable = read_variable_range.sample(&mut random_generator);
                                Event::read_empty(variable)
                            } else {
                                let variable = read_variable_range.sample(&mut random_generator);
                                // let variable = write_variable_range.sample(&mut random_generator);
                                let version = {
                                    let entry = counters.entry(variable).or_default();
                                    *entry += 1;
                                    *entry
                                };
                                Event::write(variable, version)
                            }
                        })
                        .collect(),
                    committed: false,
                })
                .collect::<Vec<_>>()
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
