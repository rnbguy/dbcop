#[path = "../../core/tests/common/axiomatic_reference.rs"]
mod axiomatic_reference;

use axiomatic_reference::{reference_check, validate_witness, ReferenceResult, LEVELS};
use dbcop_core::consistency::check;
use dbcop_core::history::raw::types::Session;
use dbcop_sat::{check_prefix, check_serializable, check_snapshot_isolation};
use serde::Deserialize;

#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
struct SavedCase {
    name: String,
    history: Vec<Session<u64, u64>>,
    expected: [bool; 7],
}

#[test]
fn saved_apalache_corpus_matches_reference_core_and_sat() {
    // Frozen 273 unique histories/prefixes reconstructed from nine saved Apalache profiles.
    let cases: Vec<SavedCase> =
        serde_json::from_str(include_str!("../../core/tests/fixtures/apalache.json"))
            .expect("saved Apalache corpus must decode using the raw history schema");
    assert_eq!(cases.len(), 273, "the frozen saved-history corpus changed");
    let mut counts = [[0_usize; 2]; 3];

    for case in &cases {
        for (index, level) in LEVELS.into_iter().enumerate() {
            let context = format!(
                "case={} level={level:?} expected={:?} history={:?}",
                case.name, case.expected, case.history
            );
            let reference = reference_check(&case.history, level);
            assert_eq!(
                matches!(reference, ReferenceResult::Consistent(_)),
                case.expected[index],
                "frozen label disagrees with independent reference: {context}; reference={reference:?}"
            );

            let core = check(&case.history, level);
            assert_eq!(
                core.is_ok(),
                case.expected[index],
                "core verdict mismatch: {context}; core={core:?}"
            );
            if let Ok(witness) = &core {
                validate_witness(&case.history, level, witness)
                    .unwrap_or_else(|error| panic!("invalid core witness: {context}; {error}"));
            }

            let sat = match index {
                4 => check_prefix(&case.history).map(Some),
                5 => check_snapshot_isolation(&case.history).map(Some),
                // Serializable's public SAT API returns unit, not a witness.
                6 => check_serializable(&case.history).map(|()| None),
                _ => continue,
            };
            assert_eq!(
                sat.is_ok(),
                case.expected[index],
                "SAT verdict mismatch: {context}; sat={sat:?}"
            );
            if let Ok(Some(witness)) = &sat {
                validate_witness(&case.history, level, witness)
                    .unwrap_or_else(|error| panic!("invalid SAT witness: {context}; {error}"));
            }
            counts[index - 4][usize::from(sat.is_ok())] += 1;
        }
    }

    for (level, [no, yes]) in LEVELS[4..].iter().zip(counts) {
        println!("saved Apalache corpus {level:?}: YES={yes} NO={no}");
        assert!(yes > 0 && no > 0, "{level:?} must exercise SAT and UNSAT");
        assert_eq!(yes + no, cases.len(), "{level:?} must cover every case");
    }
}
