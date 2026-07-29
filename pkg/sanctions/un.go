package sanctions

import (
	"fmt"
	"strings"
)

// unList is the root of the UN Security Council consolidated list.
type unList struct {
	Generated   string     `xml:"dateGenerated,attr"`
	Individuals []unRecord `xml:"INDIVIDUALS>INDIVIDUAL"`
	Entities    []unRecord `xml:"ENTITIES>ENTITY"`
}

// unRecord covers both record kinds. The two differ only in which optional
// elements appear, so one struct reads both and the caller states the kind.
type unRecord struct {
	DataID      string   `xml:"DATAID"`
	First       string   `xml:"FIRST_NAME"`
	Second      string   `xml:"SECOND_NAME"`
	Third       string   `xml:"THIRD_NAME"`
	Fourth      string   `xml:"FOURTH_NAME"`
	Script      string   `xml:"NAME_ORIGINAL_SCRIPT"`
	ListType    string   `xml:"UN_LIST_TYPE"`
	Reference   string   `xml:"REFERENCE_NUMBER"`
	ListedOn    string   `xml:"LISTED_ON"`
	Comments    string   `xml:"COMMENTS1"`
	Updated     []string `xml:"LAST_DAY_UPDATED>VALUE"`
	Nationality []string `xml:"NATIONALITY>VALUE"`
	Aliases     []struct {
		Quality string `xml:"QUALITY"`
		Name    string `xml:"ALIAS_NAME"`
	} `xml:"INDIVIDUAL_ALIAS"`
	EntityAliases []struct {
		Quality string `xml:"QUALITY"`
		Name    string `xml:"ALIAS_NAME"`
	} `xml:"ENTITY_ALIAS"`
	Births []struct {
		Type string `xml:"TYPE_OF_DATE"`
		Date string `xml:"DATE"`
		Year string `xml:"YEAR"`
		From string `xml:"FROM_YEAR"`
		To   string `xml:"TO_YEAR"`
	} `xml:"INDIVIDUAL_DATE_OF_BIRTH"`
	Places []struct {
		City     string `xml:"CITY"`
		Province string `xml:"STATE_PROVINCE"`
		Country  string `xml:"COUNTRY"`
	} `xml:"INDIVIDUAL_PLACE_OF_BIRTH"`
	Documents []struct {
		Type    string `xml:"TYPE_OF_DOCUMENT"`
		Number  string `xml:"NUMBER"`
		Country string `xml:"ISSUING_COUNTRY"`
	} `xml:"INDIVIDUAL_DOCUMENT"`
	Addresses []struct {
		Street   string `xml:"STREET"`
		City     string `xml:"CITY"`
		Province string `xml:"STATE_PROVINCE"`
		Country  string `xml:"COUNTRY"`
	} `xml:"INDIVIDUAL_ADDRESS"`
	EntityAddresses []struct {
		Street   string `xml:"STREET"`
		City     string `xml:"CITY"`
		Province string `xml:"STATE_PROVINCE"`
		Country  string `xml:"COUNTRY"`
	} `xml:"ENTITY_ADDRESS"`
}

// ParseUN reads the UN Security Council consolidated list.
func ParseUN(data []byte) ([]Entry, error) {
	var doc unList
	if err := decode(data, &doc); err != nil {
		return nil, fmt.Errorf("un consolidated: %w", err)
	}
	if len(doc.Individuals)+len(doc.Entities) == 0 {
		return nil, fmt.Errorf("un consolidated: no records — refusing an empty list")
	}
	out := make([]Entry, 0, len(doc.Individuals)+len(doc.Entities))
	for _, r := range doc.Individuals {
		out = append(out, unEntry(r, Individual))
	}
	for _, r := range doc.Entities {
		out = append(out, unEntry(r, Entity))
	}
	return out, nil
}

