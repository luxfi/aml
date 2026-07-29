// Copyright 2024-2026 Lux Partners Limited. All rights reserved.
// Use of this source code is governed by the Apache 2.0
// license that can be found in the LICENSE file.

package sanctions

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"strings"

	"github.com/luxfi/aml/pkg/types"
)

// A Parser turns one publisher's native XML into canonical entries. Transport,
// schema, and persistence are separate concerns: fetch owns the network, a
// Parser owns one schema and touches nothing else, the store owns writing.
type Parser func([]byte) ([]types.SanctionsEntry, error)

// parsers resolves a list source to the parser for its schema. Each publisher
// ships a different root element and a different name model — sdnList,
// CONSOLIDATED_LIST, export, Designations — so there is one parser per source
// and deliberately no default case. An unregistered source is an error rather
// than a fallback to some other publisher's parser, because a mismatched parser
// yields zero entries, and zero entries is indistinguishable from a list on
// which nobody is designated.
var parsers = map[string]Parser{
	types.ListOFACSDN: ParseOFAC,
	types.ListUN:      ParseUN,
	types.ListEU:      ParseEU,
	types.ListHMT:     ParseUK,
}

// ParserFor returns the parser registered for a list source.
func ParserFor(source string) (Parser, error) {
	p, ok := parsers[source]
	if !ok {
		return nil, fmt.Errorf("no parser registered for list source %q", source)
	}
	return p, nil
}

// decode unmarshals XML with entity expansion disabled, so an entity reference
// fails the parse instead of expanding. Strict mode plus an empty entity map
// turns a billion-laughs payload into a parse error rather than an OOM.
func decode(body []byte, v any) error {
	d := xml.NewDecoder(bytes.NewReader(body))
	d.Strict = true
	d.Entity = map[string]string{}
	return d.Decode(v)
}

// join concatenates non-empty parts with a single space.
func join(parts ...string) string {
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, " ")
}

// addAlias appends an alias if it is non-empty and not already present. Lists
// repeat aliases across regulations (an EU entity carries one nameAlias per
// amending regulation), and a duplicate alias costs a matcher pass per copy.
func addAlias(aliases []string, name string) []string {
	if name = strings.TrimSpace(name); name == "" {
		return aliases
	}
	for _, a := range aliases {
		if strings.EqualFold(a, name) {
			return aliases
		}
	}
	return append(aliases, name)
}

// ── OFAC SDN ────────────────────────────────────────────────────────────────
// Root <sdnList>, one <sdnEntry> per designation, names split first/last.

// ParseOFAC parses the OFAC Specially Designated Nationals XML.
func ParseOFAC(body []byte) ([]types.SanctionsEntry, error) {
	var list sdnListXML
	if err := decode(body, &list); err != nil {
		return nil, fmt.Errorf("parse OFAC XML: %w", err)
	}

	entries := make([]types.SanctionsEntry, 0, len(list.Entries))
	for _, e := range list.Entries {
		entryType := types.SanctionIndividual
		switch e.SDNType {
		case "Entity":
			entryType = types.SanctionEntity
		case "Vessel":
			entryType = types.SanctionVessel
		case "Aircraft":
			entryType = types.SanctionAircraft
		}

		var aliases []string
		for _, a := range e.Aliases {
			aliases = addAlias(aliases, join(a.FirstName, a.LastName))
		}

		var dob string
		if len(e.DateOfBirth) > 0 {
			dob = e.DateOfBirth[0].DateOfBirth
		}
		var nationality string
		if len(e.Nationalities) > 0 {
			nationality = e.Nationalities[0].Country
		}

		entries = append(entries, types.SanctionsEntry{
			RefID:       fmt.Sprintf("%d", e.UID),
			Name:        join(e.FirstName, e.LastName),
			Aliases:     aliases,
			DOB:         dob,
			Nationality: nationality,
			Type:        entryType,
		})
	}
	return entries, nil
}

type sdnListXML struct {
	XMLName xml.Name      `xml:"sdnList"`
	Entries []sdnEntryXML `xml:"sdnEntry"`
}

type sdnEntryXML struct {
	UID           int           `xml:"uid"`
	FirstName     string        `xml:"firstName"`
	LastName      string        `xml:"lastName"`
	SDNType       string        `xml:"sdnType"`
	Aliases       []sdnAliasXML `xml:"akaList>aka"`
	DateOfBirth   []sdnDOBXML   `xml:"dateOfBirthList>dateOfBirthItem"`
	Nationalities []sdnNatXML   `xml:"nationalityList>nationality"`
}

type sdnAliasXML struct {
	FirstName string `xml:"firstName"`
	LastName  string `xml:"lastName"`
}

