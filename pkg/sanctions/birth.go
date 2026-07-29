package sanctions

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"strconv"
	"strings"
)

// decode reads an XML document with entity expansion disabled.
//
// A sanctions list is fetched from a remote host over the public internet on a
// schedule, unattended. Leaving entity expansion on lets a malformed or hostile
// document expand into unbounded memory, which takes screening offline — and a
// screening outage that fails open is a sanctions breach.
func decode(data []byte, into any) error {
	d := xml.NewDecoder(bytes.NewReader(data))
	d.Strict = true
	d.Entity = map[string]string{}
	return d.Decode(into)
}

// months maps the abbreviated month names the lists use onto their numbers.
var months = map[string]int{
	"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6,
	"jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12,
}

// parseOFACBirth reads a date of birth as OFAC writes it.
//
// OFAC publishes seven distinct shapes across its 8,141 dates: an exact date
// ("10 Dec 1948"), a year alone ("1938"), an approximation ("circa 1951"), a
// month and year, and ranges of any of those joined by "to". Storing the string
// unparsed, as the previous model did, makes every one of them useless for
// comparison — and a date of birth that cannot be compared cannot break a tie
// between a designated subject and a customer who merely shares their name.
func parseOFACBirth(s string) (Birth, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return Birth{}, false
	}
	var circa bool
	if strings.Contains(s, "circa") {
		circa = true
		s = strings.ReplaceAll(s, "circa", " ")
	}
	lo, hi := s, s
	if i := strings.Index(s, " to "); i >= 0 {
		lo, hi = s[:i], s[i+4:]
	}
	from, ok := parseLoose(lo)
	if !ok {
		return Birth{}, false
	}
	to, ok := parseLoose(hi)
	if !ok {
		to = from
	}
	return Birth{From: from, To: to, Circa: circa}, true
}

// parseLoose reads "14 aug 1971", "aug 1971", or "1971" into an ISO prefix,
// keeping exactly the precision the source stated. Inventing a day for a source
// that gave only a year would make an approximate match look exact.
func parseLoose(s string) (string, bool) {
	f := strings.Fields(strings.NewReplacer(",", " ", "-", " ", "/", " ").Replace(s))
	var day, mon, year int
	for _, tok := range f {
		if m, ok := months[safeCut(tok, 3)]; ok && mon == 0 {
			mon = m
			continue
		}
		n, err := strconv.Atoi(tok)
		if err != nil {
			continue
		}
		switch {
		case n >= 1000 && n <= 9999 && year == 0:
			year = n
		case n >= 1 && n <= 31 && day == 0:
			day = n
		}
	}
	if year == 0 {
		return "", false
	}
	switch {
	case mon > 0 && day > 0:
		return fmt.Sprintf("%04d-%02d-%02d", year, mon, day), true
	case mon > 0:
		return fmt.Sprintf("%04d-%02d", year, mon), true
	default:
		return fmt.Sprintf("%04d", year), true
	}
}

func safeCut(s string, n int) string {
	if len(s) < n {
		return s
	}
	return s[:n]
}

// parseSlashBirth reads a date of birth as OFSI writes it: dd/mm/yyyy, with zero
// standing for an unknown component.
//
// OFSI publishes dates like "00/00/1994", where the day and month are genuinely
// unknown. A parser that rejects them loses the year — the only part that was
// ever known — and a parser that reads them as day zero of month zero produces a
// date that matches nothing.
func parseSlashBirth(s string) (Birth, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Birth{}, false
	}
	f := strings.Split(s, "/")
	if len(f) != 3 {
		return Birth{}, false
	}
	day, _ := strconv.Atoi(f[0])
	mon, _ := strconv.Atoi(f[1])
	year, err := strconv.Atoi(f[2])
	if err != nil || year < 1000 || year > 9999 {
		return Birth{}, false
	}
	var iso string
	switch {
	case mon > 0 && mon <= 12 && day > 0 && day <= 31:
		iso = fmt.Sprintf("%04d-%02d-%02d", year, mon, day)
	case mon > 0 && mon <= 12:
		iso = fmt.Sprintf("%04d-%02d", year, mon)
	default:
		iso = fmt.Sprintf("%04d", year)
	}
	return Birth{From: iso, To: iso}, true
}

// Overlaps reports whether two dates of birth can describe the same person.
//
// Comparison is by ISO prefix, so a subject listed only by year matches a
// customer whose full date falls in that year, and a range matches any date
// inside it. This is the behaviour a tie-break needs: it must exclude a customer
// born in a different year without pretending to know a day the list never gave.
func (b Birth) Overlaps(other Birth) bool {
	if b.From == "" || other.From == "" {
		return false
	}
	return within(other.From, b.From, b.To) || within(b.From, other.From, other.To)
}

// within reports whether v falls inside the closed range [lo, hi] under prefix
// comparison.
func within(v, lo, hi string) bool {
	if hi == "" {
		hi = lo
	}
	n := min(len(v), len(lo))
	if v[:n] < lo[:n] {
		return false
	}
	n = min(len(v), len(hi))
	return v[:n] <= hi[:n]
}
