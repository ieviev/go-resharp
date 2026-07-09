package compiler

import "strings"

const resharpMetaChars = `\.+*?()|[]{}^$#&-~_`

// QuoteMeta escapes resharp meta characters.
func QuoteMeta(s string) string {
	var b strings.Builder
	for _, r := range s {
		if strings.ContainsRune(resharpMetaChars, r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}