type sdnDOBXML struct {
	DateOfBirth string `xml:"dateOfBirth"`
}

type sdnNatXML struct {
	Country string `xml:"country"`
}

// ── UN Consolidated List ────────────────────────────────────────────────────
// Root <CONSOLIDATED_LIST>, split into <INDIVIDUALS> and <ENTITIES>. Both
// subject types carry the same record shape with type-prefixed element names
// (INDIVIDUAL_ALIAS versus ENTITY_ALIAS), so one struct declares both paths and
// exactly one set populates for any given record.

// ParseUN parses the UN Security Council consolidated list XML.
func ParseUN(body []byte) ([]types.SanctionsEntry, error) {
	var list unListXML
	if err := decode(body, &list); err != nil {
		return nil, fmt.Errorf("parse UN XML: %w", err)
	}

	entries := make([]types.SanctionsEntry, 0, len(list.Individuals)+len(list.Entities))
	for _, s := range list.Individuals {
		entries = append(entries, s.entry(types.SanctionIndividual))
	}
	for _, s := range list.Entities {
		entries = append(entries, s.entry(types.SanctionEntity))
	}
	return entries, nil
}

type unListXML struct {
	XMLName     xml.Name       `xml:"CONSOLIDATED_LIST"`
	Individuals []unSubjectXML `xml:"INDIVIDUALS>INDIVIDUAL"`
	Entities    []unSubjectXML `xml:"ENTITIES>ENTITY"`
}

type unSubjectXML struct {
	DataID      string      `xml:"DATAID"`
	Reference   string      `xml:"REFERENCE_NUMBER"`
	First       string      `xml:"FIRST_NAME"`
	Second      string      `xml:"SECOND_NAME"`
	Third       string      `xml:"THIRD_NAME"`
	Fourth      string      `xml:"FOURTH_NAME"`
	Nationality string      `xml:"NATIONALITY>VALUE"`
	IndAliases  []unAlias   `xml:"INDIVIDUAL_ALIAS"`
	EntAliases  []unAlias   `xml:"ENTITY_ALIAS"`
	IndAddress  []unAddress `xml:"INDIVIDUAL_ADDRESS"`
	EntAddress  []unAddress `xml:"ENTITY_ADDRESS"`
	DOB         []unDOB     `xml:"INDIVIDUAL_DATE_OF_BIRTH"`
}

type unAlias struct {
	Quality string `xml:"QUALITY"`
	Name    string `xml:"ALIAS_NAME"`
}

type unAddress struct {
	Street  string `xml:"STREET"`
	City    string `xml:"CITY"`
	Country string `xml:"COUNTRY"`
}

// unDOB holds a UN date of birth, which is frequently partial: of the dated
// records, some carry a full DATE, some a YEAR alone, and some a FROM_YEAR /
// TO_YEAR range under TYPE_OF_DATE "BETWEEN".
type unDOB struct {
	TypeOfDate string `xml:"TYPE_OF_DATE"`
	Date       string `xml:"DATE"`
	Year       string `xml:"YEAR"`
	FromYear   string `xml:"FROM_YEAR"`
	ToYear     string `xml:"TO_YEAR"`
}

// value renders the date at the precision the list actually carries, so a
// year-only record is not padded into a spurious exact date.
func (d unDOB) value() string {
	switch {
	case strings.TrimSpace(d.Date) != "":
		return strings.TrimSpace(d.Date)
	case strings.TrimSpace(d.FromYear) != "" && strings.TrimSpace(d.ToYear) != "":
		return strings.TrimSpace(d.FromYear) + "/" + strings.TrimSpace(d.ToYear)
	default:
		return strings.TrimSpace(d.Year)
	}
}

func (s unSubjectXML) entry(entryType string) types.SanctionsEntry {
	var aliases []string
	for _, a := range append(s.IndAliases, s.EntAliases...) {
		aliases = addAlias(aliases, a.Name)
	}

	var address string
	for _, a := range append(s.IndAddress, s.EntAddress...) {
		if address = join(a.Street, a.City, a.Country); address != "" {
			break
		}
	}

	var dob string
	for _, d := range s.DOB {
		if dob = d.value(); dob != "" {
			break
		}
	}

	// REFERENCE_NUMBER is the human-facing designation reference (CDi.001) and
	// DATAID the stable numeric key. RefID takes DATAID so a re-listing under a
	// new reference does not read as a new entry.
	ref := strings.TrimSpace(s.DataID)
	if ref == "" {
		ref = strings.TrimSpace(s.Reference)
	}

	return types.SanctionsEntry{
		RefID:       ref,
		Name:        join(s.First, s.Second, s.Third, s.Fourth),
		Aliases:     aliases,
		DOB:         dob,
		Nationality: strings.TrimSpace(s.Nationality),
		Address:     address,
		Type:        entryType,
	}
}

