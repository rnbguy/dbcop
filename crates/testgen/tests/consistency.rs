use dbcop_core::{check, Consistency};
use dbcop_testgen::generator::generate_single_history;

#[test]
fn generated_history_satisfies_committed_read() {
    let sessions = generate_single_history(3, 5, 4, 6);
    assert!(!sessions.is_empty());
    check(&sessions, Consistency::CommittedRead)
        .expect("generated history must satisfy committed-read");
}

#[test]
fn generated_histories_satisfy_committed_read_across_params() {
    for n_node in 2..=3 {
        for n_var in 2..=4 {
            for n_txn in 2..=3 {
                let sessions = generate_single_history(n_node, n_var, n_txn, 3);
                assert!(!sessions.is_empty());
                check(&sessions, Consistency::CommittedRead)
                    .expect("generated history must satisfy committed-read for small param grid");
            }
        }
    }
}
