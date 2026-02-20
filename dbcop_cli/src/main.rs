use clap::Parser;
use dbcop::{App, Command};

fn main() {
    let app = App::parse();
    match app.command {
        Command::Generate => todo!("generate subcommand"),
        Command::Verify => todo!("verify subcommand"),
    }
}
