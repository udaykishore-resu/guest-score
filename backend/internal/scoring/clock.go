package scoring

import "time"

// Time is the injected evaluation instant.
//
// Constitution Principle III forbids the scoring engine from reading a clock,
// but "pass a time.Time around" is easy to violate by accident. Wrapping it in
// a named type makes the injection explicit at every call site and gives the
// age arithmetic a single home, so "how old is this review" is defined once
// instead of open-coded in three places.
type Time struct {
	t time.Time
}

// At builds an evaluation time from a wall-clock instant.
func At(t time.Time) Time { return Time{t: t.UTC()} }

// Now builds an evaluation time from the current instant.
//
// This is the only clock read in the package, and it is deliberately not called
// by Compute — callers in the API layer resolve the time and pass it down, so
// tests can fix it.
func Now() Time { return Time{t: time.Now().UTC()} }

// Std returns the underlying instant.
func (t Time) Std() time.Time { return t.t }

// DaysSince returns the age of an instant in fractional days as of t. The
// result is negative when the argument is in the future; decay() treats that
// case as fresh rather than letting clock skew inflate a score.
func (t Time) DaysSince(u time.Time) float64 {
	return t.t.Sub(u.UTC()).Hours() / 24.0
}
