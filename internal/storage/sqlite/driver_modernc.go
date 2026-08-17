//go:build !sqlcipher

package sqlite

// Pure-Go SQLite driver (modernc.org/sqlite). The blank import registers the
// "sqlite" driver name with database/sql so that Open() can dial it.
//
// SQLCipher (CGo, encrypted DB) is deferred past v0.1 and will live behind
// the sqlcipher build tag with its own driver registration. The build tag
// gate above ensures this file and driver_sqlcipher.go never coexist in the
// same binary — preventing a future "duplicate driverName" compile error
// when the follow-up swaps in `_ "github.com/mutecomm/go-sqlcipher/v4"` and
// const driverName = "sqlite3".
import _ "modernc.org/sqlite" //nolint:revive // blank import is the documented way to register a database/sql driver

// driverName is the database/sql driver to use when opening connections.
const driverName = "sqlite"
