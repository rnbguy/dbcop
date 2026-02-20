use dbcop_core::{check, Consistency};
use dbcop_testgen::generator::generate_single_history;

#[test]
fn generated_history_satisfies_committed_read() {
    let sessions = generate_single_history(3, 5, 4, 6);
    assert!(!sessions.is_empty());
    check(&sessions, Consistency::CommittedRead)
        .expect("generated history must satisfy committed-read");
}
