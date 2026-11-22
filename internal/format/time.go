package format

import "time"

// T converts RFC3339 to "2006-01-02 15:04" UTC; "" stays "".
func T(s string) string { panic("WP3") }

// Dur renders a duration as "1m03s", "42s", "2h05m".
func Dur(d time.Duration) string { panic("WP3") }

// Age renders the time elapsed between then and now in a short form.
func Age(then, now time.Time) string { panic("WP3") }
