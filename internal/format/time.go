package format

import (
	"fmt"
	"time"
)

// T converts RFC3339 to "2006-01-02 15:04" UTC; "" stays "". A string that
// does not parse is returned unchanged.
func T(s string) string {
	if s == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return s
	}
	return t.UTC().Format("2006-01-02 15:04")
}

// Dur renders a duration as "1m03s", "42s", "2h05m". Sub-second values are
// rounded to whole seconds; negative values render as "0s".
func Dur(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	secs := int64((d + 500*time.Millisecond) / time.Second)
	h, m, s := secs/3600, secs%3600/60, secs%60
	switch {
	case h > 0:
		return fmt.Sprintf("%dh%02dm", h, m)
	case m > 0:
		return fmt.Sprintf("%dm%02ds", m, s)
	default:
		return fmt.Sprintf("%ds", s)
	}
}

// Age renders the time elapsed between then and now in a short form
// ("42s", "5m", "3h", "2d", "4mo", "2y"); a future then renders as "0s".
func Age(then, now time.Time) string {
	d := now.Sub(then)
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d/time.Second))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d/time.Minute))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d/time.Hour))
	case d < 60*24*time.Hour:
		return fmt.Sprintf("%dd", int(d/(24*time.Hour)))
	case d < 365*24*time.Hour:
		return fmt.Sprintf("%dmo", int(d/(30*24*time.Hour)))
	default:
		return fmt.Sprintf("%dy", int(d/(365*24*time.Hour)))
	}
}
