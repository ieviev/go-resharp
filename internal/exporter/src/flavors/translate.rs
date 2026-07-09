#[derive(Debug, Clone, PartialEq, Eq)]
pub struct TranslateError {
    pub message: String,
}

impl std::fmt::Display for TranslateError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str(&self.message)
    }
}
impl std::error::Error for TranslateError {}

pub(crate) fn err(message: impl Into<String>) -> TranslateError {
    TranslateError {
        message: message.into(),
    }
}

#[derive(Debug, Clone, Copy, Default, PartialEq, Eq)]
pub struct RustFlags {
    pub case_insensitive: bool,
    pub multi_line: bool,
    pub dot_matches_new_line: bool,
    pub ascii: bool,
}

pub fn parse_rust_flags(flags: &str) -> Result<RustFlags, TranslateError> {
    let mut f = RustFlags::default();
    for c in flags.chars() {
        match c {
            'i' => f.case_insensitive = true,
            'm' => f.multi_line = true,
            's' => f.dot_matches_new_line = true,
            'a' => f.ascii = true,
            'U' => {
                return Err(err(
                    "swap_greed has no sound RE# equivalent (leftmost-longest engine)",
                ))
            }
            'x' => {
                return Err(err(
                    "verbose/ignore_whitespace mode not supported by this translator",
                ))
            }
            other => return Err(err(format!("unknown synthetic flag '{other}'"))),
        }
    }
    Ok(f)
}

fn is_resharp_only_meta(b: u8) -> bool {
    matches!(b, b'&' | b'~' | b'_')
}

fn valid_quantifier_brace(src: &[u8], i: usize) -> Option<usize> {
    debug_assert_eq!(src[i], b'{');
    let mut j = i + 1;
    while j < src.len() && src[j].is_ascii_whitespace() {
        j += 1;
    }
    let start_digits = j;
    while j < src.len() && src[j].is_ascii_digit() {
        j += 1;
    }
    if j == start_digits {
        return None;
    }
    while j < src.len() && src[j].is_ascii_whitespace() {
        j += 1;
    }
    if j < src.len() && src[j] == b',' {
        j += 1;
        if j < src.len() && src[j] == b'}' {
            return Some(j + 1);
        }
        while j < src.len() && src[j].is_ascii_whitespace() {
            j += 1;
        }
        let start_digits2 = j;
        while j < src.len() && src[j].is_ascii_digit() {
            j += 1;
        }
        if j == start_digits2 {
            return None;
        }
        while j < src.len() && src[j].is_ascii_whitespace() {
            j += 1;
        }
    }
    if j < src.len() && src[j] == b'}' {
        Some(j + 1)
    } else {
        None
    }
}

fn named_group_header_end(src: &[u8], i: usize) -> Option<usize> {
    let rest = &src[i..];
    let prefix_len = if rest.starts_with(b"(?P<") {
        4
    } else if rest.starts_with(b"(?<") && !rest.starts_with(b"(?<=") && !rest.starts_with(b"(?<!") {
        3
    } else {
        return None;
    };
    let mut j = i + prefix_len;
    while j < src.len() && src[j] != b'>' {
        j += 1;
    }
    if j < src.len() {
        Some(j + 1)
    } else {
        None
    }
}

fn uses_inline_verbose_mode(src: &[u8]) -> bool {
    let mut i = 0;
    while i + 1 < src.len() {
        if src[i] == b'(' && src[i + 1] == b'?' {
            let mut j = i + 2;
            let mut saw_x = false;
            let mut any_flag_char = false;
            while j < src.len() && matches!(src[j], b'a'..=b'z' | b'A'..=b'Z' | b'-') {
                any_flag_char = true;
                if src[j] == b'x' {
                    saw_x = true;
                }
                j += 1;
            }
            if any_flag_char && j < src.len() && matches!(src[j], b')' | b':') && saw_x {
                return true;
            }
        }
        i += 1;
    }
    false
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) enum Dialect {
    Rust,
    Re2,
}