func unEntry(r unRecord, kind string) Entry {
	e := Entry{
		List:     UN,
		RefID:    strings.TrimSpace(r.DataID),
		Group:    strings.TrimSpace(r.DataID),
		Kind:     kind,
		Remarks:  strings.TrimSpace(r.Comments),
		ListedOn: strings.TrimSpace(r.ListedOn),
	}
	if v := strings.TrimSpace(r.ListType); v != "" {
		e.Programs = append(e.Programs, v)
	}
	if strings.TrimSpace(r.Reference) != "" {
		e.RefID = strings.TrimSpace(r.Reference)
	}
	for _, u := range r.Updated {
		if u = strings.TrimSpace(u); u != "" {
			e.UpdatedOn = u
		}
	}

	// An individual's name is split across four ordered parts; an entity carries
	// its whole name in the first.
	e.Names = append(e.Names, Name{
		Full:   join(r.First, r.Second, r.Third, r.Fourth),
		Type:   Primary,
		Strong: true,
		Script: strings.TrimSpace(r.Script),
	})
	for _, a := range append(r.Aliases, r.EntityAliases...) {
		full := strings.TrimSpace(a.Name)
		if full == "" {
			continue
		}
		e.Names = append(e.Names, Name{Full: full, Type: unAliasType(a.Quality), Strong: unStrong(a.Quality)})
	}

	for _, b := range r.Births {
		if bd, ok := unBirth(b.Type, b.Date, b.Year, b.From, b.To); ok {
			e.Births = append(e.Births, bd)
		}
	}
	for _, p := range r.Places {
		if v := join(p.City, p.Province, p.Country); v != "" {
			e.Places = append(e.Places, v)
		}
	}
	for _, n := range r.Nationality {
		if n = strings.TrimSpace(n); n != "" {
			e.Nationalities = append(e.Nationalities, n)
		}
	}
	for _, d := range r.Documents {
		num := strings.TrimSpace(d.Number)
		if num == "" {
			continue
		}
		kind := Other
		if strings.Contains(strings.ToLower(d.Type), "passport") {
			kind = Passport
		} else if strings.Contains(strings.ToLower(d.Type), "national") {
			kind = National
		}
		e.Documents = append(e.Documents, Document{Kind: kind, Number: num, Country: strings.TrimSpace(d.Country)})
	}
	for _, a := range append(r.Addresses, r.EntityAddresses...) {
		if v := join(a.Street, a.City, a.Province, a.Country); v != "" {
			e.Addresses = append(e.Addresses, v)
		}
	}
	return e
}

// unStrong reads the UN alias QUALITY field.
//
// The field mixes two meanings: it carries confidence ("Good", "Low") on most
// aliases and an alias type ("a.k.a.", "f.k.a.") on others. Only an explicit Low
// marks a weak name; a value stating a type says nothing about confidence and is
// therefore not treated as a downgrade.
func unStrong(q string) bool {
	return !strings.EqualFold(strings.TrimSpace(q), "low")
}

func unAliasType(q string) string {
	switch strings.ToLower(strings.TrimSpace(q)) {
	case "f.k.a.":
		return Formerly
	default:
		return Also
	}
}

// unBirth reads a UN date of birth, which is an exact date, a year, an
// approximate year, or a range between two years.
func unBirth(kind, date, year, from, to string) (Birth, bool) {
	kind = strings.ToUpper(strings.TrimSpace(kind))
	circa := kind == "APPROXIMATELY"
	if d := strings.TrimSpace(date); d != "" {
		return Birth{From: d, To: d, Circa: circa}, true
	}
	if kind == "BETWEEN" {
		f, t := strings.TrimSpace(from), strings.TrimSpace(to)
		if f != "" && t != "" {
			return Birth{From: f, To: t, Circa: circa}, true
		}
	}
	if y := strings.TrimSpace(year); y != "" {
		return Birth{From: y, To: y, Circa: circa}, true
	}
	return Birth{}, false
}
