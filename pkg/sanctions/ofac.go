package sanctions

import (
	"fmt"
	"strings"
)

// ofacList is the root of the OFAC SDN XML export.
//
// The document is namespaced. The field tags below carry local names only, which
// Go's decoder matches irrespective of namespace, so the parser keeps working
// when OFAC moves the export between hosts and namespace URIs — as it has.
type ofacList struct {
	Publish ofacPublish `xml:"publshInformation"`
	Entries []ofacEntry `xml:"sdnEntry"`
}

// ofacPublish carries the publication date and the record count OFAC states the
// file contains. The count is an integrity check the publisher hands us for free:
// parsing fewer records than OFAC says it published means the file was truncated
// in transit, and a truncated sanctions list is a list that clears designated
// subjects.
type ofacPublish struct {
	Date  string `xml:"Publish_Date"`
	Count int    `xml:"Record_Count"`
}

type ofacEntry struct {
	UID       int      `xml:"uid"`
	FirstName string   `xml:"firstName"`
	LastName  string   `xml:"lastName"`
	Title     string   `xml:"title"`
	Type      string   `xml:"sdnType"`
	Remarks   string   `xml:"remarks"`
	Programs  []string `xml:"programList>program"`
	Akas      []struct {
		Type      string `xml:"type"`
		Category  string `xml:"category"`
		FirstName string `xml:"firstName"`
		LastName  string `xml:"lastName"`
	} `xml:"akaList>aka"`
	IDs []struct {
		Type    string `xml:"idType"`
		Number  string `xml:"idNumber"`
		Country string `xml:"idCountry"`
	} `xml:"idList>id"`
	Births []struct {
		Date string `xml:"dateOfBirth"`
		Main string `xml:"mainEntry"`
	} `xml:"dateOfBirthList>dateOfBirthItem"`
	Places []struct {
		Place string `xml:"placeOfBirth"`
		Main  string `xml:"mainEntry"`
	} `xml:"placeOfBirthList>placeOfBirthItem"`
	Nationalities []struct {
		Country string `xml:"country"`
	} `xml:"nationalityList>nationality"`
	Citizenships []struct {
		Country string `xml:"country"`
	} `xml:"citizenshipList>citizenship"`
	Addresses []struct {
		One      string `xml:"address1"`
		Two      string `xml:"address2"`
		Three    string `xml:"address3"`
		City     string `xml:"city"`
		Province string `xml:"stateOrProvince"`
		Postal   string `xml:"postalCode"`
		Country  string `xml:"country"`
	} `xml:"addressList>address"`
}

// digitalPrefix is how OFAC labels a designated blockchain address in idType:
// "Digital Currency Address - XBT". The suffix names the network.
const digitalPrefix = "Digital Currency Address - "

// ParseOFAC reads the OFAC SDN XML export.
//
// It verifies the parsed count against the count OFAC publishes in the file and
// refuses the whole file on a mismatch. A sanctions list is the one input where
// partial success is worse than failure: the missing records are indistinguishable
// from records that were never designated, so screening reports clean.
func ParseOFAC(data []byte) ([]Entry, error) {
	var doc ofacList
	if err := decode(data, &doc); err != nil {
		return nil, fmt.Errorf("ofac sdn: %w", err)
	}
	if doc.Publish.Count > 0 && len(doc.Entries) != doc.Publish.Count {
		return nil, fmt.Errorf("ofac sdn: parsed %d records, file declares %d — refusing a truncated list",
			len(doc.Entries), doc.Publish.Count)
	}

	out := make([]Entry, 0, len(doc.Entries))
	for _, e := range doc.Entries {
		ref := fmt.Sprintf("%d", e.UID)
		entry := Entry{
			List:      OFAC,
			RefID:     ref,
			Group:     ref,
			Kind:      ofacKind(e.Type),
			Remarks:   strings.TrimSpace(e.Remarks),
			Programs:  trimAll(e.Programs),
			ListedOn:  doc.Publish.Date,
			UpdatedOn: doc.Publish.Date,
		}

		// An entity's whole name sits in lastName with firstName empty, so joining
		// the parts is what produces a usable name for both kinds.
		entry.Names = append(entry.Names, Name{
			Full:   join(e.FirstName, e.LastName),
			Type:   Primary,
			Strong: true,
		})
		for _, a := range e.Akas {
			full := join(a.FirstName, a.LastName)
			if full == "" {
				continue
			}
			entry.Names = append(entry.Names, Name{
				Full:   full,
				Type:   akaType(a.Type),
				Strong: !strings.EqualFold(strings.TrimSpace(a.Category), "weak"),
			})
		}

		// The main date of birth is the one OFAC flags, not the one that happens to
		// be first: 37 of the 541 subjects with several dates list the main one
		// second or later, so taking the first silently drops the tie-break for
		// exactly the subjects where a tie-break is needed.
		for _, b := range e.Births {
			if bd, ok := parseOFACBirth(b.Date); ok {
				if isMain(b.Main) {
					entry.Births = append([]Birth{bd}, entry.Births...)
				} else {
					entry.Births = append(entry.Births, bd)
				}
			}
		}
		for _, p := range e.Places {
			if v := strings.TrimSpace(p.Place); v != "" {
				if isMain(p.Main) {
					entry.Places = append([]string{v}, entry.Places...)
				} else {
					entry.Places = append(entry.Places, v)
				}
			}
		}
		for _, n := range e.Nationalities {
			if v := strings.TrimSpace(n.Country); v != "" {
				entry.Nationalities = append(entry.Nationalities, v)
			}
		}
		for _, c := range e.Citizenships {
			if v := strings.TrimSpace(c.Country); v != "" {
				entry.Citizenships = append(entry.Citizenships, v)
			}
		}
		for _, id := range e.IDs {
			num := strings.TrimSpace(id.Number)
			if num == "" {
				continue
			}
			t := strings.TrimSpace(id.Type)
			doc := Document{Number: num, Country: strings.TrimSpace(id.Country)}
			switch {
			case strings.HasPrefix(t, digitalPrefix):
				doc.Kind = Address
				doc.Chain = strings.TrimSpace(strings.TrimPrefix(t, digitalPrefix))
			case strings.Contains(strings.ToLower(t), "passport"):
				doc.Kind = Passport
			case strings.Contains(strings.ToLower(t), "national id"):
				doc.Kind = National
			default:
				doc.Kind = Other
			}
			entry.Documents = append(entry.Documents, doc)
		}
		for _, a := range e.Addresses {
			if v := join(a.One, a.Two, a.Three, a.City, a.Province, a.Postal, a.Country); v != "" {
				entry.Addresses = append(entry.Addresses, v)
			}
		}
		out = append(out, entry)
	}
	return out, nil
}

func ofacKind(t string) string {
	switch strings.ToLower(strings.TrimSpace(t)) {
	case "individual":
		return Individual
	case "vessel":
		return Vessel
	case "aircraft":
		return Aircraft
	default:
		return Entity
	}
}

func akaType(t string) string {
	switch strings.ToLower(strings.TrimSpace(t)) {
	case "f.k.a.":
		return Formerly
	case "n.k.a.":
		return Now
	default:
		return Also
	}
}

func isMain(v string) bool { return strings.EqualFold(strings.TrimSpace(v), "true") }

func trimAll(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}