pub fn rust_regex_to_resharp(pattern: &str) -> Result<String, TranslateError> {
    translate_core(pattern, Dialect::Rust)
}

pub use super::re2::re2_to_resharp;

pub(crate) fn translate_core(pattern: &str, dialect: Dialect) -> Result<String, TranslateError> {
    if uses_inline_verbose_mode(pattern.as_bytes()) {
        return Err(err(
            "inline (?x...) verbose/whitespace mode not supported by this translator",
        ));
    }
    let src = pattern.as_bytes();
    let mut out = String::with_capacity(src.len() + 8);
    let mut depth: u32 = 0;
    let mut just_quantified = false;
    let mut i = 0usize;
    while i < src.len() {
        let b = src[i];

        if b == b'\\' {
            if i + 1 >= src.len() {
                return Err(err("trailing unescaped backslash"));
            }
            if dialect == Dialect::Re2 {
                if let Some((text, new_i)) = super::re2::handle_re2_escape(src, i, depth)? {
                    out.push_str(&text);
                    i = new_i;
                    just_quantified = false;
                    continue;
                }
            }
            // Copy the escape verbatim (both grammars agree on escape
            // sequences; already-escaped &/~/_  e.g. `\&` need no further
            // escaping - a second backslash would change the meaning).
            let n = utf8_char_len(src, i + 1);
            out.push_str(
                std::str::from_utf8(&src[i..i + 1 + n])
                    .map_err(|_| err("invalid utf-8 after backslash"))?,
            );
            i += 1 + n;
            just_quantified = false;
            continue;
        }

        if depth == 0 {
            if let Some(end) = named_group_header_end(src, i) {
                out.push_str(
                    std::str::from_utf8(&src[i..end])
                        .map_err(|_| err("invalid utf-8 in group name"))?,
                );
                i = end;
                just_quantified = false;
                continue;
            }
            if b == b'{' {
                if let Some(end) = valid_quantifier_brace(src, i) {
                    let slice = std::str::from_utf8(&src[i..end]).unwrap();
                    out.extend(slice.chars().filter(|c| !c.is_ascii_whitespace()));
                    i = end;
                    just_quantified = true;
                    continue;
                }
            }
            if matches!(b, b'*' | b'+') {
                out.push(b as char);
                i += 1;
                just_quantified = true;
                continue;
            }
            if b == b'?' {
                if just_quantified {
                    // Laziness marker on the quantifier just emitted. Lazy
                    // and greedy quantifiers are not equivalent - reject
                    // rather than silently reinterpret.
                    return Err(err(
                        "lazy quantifiers are not supported (not equivalent to greedy)",
                    ));
                }
                out.push('?');
                i += 1;
                just_quantified = true;
                continue;
            }
        }

        if b == b'[' {
            depth += 1;
            out.push('[');
            i += 1;
            just_quantified = false;
            continue;
        }
        if b == b']' && depth > 0 {
            depth -= 1;
            out.push(']');
            i += 1;
            just_quantified = false;
            continue;
        }
        if depth == 0 && is_resharp_only_meta(b) {
            out.push('\\');
            out.push(b as char);
            i += 1;
            just_quantified = false;
            continue;
        }
        let n = utf8_char_len(src, i);
        out.push_str(std::str::from_utf8(&src[i..i + n]).map_err(|_| err("invalid utf-8"))?);
        i += n;
        just_quantified = false;
    }
    if depth != 0 {
        return Err(err("unbalanced character class brackets"));
    }
    Ok(out)
}

