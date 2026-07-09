use super::translate::{err, translate_core, Dialect, TranslateError};

pub fn re2_to_resharp(pattern: &str) -> Result<String, TranslateError> {
    let quoted = expand_re2_quoting(pattern)?;
    translate_core(&quoted, Dialect::Re2)
}

pub(crate) fn handle_re2_escape(
    src: &[u8],
    i: usize,
    depth: u32,
) -> Result<Option<(String, usize)>, TranslateError> {
    debug_assert_eq!(src[i], b'\\');
    if src[i + 1] == b'C' {
        if depth != 0 {
            return Err(err("\\C is not valid inside a character class"));
        }
        return Ok(Some(("_".to_string(), i + 2)));
    }
    if (b'0'..=b'7').contains(&src[i + 1]) {
        let digits_start = i + 1;
        let mut j = digits_start;
        while j < src.len() && j < digits_start + 3 && (b'0'..=b'7').contains(&src[j]) {
            j += 1;
        }
        let digits = std::str::from_utf8(&src[digits_start..j]).unwrap();
        let value = u32::from_str_radix(digits, 8).map_err(|_| err("invalid octal escape"))?;
        let ch = char::from_u32(value).ok_or_else(|| err("octal escape out of Unicode range"))?;
        return Ok(Some((format!("\\x{{{:x}}}", ch as u32), j)));
    }
    Ok(None)
}

fn is_re2_meta_character(c: char) -> bool {
    matches!(
        c,
        '\\' | '.'
            | '+'
            | '*'
            | '?'
            | '('
            | ')'
            | '|'
            | '['
            | ']'
            | '{'
            | '}'
            | '^'
            | '$'
            | '#'
            | '&'
            | '-'
            | '~'
    )
}

