package standalone

import "time"

// Clock returns the current time. Tests substitute a deterministic
// implementation. Production code always passes time.Now.
type Clock func() time.Time

// systemClock is the default Clock returning real wall-clock time.
func systemClock() time.Time { return time.Now() }
