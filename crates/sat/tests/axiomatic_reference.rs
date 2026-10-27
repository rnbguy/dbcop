#[path = "../../core/tests/common/axiomatic_reference.rs"]
mod axiomatic_reference;

use axiomatic_reference::{
    directed_histories, for_each_history, malformed_histories, reference_check, validate_witness,
    ReferenceResult,
};
use dbcop_core::consistency::error::Error;
use dbcop_core::consistency::Witness;
use dbcop_core::history::raw::types::Session;
use dbcop_core::Consistency;
use dbcop_sat::{check_prefix, check_serializable, check_snapshot_isolation};

const LEVELS: [Consistency; 3] = [
    Consistency::Prefix,
    Consistency::SnapshotIsolation,
    Consistency::Serializable,
];

fn solve(
    sessions: &[Session<u64, u64>],
    level: Consistency,
) -> Result<Option<Witness>, Error<u64, u64>> {
    match level {
        Consistency::Prefix => check_prefix(sessions).map(Some),
        Consistency::SnapshotIsolation => check_snapshot_isolation(sessions).map(Some),
        // The actual public SAT SER API returns Result<(), Error>, not a
        // witness. Check its decision against the axioms; do not fabricate one.
        Consistency::Serializable => check_serializable(sessions).map(|()| None),
        _ => unreachable!("only the three NPC SAT levels are used here"),
    }
}

fn compare(sessions: &[Session<u64, u64>], domain: &str, ordinal: u64, counts: &mut [[u64; 4]; 3]) {
    for (index, level) in LEVELS.into_iter().enumerate() {
        let reference = reference_check(sessions, level);
        let bucket = match &reference {
            ReferenceResult::Invalid(_) => 0,
            ReferenceResult::Consistent(_) => 1,
            ReferenceResult::Inconsistent => 2,
        };
        counts[index][bucket] += 1;
        let implementation = solve(sessions, level);
        let disagreement = match (&reference, &implementation) {
            (ReferenceResult::Invalid(_), Err(Error::NonAtomic(_)))
            | (ReferenceResult::Inconsistent, Err(_))
            | (ReferenceResult::Consistent(_), Ok(None)) => None,
            (ReferenceResult::Consistent(_), Ok(Some(witness))) => {
                counts[index][3] += 1;
                validate_witness(sessions, level, witness).err()
            }
            _ => Some("reference/checker classification disagreement".into()),
        };
        if let Some(reason) = disagreement {
            panic!(
                "first mismatch: domain={domain}, ordinal={ordinal}, level={level:?}, implementation=sat\n\
                 reason={reason}\nraw={sessions:?}\nreference={reference:?}\nactual={implementation:?}\n\
                 counts_so_far[invalid,consistent,inconsistent,witnesses]={counts:?}"
            );
        }
    }
}

fn report(domain: &str, counts: &[[u64; 4]; 3]) {
    for (level, count) in LEVELS.into_iter().zip(counts) {
        eprintln!(
            "reference domain={domain} implementation=sat level={level:?} invalid={} consistent={} inconsistent={} total={} validated_witnesses={}",
            count[0], count[1], count[2], count[..3].iter().sum::<u64>(), count[3]
        );
    }
}

#[test]
fn sat_matches_primary_axioms_on_directed_and_complete_bounded_domain() {
    let mut directed_counts = [[0; 4]; 3];
    for (ordinal, case) in directed_histories().into_iter().enumerate() {
        for (level, expected_index) in LEVELS.into_iter().zip([4, 5, 6]) {
            let reference = reference_check(&case.history, level);
            assert_eq!(
                matches!(reference, ReferenceResult::Consistent(_)),
                case.admitted[expected_index],
                "independently stated expectation: case={} level={level:?} raw={:?} reference={reference:?}",
                case.name, case.history
            );
            assert!(
                !matches!(reference, ReferenceResult::Invalid(_)),
                "{}: {reference:?}",
                case.name
            );
        }
        compare(
            &case.history,
            case.name,
            u64::try_from(ordinal).unwrap(),
            &mut directed_counts,
        );
    }
    report("directed", &directed_counts);

    let mut malformed_counts = [[0; 4]; 3];
    for (ordinal, (name, history)) in malformed_histories().into_iter().enumerate() {
        for level in LEVELS {
            assert!(
                matches!(
                    reference_check(&history, level),
                    ReferenceResult::Invalid(_)
                ),
                "{name}: {history:?}"
            );
        }
        compare(
            &history,
            name,
            u64::try_from(ordinal).unwrap(),
            &mut malformed_counts,
        );
    }
    report("directed-malformed", &malformed_counts);

    let mut bounded_counts = [[0; 4]; 3];
    let mut ordinal = 0;
    for_each_history(|history| {
        compare(
            history,
            "sessions<=2,transactions<=3,events<=4,variables={0,1}",
            ordinal,
            &mut bounded_counts,
        );
        ordinal += 1;
    });
    report(
        "sessions<=2,transactions<=3,events<=4,variables={0,1}",
        &bounded_counts,
    );
    assert_eq!(
        ordinal, 253_537,
        "complete labeled finite domain cardinality"
    );
    for count in bounded_counts {
        assert!(count[0] > 0 && count[1] > 0 && count[2] > 0);
    }
}
