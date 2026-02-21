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
        Command::Fmt(args) => fmt(args),
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
        .filter(|e| {
            e.path()
                .extension()
                .is_some_and(|ext| ext == "json" || ext == "hist" || ext == "txt")
        })
        .collect();

    entries.sort_by_key(fs::DirEntry::path);

    if entries.is_empty() {
        eprintln!(
            "No .json, .hist, or .txt files found in {}",
            args.input_dir.display()
        );
        process::exit(1);
    }

    for entry in entries {
        let path = entry.path();
        let filename = path.file_name().unwrap_or_default().to_string_lossy();
        let ext = path
            .extension()
            .map(|e| e.to_string_lossy().into_owned())
            .unwrap_or_default();

        // outcome: Ok(()) = pass, Err(msg) = fail with debug string
        let outcome: Result<dbcop_core::consistency::witness::Witness, String> = if ext == "json" {
            let file = fs::File::open(&path).unwrap_or_else(|e| {
                eprintln!("Failed to open {filename}: {e}");
                process::exit(1);
            });
            let history: dbcop_testgen::generator::History = serde_json::from_reader(file)
                .unwrap_or_else(|e| {
                    eprintln!("Failed to parse {filename}: {e}");
                    process::exit(1);
                });
            dbcop_core::check(history.get_data(), level).map_err(|e| format!("{e:?}"))
        } else {
            // .hist or .txt
            let content = fs::read_to_string(&path).unwrap_or_else(|e| {
                eprintln!("Failed to read {filename}: {e}");
                process::exit(1);
            });
            let sessions = dbcop_core::history::raw::parser::parse_history(&content)
                .unwrap_or_else(|e| {
                    eprintln!("Failed to parse {filename}: {e}");
                    process::exit(1);
                });
            dbcop_core::check(&sessions, level).map_err(|e| format!("{e:?}"))
        };

        match outcome {
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
                    println!("  error: {e}");
                } else {
                    println!("{filename}: FAIL ({e})");
                }
            }
        }
    }

    if any_failed {
        process::exit(1);
    }
}

fn fmt(args: &dbcop_cli::FmtArgs) {
    let mut hist_files: Vec<std::path::PathBuf> = Vec::new();

    for path in &args.paths {
        if path.is_file() {
            hist_files.push(path.clone());
        } else if path.is_dir() {
            // Recursively find all .hist files in the directory.
            collect_hist_files(path, &mut hist_files);
        } else {
            eprintln!("Path not found: {}", path.display());
            process::exit(1);
        }
    }

    let mut reformatted = 0usize;

    for path in &hist_files {
        let content = fs::read_to_string(path).unwrap_or_else(|e| {
            eprintln!("Failed to read {}: {e}", path.display());
            process::exit(1);
        });

        let sessions =
            dbcop_core::history::raw::parser::parse_history(&content).unwrap_or_else(|e| {
                eprintln!("Failed to parse {}: {e}", path.display());
                process::exit(1);
            });

        let formatted = dbcop_core::history::raw::display::format_history(&sessions);

        if formatted != content {
            reformatted += 1;
            if args.check {
                println!("Would reformat: {}", path.display());
            } else {
                fs::write(path, &formatted).unwrap_or_else(|e| {
                    eprintln!("Failed to write {}: {e}", path.display());
                    process::exit(1);
                });
                println!("Reformatted: {}", path.display());
            }
        }
    }

    if args.check {
        if reformatted > 0 {
            println!("{reformatted} file(s) would be reformatted");
            process::exit(1);
        } else {
            println!("All files are correctly formatted");
        }
    } else {
        println!("{reformatted} file(s) reformatted");
    }
}

fn collect_hist_files(dir: &std::path::Path, out: &mut Vec<std::path::PathBuf>) {
    let entries = fs::read_dir(dir).unwrap_or_else(|e| {
        eprintln!("Failed to read directory {}: {e}", dir.display());
        process::exit(1);
    });
    let mut entries: Vec<_> = entries.filter_map(Result::ok).collect();
    entries.sort_by_key(fs::DirEntry::path);
    for entry in entries {
        let path = entry.path();
        if path.is_dir() {
            collect_hist_files(&path, out);
        } else if path.extension().is_some_and(|e| e == "hist") {
            out.push(path);
        }
    }
}
