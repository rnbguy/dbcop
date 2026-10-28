use dbcop_core::history::raw::types::{Event, Session, Transaction};
use dbcop_core::{check, Consistency};

fn merged_concurrent_writers(version: Option<u64>) -> Vec<Session<&'static str, u64>> {
    vec![
        vec![
            Transaction::committed(vec![Event::write("x", 46), Event::read_empty("y")]),
            Transaction::committed(vec![
                Event::read("z", 56),
                Event::Read {
                    variable: "x",
                    version,
                },
            ]),
        ],
        vec![
            Transaction::committed(vec![
                Event::write("y", 48),
                Event::read_empty("x"),
                Event::write("x", 49),
            ]),
            Transaction::committed(vec![Event::write("z", 56)]),
        ],
    ]
}

#[test]
fn concurrent_writer_commit_order_is_not_causal_visibility() {
    // Minimized from stock SmallBank: either concurrent x-writer can win.
    for level in [Consistency::Causal, Consistency::Prefix] {
        for version in [46, 49] {
            let history = merged_concurrent_writers(Some(version));
            assert!(
                check(&history, level).is_ok(),
                "{level:?}: commit arbitration of x={version} must not invent a causal dependency"
            );
        }
        assert!(check(&merged_concurrent_writers(None), level).is_err());
    }
}
