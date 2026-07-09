#[allow(dead_code)]
mod flavors;

use std::alloc::{alloc, dealloc, Layout};
use std::cell::RefCell;
use std::slice;

use bincode::Options;
use resharp::dump::RegexDump;
use resharp::{PrefixKind, LDFA};
use resharp_algebra::nulls::{NullState, EID_ALWAYS0, EID_BEGIN0, EID_CENTER0, EID_END0, EID_NONE};
use serde::{Deserialize, Serialize};

#[derive(Deserialize)]
struct Request {
    pattern: String,
    ascii: bool,
    #[serde(default)]
    flavor: Flavor,
}

#[derive(Deserialize, Default, PartialEq, Eq)]
#[serde(rename_all = "lowercase")]
enum Flavor {
    #[default]
    Resharp,
    Re2,
    Rust,
}

#[derive(Serialize)]
struct ErrorResponse {
    message: String,
}

fn bincode_cfg() -> impl bincode::Options {
    bincode::DefaultOptions::new()
        .with_fixint_encoding()
        .with_little_endian()
}

fn resolve_effects(effects: &[Vec<NullState>], eid: u16) -> Vec<(u8, u32)> {
    match eid as u32 {
        EID_NONE => vec![],
        EID_CENTER0 => vec![(resharp_algebra::nulls::Nullability::CENTER.0, 0)],
        EID_ALWAYS0 => vec![(resharp_algebra::nulls::Nullability::ALWAYS.0, 0)],
        EID_BEGIN0 => vec![(resharp_algebra::nulls::Nullability::BEGIN.0, 0)],
        EID_END0 => vec![(resharp_algebra::nulls::Nullability::END.0, 0)],
        _ => effects[eid as usize]
            .iter()
            .map(|n| (n.mask.0, n.rel))
            .collect(),
    }
}

fn write_bytes(out: &mut Vec<u8>, bytes: &[u8]) {
    out.extend_from_slice(&(bytes.len() as u32).to_le_bytes());
    out.extend_from_slice(bytes);
}

fn literal_needle(fwd_prefix_search: &impl Serialize) -> Result<Vec<u8>, String> {
    let val = serde_json::to_value(fwd_prefix_search)
        .map_err(|e| format!("serialize literal prefix: {e}"))?;
    let needle = val
        .get("Literal")
        .and_then(|v| v.get("needle"))
        .and_then(|v| v.as_array())
        .ok_or("literal prefix JSON missing Literal.needle array")?;
    needle
        .iter()
        .map(|b| {
            b.as_u64()
                .and_then(|n| u8::try_from(n).ok())
                .ok_or_else(|| "literal prefix needle byte out of range".to_string())
        })
        .collect()
}

fn write_ldfa(out: &mut Vec<u8>, ldfa: &LDFA) {
    out.extend_from_slice(&ldfa.mt_log.to_le_bytes());
    out.extend_from_slice(&ldfa.mt_lookup);

    out.extend_from_slice(&(ldfa.begin_table.len() as u32).to_le_bytes());
    for &s in &ldfa.begin_table {
        out.extend_from_slice(&s.to_le_bytes());
    }

    out.extend_from_slice(&(ldfa.center_table.len() as u32).to_le_bytes());
    for &s in &ldfa.center_table {
        out.extend_from_slice(&s.to_le_bytes());
    }

    out.extend_from_slice(&(ldfa.effects_id.len() as u32).to_le_bytes());
    for &eid in &ldfa.effects_id {
        let resolved = resolve_effects(&ldfa.effects, eid);
        out.extend_from_slice(&(resolved.len() as u16).to_le_bytes());
        for (mask, rel) in resolved {
            out.push(mask);
            out.extend_from_slice(&rel.to_le_bytes());
        }
    }
}

fn translate_pattern(pattern: &str, flavor: &Flavor) -> Result<String, String> {
    match flavor {
        Flavor::Resharp => Ok(pattern.to_string()),
        Flavor::Re2 => flavors::translate::re2_to_resharp(pattern)
            .map_err(|e| format!("translate RE2 pattern: {e}")),
        Flavor::Rust => flavors::translate::rust_regex_to_resharp(pattern)
            .map_err(|e| format!("translate Rust regex-crate pattern: {e}")),
    }
}