fn expand_re2_quoting(pattern: &str) -> Result<String, TranslateError> {
    if !pattern.contains("\\Q") {
        return Ok(pattern.to_string());
    }
    let mut out = String::with_capacity(pattern.len() + 8);
    let mut chars = pattern.char_indices().peekable();
    while let Some((idx, c)) = chars.next() {
        if c == '\\' {
            if pattern[idx..].starts_with("\\Q") {
                chars.next(); // consume 'Q'
                let quoted_start = idx + 2;
                let quoted_end = pattern[quoted_start..]
                    .find("\\E")
                    .map(|p| quoted_start + p)
                    .unwrap_or(pattern.len());
                for qc in pattern[quoted_start..quoted_end].chars() {
                    if is_re2_meta_character(qc) {
                        out.push('\\');
                    }
                    out.push(qc);
                }
                // Skip forward past the consumed quoted text (and the
                // `\E` if present) by re-driving the char_indices iterator.
                while let Some(&(next_idx, _)) = chars.peek() {
                    if next_idx < quoted_end {
                        chars.next();
                    } else {
                        break;
                    }
                }
                if pattern[quoted_end..].starts_with("\\E") {
                    chars.next(); // consume '\\'
                    chars.next(); // consume 'E'
                }
                continue;
            }
            // An ordinary escape: copy the backslash and whatever follows
            // verbatim so we don't misinterpret e.g. `\\Q` (an escaped
            // literal backslash followed by a literal `Q`) as a quote
            // opener.
            out.push('\\');
            if let Some((_, nc)) = chars.next() {
                out.push(nc);
            } else {
                return Err(err("trailing unescaped backslash"));
            }
            continue;
        }
        out.push(c);
    }
    Ok(out)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn re2_dot_c_maps_to_bare_any_byte_wildcard() {
        assert_eq!(re2_to_resharp(r"a\Cb").unwrap(), r"a_b");
    }

    #[test]
    fn re2_dot_c_rejected_inside_class() {
        assert!(re2_to_resharp(r"[\C]").is_err());
    }

    #[test]
    fn re2_octal_escapes_become_hex() {
        assert_eq!(re2_to_resharp(r"\101").unwrap(), r"\x{41}"); // 'A'
        assert_eq!(re2_to_resharp(r"\0").unwrap(), r"\x{0}");
        assert_eq!(re2_to_resharp(r"\12z").unwrap(), r"\x{a}z"); // greedy: takes both digits
    }

    #[test]
    fn re2_quote_span_escapes_metachars() {
        assert_eq!(re2_to_resharp(r"\Qa.b*c\E").unwrap(), r"a\.b\*c");
        assert_eq!(re2_to_resharp(r"x\Qa_b&c\Ey").unwrap(), r"xa\_b\&cy");
    }

    #[test]
    fn re2_quote_span_without_closing_e_runs_to_end() {
        assert_eq!(re2_to_resharp(r"a\Qb.c").unwrap(), r"ab\.c");
    }

    #[test]
    fn re2_escaped_backslash_q_is_not_a_quote_opener() {
        // `\\Q` is an escaped literal backslash followed by literal `Q`,
        // not a `\Q...\E` opener.
        assert_eq!(re2_to_resharp(r"\\Qa.b\E").unwrap(), r"\\Qa.b\E");
    }

    #[test]
    fn re2_ordinary_patterns_pass_through_like_rust_dialect() {
        assert_eq!(
            re2_to_resharp(r"[a-z0-9]+\.[a-z]{2,4}").unwrap(),
            r"[a-z0-9]+\.[a-z]{2,4}"
        );
        assert_eq!(re2_to_resharp("(?P<name>a_b)").unwrap(), r"(?P<name>a\_b)");
        assert!(re2_to_resharp("a*?").is_err());
    }

    #[test]
    fn empty_pattern_passes_through() {
        assert_eq!(re2_to_resharp("").unwrap(), "");
    }

    #[test]
    fn trailing_lone_backslash_is_error() {
        assert!(re2_to_resharp("a\\").is_err());
    }

    #[test]
    fn re2_octal_boundary_values() {
        // 1-3 octal digits, greedy up to 3; a following non-octal digit or
        // a 4th digit both stop the run.
        assert_eq!(re2_to_resharp(r"\7").unwrap(), r"\x{7}");
        assert_eq!(re2_to_resharp(r"\777").unwrap(), r"\x{1ff}"); // max 3-digit octal = 511
        assert_eq!(re2_to_resharp(r"\7777").unwrap(), r"\x{1ff}7"); // 4th digit is literal
        assert_eq!(re2_to_resharp(r"\08").unwrap(), r"\x{0}8"); // '8' is not octal, stops run
        assert_eq!(re2_to_resharp(r"\0012").unwrap(), r"\x{1}2"); // greedy takes 3 digits "001"
    }

    #[test]
    fn re2_quote_span_multiple_in_one_pattern() {
        assert_eq!(
            re2_to_resharp(r"\Qa.b\Ex\Qc*d\E").unwrap(),
            r"a\.bx c\*d".replace(' ', "")
        );
    }

    #[test]
    fn re2_quote_span_empty() {
        assert_eq!(re2_to_resharp(r"a\Q\Eb").unwrap(), "ab");
    }

    #[test]
    fn re2_quote_span_containing_literal_backslash() {
        // A literal backslash inside \Q...\E is itself a meta character in
        // the target grammar and must be escaped so it isn't misread as
        // introducing an escape sequence in the emitted RE# text.
        assert_eq!(re2_to_resharp(r"\Qa\b\E").unwrap(), r"a\\b");
    }

    #[test]
    fn re2_quote_span_containing_bare_metachar() {
        assert_eq!(re2_to_resharp(r"\Qa_b\E").unwrap(), r"a\_b");
    }

    #[test]
    fn re2_no_quote_span_present_is_unaffected() {
        assert_eq!(re2_to_resharp(r"abc\d+").unwrap(), r"abc\d+");
    }

    #[test]
    fn re2_class_set_ops_not_supported_known_gap_documented_not_fixed() {
        // Documented, deliberately-unhandled gap (see this module's doc
        // comment): RE2 has no &&/--/~~ set operators, so a literal
        // doubled operator character inside an RE2 class is passed through
        // unchanged and would be misparsed by RE#'s superset grammar as a
        // set operator instead of two literal characters. This test pins
        // down the current (known-imperfect) behavior so a future fix is a
        // deliberate, visible change to this test, not a silent regression
        // discovered later.
        assert_eq!(re2_to_resharp("[a&&b]").unwrap(), "[a&&b]");
    }
}