// ── EU Financial Sanctions File ─────────────────────────────────────────────
// Root <export>, one <sanctionEntity> per designation. Every name is a
// <nameAlias> and the file marks no primary name: the strong attribute is
// "true" on every alias, so it carries no signal to select one. The first
// nameAlias is taken as the display name and all of them are matched against,
// which is what makes the absence of a primary marker harmless here.

// ParseEU parses the EU consolidated financial sanctions list XML.
func ParseEU(body []byte) ([]types.SanctionsEntry, error) {
	var list euExportXML
	if err := decode(body, &list); err != nil {
		return nil, fmt.Errorf("parse EU XML: %w", err)
	}

	entries := make([]types.SanctionsEntry, 0, len(list.Entities))
	for _, e := range list.Entities {
		// classificationCode is P for a natural person and E for an entity.
		entryType := types.SanctionEntity
		if strings.EqualFold(e.SubjectType.Classification, "P") ||
			strings.EqualFold(e.SubjectType.Code, "person") {
			entryType = types.SanctionIndividual
		}

		var name string
		var aliases []string
		for _, a := range e.Aliases {
			n := strings.TrimSpace(a.WholeName)
			if n == "" {
				n = join(a.FirstName, a.MiddleName, a.LastName)
			}
			if n == "" {
				continue
			}
			if name == "" {
				name = n
				continue
			}
			aliases = addAlias(aliases, n)
		}

		var dob string
		for _, b := range e.Birthdates {
			if dob = strings.TrimSpace(b.Birthdate); dob != "" {
				break
			}
			if dob = strings.TrimSpace(b.Year); dob != "" {
				break
			}
		}

		var nationality string
		for _, c := range e.Citizenships {
			// "00" is the file's sentinel for an unknown country.
			if code := strings.TrimSpace(c.CountryISO2); code != "" && code != "00" {
				nationality = strings.TrimSpace(c.CountryDescription)
				if nationality == "" {
					nationality = code
				}
				break
			}
		}

		var address string
		for _, a := range e.Addresses {
			if address = join(a.Street, a.City, a.CountryDescription); address != "" {
				break
			}
		}

		// logicalId repeats across element types, so it is not unique on its
		// own; euReferenceNumber identifies the designation.
		ref := strings.TrimSpace(e.EUReference)
		if ref == "" {
			ref = strings.TrimSpace(e.LogicalID)
		}

		entries = append(entries, types.SanctionsEntry{
			RefID:       ref,
			Name:        name,
			Aliases:     aliases,
			DOB:         dob,
			Nationality: nationality,
			Address:     address,
			Type:        entryType,
		})
	}
	return entries, nil
}

type euExportXML struct {
	XMLName  xml.Name    `xml:"export"`
	Entities []euContent `xml:"sanctionEntity"`
}

type euContent struct {
	EUReference  string          `xml:"euReferenceNumber,attr"`
	UnitedNation string          `xml:"unitedNationId,attr"`
	LogicalID    string          `xml:"logicalId,attr"`
	SubjectType  euSubjectType   `xml:"subjectType"`
	Aliases      []euNameAlias   `xml:"nameAlias"`
	Birthdates   []euBirthdate   `xml:"birthdate"`
	Citizenships []euCitizenship `xml:"citizenship"`
	Addresses    []euAddress     `xml:"address"`
}

type euSubjectType struct {
	Code           string `xml:"code,attr"`
	Classification string `xml:"classificationCode,attr"`
}

type euNameAlias struct {
	WholeName  string `xml:"wholeName,attr"`
	FirstName  string `xml:"firstName,attr"`
	MiddleName string `xml:"middleName,attr"`
	LastName   string `xml:"lastName,attr"`
}

type euBirthdate struct {
	Birthdate string `xml:"birthdate,attr"`
	Year      string `xml:"year,attr"`
}

type euCitizenship struct {
	CountryISO2        string `xml:"countryIso2Code,attr"`
	CountryDescription string `xml:"countryDescription,attr"`
}

type euAddress struct {
	Street             string `xml:"street,attr"`
	City               string `xml:"city,attr"`
	CountryDescription string `xml:"countryDescription,attr"`
}

