package sqlite

// Pure-Go SQLite driver (modernc.org/sqlite). The blank import registers the
// "sqlite" driver name with database/sql so that Open() can dial it.
//
// SQLCipher (CGo, encrypted DB) is planned for stage 3 and will live behind
// a separate build tag with its own driver registration. Until then, only the
// pure-Go driver is wired so callers cannot accidentally believe their DB is
// encrypted.
import _ "modernc.org/sqlite" //nolint:revive // blank import is the documented way to register a database/sql driver

// driverName is the database/sql driver to use when opening connections.
const driverName = "sqlite"
