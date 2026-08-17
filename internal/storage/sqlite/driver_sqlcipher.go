//go:build sqlcipher

package sqlite

// SQLCipher driver is deferred past v0.1. Building with -tags sqlcipher must
// NOT silently produce a binary that uses the unencrypted modernc driver —
// that would be a security misrepresentation. The reference to the undefined
// symbol below makes `go build -tags sqlcipher` fail at compile time with a
// clear message ("undefined: sqlcipherDriverDeferred"), rather than producing
// a binary that panics at first run.
//
// When the real driver lands, replace this file with:
//
//	import _ "github.com/mutecomm/go-sqlcipher/v4"
//	const driverName = "sqlite3"
const driverName = sqlcipherDriverDeferred
