package sanctions

import (
	"fmt"
	"strings"
)

// euExport is the root of the EU consolidated financial sanctions file.
type euExport struct {
	Generated string     `xml:"generationDate,attr"`
	Entities  []euEntity `xml:"sanctionEntity"`
}

type euEntity struct {
	Reference    string   `xml:"euReferenceNumber,attr"`
	LogicalID    string   `xml:"logicalId,attr"`
	UnitedNation string   `xml:"unitedNationId,attr"`
	Designated   string   `xml:"designationDate,attr"`
	Remark       []string `xml:"remark"`
	Subject      struct {
		Code           string `xml:"code,attr"`
		Classification string `xml:"classificationCode,attr"`
	} `xml:"subjectType"`
	Regulations []struct {
		Programme string `xml:"programme,attr"`
		Number    string `xml:"numberTitle,attr"`
		Published string `xml:"publicationDate,attr"`
	} `xml:"regulation"`
	Names []struct {
		Whole    string `xml:"wholeName,attr"`
		First    string `xml:"firstName,attr"`
		Middle   string `xml:"middleName,attr"`
		Last     string `xml:"lastName,attr"`
		Language string `xml:"nameLanguage,attr"`
		Strong   string `xml:"strong,attr"`
	} `xml:"nameAlias"`
	Births []struct {
		Date  string `xml:"birthdate,attr"`
		Year  string `xml:"year,attr"`
		Month string `xml:"monthOfYear,attr"`
		Day   string `xml:"dayOfMonth,attr"`
		From  string `xml:"yearRangeFrom,attr"`
		To    string `xml:"yearRangeTo,attr"`
		Circa string `xml:"circa,attr"`
		// Calendar names the calendar the date is expressed in. A date recorded in
		// a non-Gregorian calendar cannot be compared against a customer's
		// Gregorian date of birth without conversion, so it is not treated as a
		// comparable date.
		Calendar string `xml:"calendarType,attr"`
		City     string `xml:"city,attr"`
		Country  string `xml:"countryDescription,attr"`
	} `xml:"birthdate"`
	Citizenships []struct {
		Code    string `xml:"countryIso2Code,attr"`
		Country string `xml:"countryDescription,attr"`
	} `xml:"citizenship"`
	IDs []struct {
		Number      string `xml:"number,attr"`
		Latin       string `xml:"latinNumber,attr"`
		TypeCode    string `xml:"identificationTypeCode,attr"`
		Description string `xml:"identificationTypeDescription,attr"`
		Country     string `xml:"countryIso2Code,attr"`
	} `xml:"identification"`
	Addresses []struct {
		Street  string `xml:"street,attr"`
		City    string `xml:"city,attr"`
		Zip     string `xml:"zipCode,attr"`
		Region  string `xml:"region,attr"`
		Country string `xml:"countryDescription,attr"`
	} `xml:"address"`
}

// ParseEU reads the EU consolidated financial sanctions file.
func ParseEU(data []byte) ([]Entry, error) {
	var doc euExport
	if err := decode(data, &doc); err != nil {
		return nil, fmt.Errorf("eu consolidated: %w", err)
	}
	if len(doc.Entities) == 0 {
		return nil, fmt.Errorf("eu consolidated: no records — refusing an empty list")
	}

	out := make([]Entry, 0, len(doc.Entities))
	for _, s := range doc.Entities {
		ref := strings.TrimSpace(s.Reference)
		if ref == "" {
			ref = strings.TrimSpace(s.LogicalID)
		}
		e := Entry{
			List:      EU,
			RefID:     ref,
			Group:     ref,
			Kind:      euKind(s.Subject.Classification),
			Remarks:   strings.TrimSpace(strings.Join(s.Remark, "; ")),
			ListedOn:  strings.TrimSpace(s.Designated),
			UpdatedOn: strings.TrimSpace(doc.Generated),
		}
		for _, r := range s.Regulations {
			if p := strings.TrimSpace(r.Programme); p != "" {
				e.Programs = append(e.Programs, p)
			}
		}

		// The EU marks no alias as weak — every one of its names carries
		// strong="true" — and it publishes no primary flag, so the first name is
		// the main one by position. The strong attribute is still read rather than
		// assumed, so the parser reflects the file if the EU starts marking weak
		// names.
		for i, n := range s.Names {
			full := strings.TrimSpace(n.Whole)
			if full == "" {
				full = join(n.First, n.Middle, n.Last)
			}
			if full == "" {
				continue
			}
			t := Also
			if i == 0 {
				t = Primary
			}
			e.Names = append(e.Names, Name{
				Full:   full,
				Type:   t,
				Strong: !strings.EqualFold(strings.TrimSpace(n.Strong), "false"),
			})
		}

		for _, b := range s.Births {
			if cal := strings.TrimSpace(b.Calendar); cal != "" && !strings.EqualFold(cal, "GREGORIAN") {
				continue
			}
			if bd, ok := euBirth(b.Date, b.Year, b.Month, b.Day, b.From, b.To, b.Circa); ok {
				e.Births = append(e.Births, bd)
			}
			if v := join(b.City, b.Country); v != "" {
				e.Places = append(e.Places, v)
			}
		}
		for _, c := range s.Citizenships {
			if v := strings.TrimSpace(c.Country); v != "" {
				e.Citizenships = append(e.Citizenships, v)
			} else if v := strings.TrimSpace(c.Code); v != "" {
				e.Citizenships = append(e.Citizenships, v)
			}
		}
		for _, id := range s.IDs {
			num := strings.TrimSpace(id.Number)
			if num == "" {
				num = strings.TrimSpace(id.Latin)
			}
			if num == "" {
				continue
			}
			kind := Other
			d := strings.ToLower(id.Description + " " + id.TypeCode)
			if strings.Contains(d, "passport") {
				kind = Passport
			} else if strings.Contains(d, "id") {
				kind = National
			}
			e.Documents = append(e.Documents, Document{Kind: kind, Number: num, Country: strings.TrimSpace(id.Country)})
		}
		for _, a := range s.Addresses {
			if v := join(a.Street, a.City, a.Zip, a.Region, a.Country); v != "" {
				e.Addresses = append(e.Addresses, v)
			}
		}
		out = append(out, e)
	}
	return out, nil
}

// euKind reads the EU subject classification: P for a natural person, E for an
// entity.
func euKind(code string) string {
	if strings.EqualFold(strings.TrimSpace(code), "P") {
		return Individual
	}
	return Entity
}

func euBirth(date, year, month, day, from, to, circa string) (Birth, bool) {
	c := strings.EqualFold(strings.TrimSpace(circa), "true")
	if d := strings.TrimSpace(date); d != "" {
		return Birth{From: d, To: d, Circa: c}, true
	}
	f, t := strings.TrimSpace(from), strings.TrimSpace(to)
	if f != "" && t != "" {
		return Birth{From: f, To: t, Circa: c}, true
	}
	y := strings.TrimSpace(year)
	if y == "" {
		return Birth{}, false
	}
	iso := y
	if m := strings.TrimSpace(month); m != "" {
		iso = fmt.Sprintf("%s-%02s", y, pad(m))
		if d := strings.TrimSpace(day); d != "" {
			iso = fmt.Sprintf("%s-%s-%s", y, pad(m), pad(d))
		}
	}
	return Birth{From: iso, To: iso, Circa: c}, true
}

func pad(s string) string {
	if len(s) == 1 {
		return "0" + s
	}
	return s
}
