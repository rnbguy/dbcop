use dbcop_cli::{generate_histories, verify_histories, ConsistencyLevel, GenerateArgs, VerifyArgs};

fn unique_dir(tag: &str) -> std::path::PathBuf {
    let dir = std::env::temp_dir().join(format!(
        "dbcop-cli-test-{}-{}",
        tag,
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .expect("clock should advance")
            .as_nanos()
    ));
    std::fs::create_dir_all(&dir).expect("temp dir should be created");
    dir
}

#[test]
fn generate_then_verify_committed_read_passes() {
    let dir = unique_dir("gen-verify");
    let args = GenerateArgs {
        n_hist: 5,
        n_node: 3,
        n_var: 4,
        n_txn: 3,
        n_evt: 3,
        output_dir: dir.clone(),
    };
    let count = generate_histories(&args).expect("generate should succeed");
    assert_eq!(count, 5);
    let files: Vec<_> = std::fs::read_dir(&dir)
        .expect("output dir should be readable")
        .filter_map(Result::ok)
        .filter(|e| e.path().extension().is_some_and(|ext| ext == "json"))
        .collect();
    assert_eq!(files.len(), 5);

    let verify = VerifyArgs {
        input_dir: dir,
        consistency: ConsistencyLevel::CommittedRead,
        verbose: false,
        json: false,
    };
    let any_failed = verify_histories(&verify).expect("verify should succeed");
    assert!(!any_failed);
}

#[test]
fn verify_empty_dir_returns_error() {
    let dir = unique_dir("empty");
    let verify = VerifyArgs {
        input_dir: dir,
        consistency: ConsistencyLevel::CommittedRead,
        verbose: false,
        json: false,
    };
    assert!(verify_histories(&verify).is_err());
}
