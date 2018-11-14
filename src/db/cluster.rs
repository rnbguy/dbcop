use db::history::{HistParams, History, Transaction};
// use verifier::Verifier;

// use std::collections::HashMap;

use std::fs;
use std::fs::File;
use std::io::{BufReader, BufWriter};
use std::path::Path;

use std::net::IpAddr;

// use rand::distributions::{Distribution, Uniform};
// use rand::Rng;
use std::thread;
use std::thread::sleep;
use std::time::Duration;

// use std::convert::From;

// use serde_yaml;

#[derive(Debug, Clone)]
pub struct Node {
    pub ip: IpAddr,
    pub id: usize,
}

pub trait ClusterNode {
    fn exec_session(&self, hist: &mut Vec<Transaction>);
}

pub trait Cluster<N>
where
    N: 'static + Send + ClusterNode,
{
    fn n_node(&self) -> usize;
    fn setup(&self) -> bool;
    fn setup_test(&mut self, p: &HistParams);
    fn get_node(&self, id: usize) -> Node;
    fn get_cluster_node(&self, id: usize) -> N;
    fn cleanup(&self);
    fn info(&self) -> String;

    fn node_vec(ips: &Vec<&str>) -> Vec<Node> {
        ips.iter()
            .enumerate()
            .map(|(i, ip)| Node {
                ip: ip.parse().unwrap(),
                id: i + 1,
            })
            .collect()
    }

    fn execute_all(&mut self, r_dir: &Path, o_dir: &Path, millisec: u64) -> Option<usize> {
        // let histories: Vec<History> = fs::read_dir(r_dir)
        //     .unwrap()
        //     .take(100)
        //     .filter_map(|entry_res| match entry_res {
        //         Ok(ref entry) if !&entry.path().is_dir() => {
        //             let file = File::open(entry.path()).unwrap();
        //             let buf_reader = BufReader::new(file);
        //             Some(serde_json::from_reader(buf_reader).unwrap())
        //         }
        //         _ => None,
        //     })
        //     .collect();

        let histories: Vec<History> = (0..100)
            .flat_map(|id| {
                let filename = format!("hist-{:05}.json", id);
                let file = File::open(r_dir.join(filename)).unwrap();
                let buf_reader = BufReader::new(file);
                serde_json::from_reader(buf_reader)
            })
            .collect();

        for ref history in histories.iter() {
            let curr_dir = o_dir.join(format!("hist-{:05}", history.get_id()));
            fs::create_dir(&curr_dir).expect("couldn't create dir");
            self.execute(history, &curr_dir);
            sleep(Duration::from_millis(millisec));
        }

        None
    }

    fn execute(&mut self, hist: &History, dir: &Path) -> Option<usize> {
        self.setup();

        self.setup_test(hist.get_params());

        let mut exec = hist.get_cloned_data();

        let start_time = chrono::Local::now();

        self.exec_history(&mut exec);

        let end_time = chrono::Local::now();

        self.cleanup();

        let exec_hist = History::new(
            hist.get_cloned_params(),
            self.info(),
            start_time,
            end_time,
            exec,
        );

        let file = File::create(dir.join("history.json")).unwrap();
        let buf_writer = BufWriter::new(file);
        serde_json::to_writer_pretty(buf_writer, &exec_hist).expect("dumping to json went wrong");

        None
    }

    fn exec_history(&self, hist: &mut Vec<Vec<Transaction>>) {
        let mut threads = (0..self.n_node())
            .zip(hist.drain(..))
            .map(|(node_id, mut single_hist)| {
                let cluster_node = self.get_cluster_node(node_id);
                thread::spawn(move || {
                    cluster_node.exec_session(&mut single_hist);
                    single_hist
                })
            })
            .collect::<Vec<_>>();
        hist.extend(threads.drain(..).map(|t| t.join().unwrap()));
    }
}