fn export(input: &str) -> Result<Vec<u8>, String> {
    let req: Request = serde_json::from_str(input).map_err(|e| format!("parse request: {e}"))?;
    if req.pattern.is_empty() {
        return Err("pattern is empty".into());
    }
    let pattern = translate_pattern(&req.pattern, &req.flavor)?;

    let re1 = compile_for(&pattern, req.ascii, false)?;

    let has_bounded = re1.bdfa_stats().is_some();
    let find_all = re1.find_all_kind_name();
    let literal_fwd_prefix = find_all == "FwdPrefix"
        && re1
            .fwd_prefix_kind()
            .is_some_and(|(kind, _)| kind == "Literal");

    if !has_bounded && literal_fwd_prefix {
        let dump = dump_bytes(&re1)?;
        if let Some(PrefixKind::AnchoredFwd(fp)) = &dump.prefix {
            if dump.neg_lb.is_some() {
                return Err("pattern unexpectedly has negative lookbehind".into());
            }
            let needle = literal_needle(fp)?;
            let lang_is_prefix_literal = dump.fixed_length == Some(fp.len() as u32)
                && !dump.has_anchors
                && !dump.has_la
                && dump.neg_lb.is_none();
            let fwd = dump.fwd.ok_or("fwd LDFA missing")?;
            let mut out = vec![1u8];
            out.push(if lang_is_prefix_literal { 1 } else { 0 });
            write_bytes(&mut out, &needle);
            write_ldfa(&mut out, &fwd);
            return Ok(out);
        }
        return Err(
            "fwd_prefix_kind reported Literal but prefix wasn't AnchoredFwd(Literal)".into(),
        );
    }

    if !has_bounded && matches!(find_all, "Dfa" | "Hardened") {
        return export_plain(dump_bytes(&re1)?);
    }

    let re2 = compile_for(&pattern, req.ascii, true)?;
    if re2.bdfa_stats().is_some() || !matches!(re2.find_all_kind_name(), "Dfa" | "Hardened") {
        return Err(format!(
            "pattern's chosen find_all strategy is {} (bounded={}) even with prefixes disabled, not supported by this exporter",
            re2.find_all_kind_name(),
            re2.bdfa_stats().is_some(),
        ));
    }
    export_plain(dump_bytes(&re2)?)
}

fn compile_for(
    pattern: &str,
    ascii: bool,
    disable_prefixes: bool,
) -> Result<resharp::Regex, String> {
    let opts = resharp::RegexOptions {
        unicode: if ascii {
            resharp::UnicodeMode::Ascii
        } else {
            resharp::UnicodeMode::Default
        },
        disable_prefixes,
        ..resharp::RegexOptions::default().multiline(false)
    };
    resharp::Regex::with_options(pattern, opts).map_err(|e| format!("compile pattern: {e}"))
}

fn dump_bytes(re: &resharp::Regex) -> Result<RegexDump, String> {
    let bytes = re.dump().map_err(|e| format!("dump: {e}"))?;
    bincode_cfg()
        .deserialize(&bytes)
        .map_err(|e| format!("bincode decode: {e}"))
}

fn export_plain(dump: RegexDump) -> Result<Vec<u8>, String> {
    if dump.neg_lb.is_some() {
        return Err("pattern unexpectedly has negative lookbehind".into());
    }
    let fwd = dump.fwd.ok_or("fwd LDFA missing")?;
    let rev_ts = dump.rev_ts.ok_or("rev_ts LDFA missing")?;
    let mut out = vec![0u8];
    write_ldfa(&mut out, &fwd);
    write_ldfa(&mut out, &rev_ts);
    Ok(out)
}

thread_local! {
    static REGEXES: RefCell<Vec<Option<resharp::Regex>>> = const { RefCell::new(Vec::new()) };
    static LAST_ERR: RefCell<Vec<u8>> = const { RefCell::new(Vec::new()) };
    static OUT: RefCell<Vec<u8>> = const { RefCell::new(Vec::new()) };
}

fn regex_new(header: &str) -> Result<i32, String> {
    let req: Request = serde_json::from_str(header).map_err(|e| format!("parse request: {e}"))?;
    if req.pattern.is_empty() {
        return Err("pattern is empty".into());
    }
    let pattern = translate_pattern(&req.pattern, &req.flavor)?;
    let re = compile_for(&pattern, req.ascii, false)?;
    REGEXES.with(|slab| {
        let mut slab = slab.borrow_mut();
        slab.push(Some(re));
        Ok((slab.len() - 1) as i32)
    })
}

