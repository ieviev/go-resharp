#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/../internal/exporter"
export RUSTUP_HOME="${RUSTUP_HOME:-$HOME/.rustup-tmp}"
export CARGO_HOME="${CARGO_HOME:-$HOME/.cargo-tmp}"
cargo fmt
cargo fmt -- --check
RUSTFLAGS='-C target-feature=+simd128' cargo build --release --target wasm32-unknown-unknown
cp target/wasm32-unknown-unknown/release/go_resharp_exporter.wasm ../../compiler/internal/wasm/dfa_export.wasm
ls -la ../../compiler/internal/wasm/dfa_export.wasm
