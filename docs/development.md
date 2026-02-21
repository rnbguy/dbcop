# Development Quickstart

## Prerequisites

- **Rust** (edition 2021, MSRV 1.73.0) with nightly toolchain (required for
  formatting)
- **Deno** (for web app, WASM builds, and TypeScript tooling)
- **taplo** (TOML formatter)
- **typos** (spell checker)

## Build

```bash
# Build all crates
cargo build --workspace

# Verify no_std compatibility of core crate
cargo build -p dbcop_core --no-default-features
```

## Test

```bash
# Run all tests
cargo test --workspace

# Run tests for a specific crate
cargo test -p dbcop_core

# Run WASM integration tests (requires wasmlib built first)
deno task wasmbuild
deno test tests/
```

## Lint and Format

```bash
# Rust formatting (requires nightly)
cargo +nightly fmt --all

# Clippy linting
cargo clippy --workspace -- -D warnings

# TOML formatting
taplo format *.toml

# Deno checks (fmt + lint + type check)
deno task deno:ci
```

## WASM and Web

```bash
# Build WASM bindings
deno task wasmbuild

# Start development server
deno task serve-web

# Build static site for deployment
deno task build
```

## Contributor Guide

For full contributor guidelines, including workflow, branch naming conventions,
CI checks, code constraints, pre-commit hooks, and the update protocol, see
[AGENTS.md](../AGENTS.md).

AGENTS.md covers:

- Git workflow and branch naming (`feat/`, `fix/`, `perf/`, etc.)
- Conventional commit PR titles
- All CI checks (Rust build, format, code quality, Deno)
- Code constraints (no emoji, `no_std` compatibility, serde gates)
- Pre-commit hook details
- Repository structure reference
- CLI and WASM API usage details
- Testing strategy and performance decisions

## See Also

- [Architecture](architecture.md) -- crate structure and data flow
- [CLI Reference](cli-reference.md) -- using the `dbcop` binary
- [Web and WASM](web-and-wasm.md) -- browser-based interface
