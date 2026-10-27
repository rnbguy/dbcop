use std::fs;
use std::io::{self, Write};
use std::process::ExitCode;

use clap::Parser;
use color_eyre::eyre::{eyre, Result, WrapErr};
use dbcop_cli::{App, Command};
use tracing_subscriber::EnvFilter;

fn main() -> Result<ExitCode> {
    color_eyre::install()?;
    tracing_subscriber::fmt()
        .with_env_filter(EnvFilter::from_default_env())
        .try_init()
        .map_err(|error| eyre!(error))
        .wrap_err("Failed to initialize logging")?;

    let app = App::parse();
    match &app.command {
        Command::Generate(args) => {
            dbcop_cli::generate_histories(args)?;
            Ok(ExitCode::SUCCESS)
        }
        Command::Verify(args) => Ok(if dbcop_cli::verify_histories(args)? {
            ExitCode::FAILURE
        } else {
            ExitCode::SUCCESS
        }),
        Command::Fmt(args) => fmt(args),
        Command::Schema => {
            schema()?;
            Ok(ExitCode::SUCCESS)
        }
    }
}

fn fmt(args: &dbcop_cli::FmtArgs) -> Result<ExitCode> {
    let mut hist_files: Vec<std::path::PathBuf> = Vec::new();

    for path in &args.paths {
        if path.is_file() {
            hist_files.push(path.clone());
        } else if path.is_dir() {
            // Recursively find all .hist files in the directory.
            collect_hist_files(path, &mut hist_files)?;
        } else {
            return Err(eyre!("Path not found: {}", path.display()));
        }
    }

    let mut reformatted = 0usize;
    let mut output = io::stdout().lock();

    for path in &hist_files {
        let content = fs::read_to_string(path)
            .wrap_err_with(|| format!("Failed to read {}", path.display()))?;

        let sessions = dbcop_parser::parse_history(&content)
            .wrap_err_with(|| format!("Failed to parse {}", path.display()))?;

        let formatted = dbcop_core::history::raw::display::format_history(&sessions);

        if formatted != content {
            reformatted += 1;
            if args.check {
                writeln!(output, "Would reformat: {}", path.display())?;
            } else {
                fs::write(path, &formatted)
                    .wrap_err_with(|| format!("Failed to write {}", path.display()))?;
                writeln!(output, "Reformatted: {}", path.display())?;
            }
        }
    }

    if args.check {
        if reformatted > 0 {
            writeln!(output, "{reformatted} file(s) would be reformatted")?;
            return Ok(ExitCode::FAILURE);
        }
        writeln!(output, "All files are correctly formatted")?;
    } else {
        writeln!(output, "{reformatted} file(s) reformatted")?;
    }
    Ok(ExitCode::SUCCESS)
}

fn schema() -> Result<()> {
    use dbcop_core::history::raw::types::Transaction;
    let schema = schemars::schema_for!(Vec<Vec<Transaction<u64, u64>>>);
    let mut output = io::stdout().lock();
    serde_json::to_writer_pretty(&mut output, &schema).wrap_err("Failed to serialize schema")?;
    writeln!(output).wrap_err("Failed to write schema")?;
    Ok(())
}

fn collect_hist_files(dir: &std::path::Path, out: &mut Vec<std::path::PathBuf>) -> Result<()> {
    let entries = fs::read_dir(dir)
        .wrap_err_with(|| format!("Failed to read directory {}", dir.display()))?;
    let mut entries: Vec<_> = entries
        .collect::<Result<_, _>>()
        .wrap_err_with(|| format!("Failed to read directory entry in {}", dir.display()))?;
    entries.sort_by_key(fs::DirEntry::path);
    for entry in entries {
        let path = entry.path();
        if path.is_dir() {
            collect_hist_files(&path, out)?;
        } else if path.extension().is_some_and(|e| e == "hist") {
            out.push(path);
        }
    }
    Ok(())
}
