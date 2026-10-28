use dbcop_core::history::raw::types::{Event, Transaction};
use dbcop_sat::{check_prefix, check_snapshot_isolation};

#[test]
fn prefix_commit_arbitration_does_not_order_the_read_phases() {
    for version in [46, 49] {
        let history = vec![
            vec![
                Transaction::committed(vec![Event::write("x", 46), Event::read_empty("y")]),
                Transaction::committed(vec![Event::read("z", 56), Event::read("x", version)]),
            ],
            vec![
                Transaction::committed(vec![
                    Event::write("y", 48),
                    Event::read_empty("x"),
                    Event::write("x", 49),
                ]),
                Transaction::committed(vec![Event::write("z", 56)]),
            ],
        ];
        assert!(
            check_prefix(&history).is_ok(),
            "either concurrent writer can arbitrate x={version} without ordering the snapshots"
        );
        assert!(
            check_snapshot_isolation(&history).is_err(),
            "the overlapping x writers must still violate snapshot isolation"
        );
    }
}
