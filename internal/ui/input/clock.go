package input

import "time"

// sendCtxUnit is the multiplier applied to sendTimeoutSeconds when
// constructing the SendText context. Pulled out so unit tests that
// want the timeout in nanoseconds can override it without rewriting
// the Update path.
var sendCtxUnit = time.Second
