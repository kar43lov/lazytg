//go:build sqlcipher

package sqlite

// SQLCipher driver. The real CGo-backed implementation is wired in stage 3
// of the lazytg roadmap. This stub keeps the build-tag matrix compiling so
// that CI can validate the tag flow before the real driver is plugged in.
//
// When the real driver lands, replace the import below with:
//
//	import _ "github.com/mutecomm/go-sqlcipher/v4"
//
// and set driverName to "sqlite3" (the name go-sqlcipher registers under).
import _ "modernc.org/sqlite" //nolint:revive // blank import is the documented way to register a database/sql driver

// driverName is the database/sql driver to use when opening connections.
const driverName = "sqlite"