// ── UK Sanctions List ───────────────────────────────────────────────────────
// Root <Designations>. This replaced the OFSI Consolidated List, which closed
// on 28 January 2026 (see lists.go). Names are split Name1..Name5 for given
// names and Name6 for the family or organisation name, and NameType selects the
// primary name — case-inconsistently, which is why it is folded before
// comparison. Non-Latin-script names sit in a separate element and are carried
// as aliases so screening reaches them.

// ParseUK parses the FCDO UK Sanctions List XML.
func ParseUK(body []byte) ([]types.SanctionsEntry, error) {
	var list ukListXML
	if err := decode(body, &list); err != nil {
		return nil, fmt.Errorf("parse UK XML: %w", err)
	}

	entries := make([]types.SanctionsEntry, 0, len(list.Designations))
	for _, d := range list.Designations {
		entryType := types.SanctionEntity
		switch strings.ToLower(strings.TrimSpace(d.Subject)) {
		case "individual":
			entryType = types.SanctionIndividual
		case "ship":
			entryType = types.SanctionVessel
		}

		var name string
		var aliases []string
		for _, n := range d.Names {
			full := join(n.Name1, n.Name2, n.Name3, n.Name4, n.Name5, n.Name6)
			if full == "" {
				continue
			}
			// NameType appears as "Primary Name", "Primary name", "Alias" and
			// "ALias" in the same file, so the comparison folds case. A record
			// may carry more than one primary name; the first wins and the rest
			// are matched as aliases.
			if name == "" && strings.EqualFold(strings.TrimSpace(n.NameType), "primary name") {
				name = full
				continue
			}
			aliases = addAlias(aliases, full)
		}
		// Fall back to the first name of any type rather than emitting a
		// nameless entry, which would be unmatchable.
		if name == "" && len(aliases) > 0 {
			name, aliases = aliases[0], aliases[1:]
		}
		for _, n := range d.NonLatinNames {
			aliases = addAlias(aliases, n)
		}

		var dob string
		for _, raw := range d.DOBs {
			if dob = ukDate(raw); dob != "" {
				break
			}
		}

		var address string
		for _, a := range d.Addresses {
			if address = join(a.Line1, a.Line2, a.Line3, a.Line4, a.Line5, a.Line6, a.Country); address != "" {
				break
			}
		}

		var nationality string
		if len(d.Nationalities) > 0 {
			nationality = strings.TrimSpace(d.Nationalities[0])
		}

		entries = append(entries, types.SanctionsEntry{
			RefID:       strings.TrimSpace(d.UniqueID),
			Name:        name,
			Aliases:     aliases,
			DOB:         dob,
			Nationality: nationality,
			Address:     address,
			Type:        entryType,
		})
	}
	return entries, nil
}

// ukDate normalises a UK list date. The file writes unknown components as the
// literal text "dd" and "mm" — "dd/mm/1945" means the year alone is known — so
// a placeholder component is dropped rather than parsed or padded.
func ukDate(raw string) string {
	parts := strings.Split(strings.TrimSpace(raw), "/")
	if len(parts) != 3 {
		return strings.TrimSpace(raw)
	}
	keep := make([]string, 0, 3)
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" || strings.EqualFold(p, "dd") || strings.EqualFold(p, "mm") ||
			strings.EqualFold(p, "yyyy") {
			continue
		}
		keep = append(keep, p)
	}
	return strings.Join(keep, "/")
}

type ukListXML struct {
	XMLName      xml.Name           `xml:"Designations"`
	Designations []ukDesignationXML `xml:"Designation"`
}

type ukDesignationXML struct {
	UniqueID      string      `xml:"UniqueID"`
	Subject       string      `xml:"IndividualEntityShip"`
	Names         []ukNameXML `xml:"Names>Name"`
	NonLatinNames []string    `xml:"NonLatinNames>NonLatinName>NameNonLatinScript"`
	Addresses     []ukAddress `xml:"Addresses>Address"`
	DOBs          []string    `xml:"IndividualDetails>Individual>DOBs>DOB"`
	Nationalities []string    `xml:"IndividualDetails>Individual>Nationalities>Nationality"`
}

type ukNameXML struct {
	Name1    string `xml:"Name1"`
	Name2    string `xml:"Name2"`
	Name3    string `xml:"Name3"`
	Name4    string `xml:"Name4"`
	Name5    string `xml:"Name5"`
	Name6    string `xml:"Name6"`
	NameType string `xml:"NameType"`
}

type ukAddress struct {
	Line1   string `xml:"AddressLine1"`
	Line2   string `xml:"AddressLine2"`
	Line3   string `xml:"AddressLine3"`
	Line4   string `xml:"AddressLine4"`
	Line5   string `xml:"AddressLine5"`
	Line6   string `xml:"AddressLine6"`
	Country string `xml:"AddressCountry"`
}
