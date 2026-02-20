use std::{fs, process};

use clap::Parser;
use dbcop_cli::{App, Command};
use tracing_subscriber::EnvFilter;

fn main() {
    tracing_subscriber::fmt()
        .with_env_filter(EnvFilter::from_default_env())
        .init();

    let app = App::parse();
    match &app.command {
        Command::Generate(args) => generate(args),
        Command::Verify(args) => verify(args),
    }
}

fn generate(args: &dbcop_cli::GenerateArgs) {
    fs::create_dir_all(&args.output_dir).unwrap_or_else(|e| {
        eprintln!("Failed to create output directory: {e}");
        process::exit(1);
    });

    let histories = dbcop_testgen::generator::generate_mult_histories(
        args.n_hist,
        args.n_node,
        args.n_var,
        args.n_txn,
        args.n_evt,
    );

    for history in &histories {
        let path = args.output_dir.join(format!("{}.json", history.get_id()));
        let file = fs::File::create(&path).unwrap_or_else(|e| {
            eprintln!("Failed to create {}: {e}", path.display());
            process::exit(1);
        });
        serde_json::to_writer_pretty(file, history).unwrap_or_else(|e| {
            eprintln!("Failed to write {}: {e}", path.display());
            process::exit(1);
        });
    }

    println!(
        "Generated {} histories to {}",
        histories.len(),
        args.output_dir.display()
    );
}

fn verify(args: &dbcop_cli::VerifyArgs) {
    let level = dbcop_core::Consistency::from(args.consistency.clone());
    let mut any_failed = false;

    let mut entries: Vec<_> = fs::read_dir(&args.input_dir)
        .unwrap_or_else(|e| {
            eprintln!("Failed to read input directory: {e}");
            process::exit(1);
        })
        .filter_map(Result::ok)
        .filter(|e| e.path().extension().is_some_and(|ext| ext == "json"))
        .collect();

    entries.sort_by_key(fs::DirEntry::path);

    if entries.is_empty() {
        eprintln!("No .json files found in {}", args.input_dir.display());
        process::exit(1);
    }

    for entry in entries {
        let path = entry.path();
        let filename = path.file_name().unwrap_or_default().to_string_lossy();

        let file = fs::File::open(&path).unwrap_or_else(|e| {
            eprintln!("Failed to open {filename}: {e}");
            process::exit(1);
        });

        let history: dbcop_testgen::generator::History = serde_json::from_reader(file)
            .unwrap_or_else(|e| {
                eprintln!("Failed to parse {filename}: {e}");
                process::exit(1);
            });

        match dbcop_core::check(history.get_data(), level) {
            Ok(witness) => {
                if args.json {
                    let result = serde_json::json!({
                        "file": filename,
                        "ok": true,
                        "witness": witness,
                    });
                    println!("{}", serde_json::to_string(&result).unwrap());
                } else if args.verbose {
                    println!("{filename}: PASS");
                    println!("  witness: {witness:?}");
                } else {
                    println!("{filename}: PASS");
                }
            }
            Err(e) => {
                any_failed = true;
                if args.json {
                    let result = serde_json::json!({
                        "file": filename,
                        "ok": false,
                        "error": e,
                    });
                    println!("{}", serde_json::to_string(&result).unwrap());
                } else if args.verbose {
                    println!("{filename}: FAIL");
                    println!("  error: {e:?}");
                } else {
                    println!("{filename}: FAIL ({e:?})");
                }
            }
        }
    }

    if any_failed {
        process::exit(1);
    }
}