fn regex_find_all(handle: i32, haystack: &[u8]) -> Result<Vec<u8>, String> {
    REGEXES.with(|slab| {
        let slab = slab.borrow();
        let re = slab
            .get(handle as usize)
            .and_then(|slot| slot.as_ref())
            .ok_or_else(|| format!("invalid or freed regex handle {handle}"))?;
        let matches = re
            .find_all(haystack)
            .map_err(|e| format!("find_all: {e}"))?;
        let mut out = Vec::with_capacity(4 + matches.len() * 8);
        out.extend_from_slice(&(matches.len() as u32).to_le_bytes());
        for m in matches {
            out.extend_from_slice(&(m.start as u32).to_le_bytes());
            out.extend_from_slice(&(m.end as u32).to_le_bytes());
        }
        Ok(out)
    })
}

fn regex_free(handle: i32) -> Result<(), String> {
    REGEXES.with(|slab| {
        let mut slab = slab.borrow_mut();
        let slot = slab
            .get_mut(handle as usize)
            .ok_or_else(|| format!("invalid or freed regex handle {handle}"))?;
        if slot.take().is_none() {
            return Err(format!("invalid or freed regex handle {handle}"));
        }
        Ok(())
    })
}

fn set_err(message: impl Into<String>) {
    let raw = serde_json::to_vec(&ErrorResponse {
        message: message.into(),
    })
    .unwrap_or_else(|_| b"{\"message\":\"failed to encode error\"}".to_vec());
    LAST_ERR.with(|s| *s.borrow_mut() = raw);
}

unsafe fn bytes<'a>(ptr: *const u8, len: usize) -> &'a [u8] {
    if len == 0 {
        &[]
    } else {
        slice::from_raw_parts(ptr, len)
    }
}

#[no_mangle]
pub extern "C" fn go_resharp_alloc(len: usize) -> *mut u8 {
    if len == 0 {
        return std::ptr::null_mut();
    }
    let layout = Layout::from_size_align(len, 1).unwrap();
    unsafe { alloc(layout) }
}

#[no_mangle]
pub unsafe extern "C" fn go_resharp_dealloc(ptr: *mut u8, len: usize) {
    if ptr.is_null() || len == 0 {
        return;
    }
    let layout = Layout::from_size_align(len, 1).unwrap();
    dealloc(ptr, layout);
}

#[no_mangle]
pub unsafe extern "C" fn go_resharp_compile(ptr: *const u8, len: usize) -> i32 {
    let Ok(s) = std::str::from_utf8(bytes(ptr, len)) else {
        set_err("request is not valid UTF-8");
        return -1;
    };
    match export(s) {
        Ok(out) => {
            OUT.with(|o| *o.borrow_mut() = out);
            0
        }
        Err(e) => {
            set_err(e);
            -1
        }
    }
}

#[no_mangle]
pub unsafe extern "C" fn go_resharp_regex_new(ptr: *const u8, len: usize) -> i32 {
    let Ok(s) = std::str::from_utf8(bytes(ptr, len)) else {
        set_err("request is not valid UTF-8");
        return -1;
    };
    match regex_new(s) {
        Ok(handle) => handle,
        Err(e) => {
            set_err(e);
            -1
        }
    }
}

#[no_mangle]
pub unsafe extern "C" fn go_resharp_regex_find_all(handle: i32, ptr: *const u8, len: usize) -> i32 {
    match regex_find_all(handle, bytes(ptr, len)) {
        Ok(out) => {
            OUT.with(|o| *o.borrow_mut() = out);
            0
        }
        Err(e) => {
            set_err(e);
            -1
        }
    }
}

#[no_mangle]
pub extern "C" fn go_resharp_regex_free(handle: i32) -> i32 {
    match regex_free(handle) {
        Ok(()) => 0,
        Err(e) => {
            set_err(e);
            -1
        }
    }
}

#[no_mangle]
pub extern "C" fn go_resharp_out_ptr() -> *const u8 {
    OUT.with(|o| o.borrow().as_ptr())
}

#[no_mangle]
pub extern "C" fn go_resharp_out_len() -> usize {
    OUT.with(|o| o.borrow().len())
}

#[no_mangle]
pub extern "C" fn go_resharp_error_ptr() -> *const u8 {
    LAST_ERR.with(|s| s.borrow().as_ptr())
}

#[no_mangle]
pub extern "C" fn go_resharp_error_len() -> usize {
    LAST_ERR.with(|s| s.borrow().len())
}
