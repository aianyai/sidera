package termout

import (
	"fmt"
	"strconv"
	"time"
)

// Timestamp is a point in time in UTC, to the minute, with no relative part:
// "2026-11-07 01:40 UTC". It is the written form of a time wherever how far it is from
// now does not matter or is said elsewhere.
func Timestamp(t time.Time) string { return t.UTC().Format("2006-01-02 15:04 UTC") }

// Time is a point in time as people read it in Sidera output: the [Timestamp]
// followed by how far it is from now, as in "2026-12-07 01:40 UTC (in 30 days)" or
// "2026-11-07 01:40 UTC (2 h ago)". The relative part is dimmed on a colored stream.
// The caller passes now, so the same text can be asked for twice and a test can fix
// it.
func (s *Stream) Time(t, now time.Time) string {
	return Timestamp(t) + " " + s.Dim("("+relative(t.Sub(now))+")")
}

// relative says how far a time is from now: "in 30 days", "2 h ago", "now". A span of
// a minute or more is rounded to the minute, so that 29 days 23 h 59 min 59 s from now
// is "in 30 days" and not "in 29 days 23 h".
func relative(d time.Duration) string {
	if d.Abs() < time.Second {
		return "now"
	}
	if d.Abs() >= time.Minute {
		d = d.Round(time.Minute)
	}
	if d < 0 {
		return Duration(-d) + " ago"
	}
	return "in " + Duration(d)
}

// Duration is a length of time in words, whole seconds, at most two units: "45 s",
// "3 min", "2 h 5 min", "30 days", "1 day 3 h". A negative duration is written as
// its absolute value.
func Duration(d time.Duration) string {
	secs := int64(d.Abs() / time.Second)
	days, hours, mins, sec := secs/86400, secs/3600%24, secs/60%60, secs%60
	switch {
	case days > 0:
		return units(days, "day", "days") + part(hours, "h")
	case hours > 0:
		return fmt.Sprintf("%d h", hours) + part(mins, "min")
	case mins > 0:
		return fmt.Sprintf("%d min", mins) + part(sec, "s")
	}
	return fmt.Sprintf("%d s", sec)
}

func units(n int64, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.FormatInt(n, 10) + " " + many
}

// part is the lesser unit of a duration, left out when it is zero.
func part(n int64, unit string) string {
	if n == 0 {
		return ""
	}
	return fmt.Sprintf(" %d %s", n, unit)
}

// Bytes is a size in decimal units with one decimal place: "512 B", "1.5 kB",
// "268.4 MB", "32.2 GB". Below a thousand it is whole bytes.
func Bytes(n int64) string {
	sign, abs := "", uint64(n)
	if n < 0 {
		// Negating as unsigned also holds for the most negative int64.
		sign, abs = "-", -abs
	}
	if abs < 1000 {
		return fmt.Sprintf("%s%d B", sign, abs)
	}
	names := []string{"kB", "MB", "GB", "TB", "PB", "EB"}
	v, i := float64(abs)/1000, 0
	// A value that rounds to 1000.0 is written in the next unit.
	for v >= 999.95 && i < len(names)-1 {
		v /= 1000
		i++
	}
	return fmt.Sprintf("%s%.1f %s", sign, v, names[i])
}
