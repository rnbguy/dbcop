use dbcop_core::consistency::incremental::IncrementalChecker;
use dbcop_core::history::raw::types::{Event, Transaction};
use dbcop_core::{check, Consistency};

const fn read(variable: u64, version: Option<u64>) -> Event<u64, u64> {
    Event::Read { variable, version }
}

#[test]
fn concurrent_write_skew_remains_readable_after_both_writers_become_visible() {
    // Minimized from unchanged SmallBank: A and B read each other's initial
    // values, so inferred write order must not make either causally visible
    // to the other. Both A.r,B.r,A.w,B.w,C.r,C.w,D.r,D.w and the order swapping
    // A.w/B.w are valid Prefix witnesses for D's corresponding x read.
    for level in [Consistency::Causal, Consistency::Prefix] {
        for version in [46, 49] {
            let mut checker = IncrementalChecker::new(level);
            let mut history = vec![
                vec![Transaction::committed(vec![
                    Event::write(1, 46),
                    read(2, None),
                ])],
                vec![
                    Transaction::committed(vec![
                        Event::write(2, 48),
                        read(1, None),
                        Event::write(1, 49),
                    ]),
                    Transaction::committed(vec![Event::write(3, 56)]),
                ],
            ];
            for (session, transactions) in history.iter().enumerate() {
                for transaction in transactions {
                    assert!(checker
                        .check_transaction(session as u64 + 1, &transaction.events, true)
                        .is_ok());
                }
            }
            assert!(checker.check_read(1, 3, Some(56), true).is_ok());
            for accept in [false, true] {
                assert!(checker.check_read(1, 1, None, accept).is_err());
            }
            history[0].push(Transaction::committed(vec![
                read(3, Some(56)),
                read(1, None),
            ]));
            assert!(check(&history, level).is_err());
            history[0].pop();
            // Trials for both legal orders must leave the retained prefix intact.
            for candidate in [46, 49] {
                assert!(checker.check_read(1, 1, Some(candidate), false).is_ok());
                let events = vec![read(3, Some(56)), read(1, Some(candidate))];
                history[0].push(Transaction::committed(events));
                assert!(check(&history, level).is_ok());
                history[0].pop();
            }
            assert!(checker.check_read(1, 1, Some(version), true).is_ok());
            checker.reset_transaction();
            assert!(checker.check_read(1, 3, Some(56), true).is_ok());
            let events = [read(3, Some(56)), read(1, Some(version))];
            assert!(checker.check_transaction(1, &events, false).is_ok());
            assert!(checker.check_read(1, 3, Some(56), true).is_ok());
            assert!(checker.check_read(1, 1, Some(version), false).is_ok());
            assert!(checker.check_transaction(1, &events, true).is_ok());
        }
    }
}
