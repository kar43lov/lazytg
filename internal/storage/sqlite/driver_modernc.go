//go:build !sqlcipher

package sqlite

// Pure-Go SQLite driver (modernc.org/sqlite). The blank import registers the
// "sqlite" driver name with database/sql so that Open() can dial it.
import _ "modernc.org/sqlite" //nolint:revive // blank import is the documented way to register a database/sql driver

// driverName is the database/sql driver to use when opening connections.
const driverName = "sqlite"
