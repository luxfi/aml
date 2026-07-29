package sanctions

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"io"
	"strings"
)

// ParseOFSI reads the UK consolidated list of financial sanctions targets.
//
// The file is a CSV with two properties a straightforward reader gets wrong.
// First, the real header is on the second line: the first line is a
// "Last Updated,<date>" preamble, so a default reader takes the preamble as the
// header and every column name is then wrong. Second, the file holds one row per
// name, not per subject — 19,761 rows describe 5,135 subjects — joined by a group
// identifier. Parsing each row as a separate designation multiplies every alert
// by the number of aliases the target happens to have.
func ParseOFSI(data []byte) ([]Entry, error) {
	r := csv.NewReader(bytes.NewReader(data))
	r.FieldsPerRecord = -1
	r.LazyQuotes = true

	first, err := r.Read()
	if err != nil {
		return nil, fmt.Errorf("ofsi conlist: %w", err)
	}
	// The preamble is a short row naming the publication date; the header is wide.
	updated := ""
	head := first
	if len(first) <= 2 {
		if len(first) == 2 {
			updated = strings.TrimSpace(first[1])
		}
		head, err = r.Read()
		if err != nil {
			return nil, fmt.Errorf("ofsi conlist: reading header after preamble: %w", err)
		}
	}

	col := make(map[string]int, len(head))
	for i, h := range head {
		col[strings.TrimSpace(strings.TrimPrefix(h, "\ufeff"))] = i
	}
	for _, want := range []string{"Name 1", "Name 6", "Group Type", "Group ID", "Regime"} {
		if _, ok := col[want]; !ok {
			return nil, fmt.Errorf("ofsi conlist: column %q not found — the layout has changed and the parser must be updated, not guessed at", want)
		}
	}

	get := func(rec []string, name string) string {
		i, ok := col[name]
		if !ok || i >= len(rec) {
			return ""
		}
		return strings.TrimSpace(rec[i])
	}

	// Rows sharing a group identifier are one subject. The group is accumulated
	// rather than replaced so that names, dates and documents from every row land
	// on the same entry.
	byGroup := make(map[string]*Entry, 6000)
	var order []string

	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("ofsi conlist: %w", err)
		}
		group := get(rec, "Group ID")
		if group == "" {
			continue
		}
		e, ok := byGroup[group]
		if !ok {
			e = &Entry{
				List:      OFSI,
				RefID:     group,
				Group:     group,
				Kind:      ofsiKind(get(rec, "Group Type")),
				UpdatedOn: updated,
				ListedOn:  get(rec, "Listed On"),
			}
			if reg := get(rec, "Regime"); reg != "" {
				e.Programs = append(e.Programs, reg)
			}
			byGroup[group] = e
			order = append(order, group)
		}

		// Name 6 holds the family name and Name 1 through Name 5 the given names,
		// in that order — the header lists Name 6 first, which is why the parser
		// addresses columns by name and never by position.
		full := join(get(rec, "Name 1"), get(rec, "Name 2"), get(rec, "Name 3"),
			get(rec, "Name 4"), get(rec, "Name 5"), get(rec, "Name 6"))
		if full != "" {
			e.Names = append(e.Names, Name{
				Full:   full,
				Type:   ofsiNameType(get(rec, "Alias Type")),
				Strong: !strings.EqualFold(get(rec, "Alias Quality"), "Low quality"),
				Script: get(rec, "Name Non-Latin Script"),
			})
		}

		if b, ok := parseSlashBirth(get(rec, "DOB")); ok {
			e.Births = appendBirth(e.Births, b)
		}
		if v := join(get(rec, "Town of Birth"), get(rec, "Country of Birth")); v != "" {
			e.Places = appendUnique(e.Places, v)
		}
		if v := get(rec, "Nationality"); v != "" {
			e.Nationalities = appendUnique(e.Nationalities, v)
		}
		if v := get(rec, "Passport Number"); v != "" {
			e.Documents = append(e.Documents, Document{Kind: Passport, Number: v})
		}
		if v := get(rec, "National Identification Number"); v != "" {
			e.Documents = append(e.Documents, Document{Kind: National, Number: v})
		}
		if v := join(get(rec, "Address 1"), get(rec, "Address 2"), get(rec, "Address 3"),
			get(rec, "Address 4"), get(rec, "Address 5"), get(rec, "Address 6"),
			get(rec, "Post/Zip Code"), get(rec, "Country")); v != "" {
			e.Addresses = appendUnique(e.Addresses, v)
		}
		if v := get(rec, "Other Information"); v != "" && e.Remarks == "" {
			e.Remarks = v
		}
	}

	if len(order) == 0 {
		return nil, fmt.Errorf("ofsi conlist: no records — refusing an empty list")
	}
	out := make([]Entry, 0, len(order))
	for _, g := range order {
		e := byGroup[g]
		promotePrimary(e)
		out = append(out, *e)
	}
	return out, nil
}

// ofsiKind reads the UK group type. The UK designates ships, which the model
// records as vessels.
func ofsiKind(t string) string {
	switch strings.ToLower(strings.TrimSpace(t)) {
	case "individual":
		return Individual
	case "ship":
		return Vessel
	default:
		return Entity
	}
}

// ofsiNameType reads the UK alias type. A primary name variation is a spelling of
// the target's own name rather than a separate alias, so it is recorded as an
// alias but never displaces the primary.
func ofsiNameType(t string) string {
	switch strings.ToLower(strings.TrimSpace(t)) {
	case "primary name":
		return Primary
	case "fka":
		return Formerly
	default:
		return Also
	}
}

// promotePrimary makes sure an entry has exactly one primary name. The UK file
// does not guarantee that the primary row is read first, and an entry whose only
// names are aliases would otherwise report no primary name at all.
func promotePrimary(e *Entry) {
	seen := -1
	for i, n := range e.Names {
		if n.Type == Primary {
			if seen == -1 {
				seen = i
			} else {
				e.Names[i].Type = Also
			}
		}
	}
	if seen == -1 && len(e.Names) > 0 {
		e.Names[0].Type = Primary
	}
}

func appendBirth(list []Birth, b Birth) []Birth {
	for _, x := range list {
		if x.From == b.From && x.To == b.To {
			return list
		}
	}
	return append(list, b)
}

func appendUnique(list []string, v string) []string {
	for _, x := range list {
		if strings.EqualFold(x, v) {
			return list
		}
	}
	return append(list, v)
}