fn utf8_char_len(src: &[u8], i: usize) -> usize {
    let b = src[i];
    let n = if b < 0x80 {
        1
    } else if b < 0xc0 {
        1 // stray continuation byte, treat defensively as 1
    } else if b < 0xe0 {
        2
    } else if b < 0xf0 {
        3
    } else {
        4
    };
    n.min(src.len() - i)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn passthrough_ordinary_pattern() {
        assert_eq!(
            rust_regex_to_resharp(r"[a-z0-9]+\.[a-z]{2,4}").unwrap(),
            r"[a-z0-9]+\.[a-z]{2,4}"
        );
    }

    #[test]
    fn escapes_bare_metachars_outside_class() {
        assert_eq!(rust_regex_to_resharp("my_var").unwrap(), r"my\_var");
        assert_eq!(rust_regex_to_resharp("a&b").unwrap(), r"a\&b");
        assert_eq!(rust_regex_to_resharp("a~b").unwrap(), r"a\~b");
    }

    #[test]
    fn leaves_class_set_ops_untouched() {
        assert_eq!(rust_regex_to_resharp("[a-y&&xyz]").unwrap(), "[a-y&&xyz]");
        assert_eq!(rust_regex_to_resharp("[0-9--4]").unwrap(), "[0-9--4]");
        assert_eq!(rust_regex_to_resharp("[a-g~~b-h]").unwrap(), "[a-g~~b-h]");
        assert_eq!(rust_regex_to_resharp("[_a-z]").unwrap(), "[_a-z]");
    }

    #[test]
    fn leaves_already_escaped_metachars_untouched() {
        assert_eq!(rust_regex_to_resharp(r"a\&b").unwrap(), r"a\&b");
        assert_eq!(rust_regex_to_resharp(r"a\_b").unwrap(), r"a\_b");
    }

    #[test]
    fn nested_classes_track_depth() {
        assert_eq!(rust_regex_to_resharp("[x[^yz]]_").unwrap(), r"[x[^yz]]\_");
    }

    #[test]
    fn named_groups_passthrough() {
        assert_eq!(
            rust_regex_to_resharp("(?P<name>a_b)").unwrap(),
            r"(?P<name>a\_b)"
        );
        assert_eq!(
            rust_regex_to_resharp("(?<name>a_b)").unwrap(),
            r"(?<name>a\_b)"
        );
    }

    #[test]
    fn lazy_quantifiers_rejected_not_lowered() {
        // Lazy and greedy quantifiers are not equivalent - must be a hard
        // translate error, never a silent rewrite to greedy.
        assert!(rust_regex_to_resharp("a*?").is_err());
        assert!(rust_regex_to_resharp("a+?").is_err());
        assert!(rust_regex_to_resharp("a??").is_err());
        assert!(rust_regex_to_resharp("a{2,4}?").is_err());
        assert!(rust_regex_to_resharp("a{2,}?b").is_err());
    }

    #[test]
    fn group_names_with_underscore_untouched() {
        assert_eq!(
            rust_regex_to_resharp("(?P<foo_bar>x)").unwrap(),
            "(?P<foo_bar>x)"
        );
        assert_eq!(
            rust_regex_to_resharp("(?<foo_bar>x)").unwrap(),
            "(?<foo_bar>x)"
        );
    }

    #[test]
    fn lookbehind_not_mistaken_for_named_group() {
        assert_eq!(rust_regex_to_resharp("(?<=a_b)").unwrap(), r"(?<=a\_b)");
        assert_eq!(rust_regex_to_resharp("(?<!a_b)").unwrap(), r"(?<!a\_b)");
    }

    #[test]
    fn literal_brace_not_a_quantifier() {
        // `{,4}` has no leading digit before the comma, so
        // `valid_quantifier_brace` correctly refuses to treat it as a
        // repetition and this translator passes it through byte-for-byte.
        // IMPORTANT: unlike `&`/`~`/`_`, a malformed `{...}` is NOT actually
        // valid literal syntax in either grammar - `regex-syntax`'s AST
        // parser unconditionally attempts to parse a counted repetition as
        // soon as it sees `{` after a repeatable atom and hard-errors if the
        // shape is invalid (confirmed via a standalone
        // `regex_syntax::ast::parse::Parser` probe and cross-checked against
        // resharp directly: `a{,4}`, `a{,4}?`, and `a{b}` are ALL rejected
        // by both `regex::Regex::new` and `resharp::Regex::new` identically,
        // with matching error kinds). So this is not a place where the
        // translator needs to produce a *valid* pattern - passing the bytes
        // through unchanged is fine precisely because both engines will
        // reject the result the same way, which cannot produce a false
        // divergence. This test only pins down the translator's mechanical
        // behavior (does not claim the output is valid RE#), and also
        // exercises that the trailing `?` is treated as an ordinary greedy
        // optional (not a laziness marker) since `just_quantified` is only
        // set after a genuine quantifier, not after a literal `}`.
        assert_eq!(rust_regex_to_resharp("a{,4}?").unwrap(), "a{,4}?");
    }

    #[test]
    fn whitespace_inside_quantifier_braces_is_stripped_not_rejected() {
        // `regex-syntax`'s `parse_decimal` always tolerates whitespace
        // around the counts in `{n,m}`, regardless of the `x` flag - this
        // is valid, non-verbose Rust regex-crate syntax. resharp does not
        // accept the whitespace, so the translator must normalize it away
        // rather than reject the pattern or copy it verbatim.
        assert_eq!(rust_regex_to_resharp("a{1, 5}").unwrap(), "a{1,5}");
        assert_eq!(rust_regex_to_resharp("a{ 1 , 5 }").unwrap(), "a{1,5}");
        assert_eq!(rust_regex_to_resharp("a{1,}").unwrap(), "a{1,}");
        assert_eq!(rust_regex_to_resharp("a{ 3 }").unwrap(), "a{3}");
        // `{1, }` (whitespace before the closing brace in the AtLeast
        // shorthand) is invalid even in real `regex-syntax` - not a valid
        // quantifier at all, so `{` must fall back to being treated as an
        // ordinary literal character (still succeeds overall, just not as
        // a repetition).
        assert_eq!(rust_regex_to_resharp("a{1, }").unwrap(), "a{1, }");
    }

    #[test]
    fn inline_verbose_mode_rejected_not_mistranslated() {
        assert!(rust_regex_to_resharp("(?x) a # comment with a stray [ bracket\nb").is_err());
        assert!(rust_regex_to_resharp("(?xs)\na").is_err());
        assert!(rust_regex_to_resharp("(?x:a b)").is_err());
        assert!(rust_regex_to_resharp("(?i)abc").is_ok());
    }

    #[test]
    fn unbalanced_brackets_rejected() {
        assert!(rust_regex_to_resharp("[a-z").is_err());
    }

    #[test]
    fn flags_parse() {
        let f = parse_rust_flags("ims").unwrap();
        assert!(f.case_insensitive && f.multi_line && f.dot_matches_new_line && !f.ascii);
        assert!(parse_rust_flags("U").is_err());
        assert!(parse_rust_flags("x").is_err());
    }

    #[test]
    fn flags_parse_ascii_and_unknown() {
        let f = parse_rust_flags("a").unwrap();
        assert!(f.ascii && !f.case_insensitive && !f.multi_line && !f.dot_matches_new_line);
        assert_eq!(parse_rust_flags("").unwrap(), RustFlags::default());
        assert!(parse_rust_flags("q").is_err());
    }

    #[test]
    fn empty_pattern_passes_through() {
        assert_eq!(rust_regex_to_resharp("").unwrap(), "");
    }

    #[test]
    fn trailing_lone_backslash_is_error() {
        assert!(rust_regex_to_resharp("a\\").is_err());
    }

    #[test]
    fn multiple_bare_metachars_all_escaped() {
        assert_eq!(
            rust_regex_to_resharp("a_b&c~d_e").unwrap(),
            r"a\_b\&c\~d\_e"
        );
    }

    #[test]
    fn bare_metachar_immediately_after_group_close() {
        assert_eq!(rust_regex_to_resharp("(a)_b").unwrap(), r"(a)\_b");
    }
}
