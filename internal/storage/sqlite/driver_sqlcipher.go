//go:build sqlcipher

package sqlite

// SQLCipher driver placeholder. The CGo-backed implementation is planned for
// Stage 3 of the lazytg roadmap. Until then, building with -tags sqlcipher
// must NOT silently produce a binary that uses the unencrypted modernc driver
// — that would be a security misrepresentation. Instead this init panics so
// the failure is visible at first run.
//
// When the real driver lands, replace this file with:
//
//	import _ "github.com/mutecomm/go-sqlcipher/v4"
//	const driverName = "sqlite3"
//
// (and remove the panic).
func init() {
	panic("sqlcipher build tag is reserved for lazytg Stage 3 (CGo SQLCipher); rebuild without -tags sqlcipher for now")
}
