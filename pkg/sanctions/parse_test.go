package sanctions

import (
	"strings"
	"testing"

	"github.com/luxfi/aml/pkg/types"
)

// Each fixture below is reduced from the live file it names and keeps the
// quirks that actually break a parser. Verified against the full downloads:
// OFAC 19,175 entries, UN 1,011, EU 6,017, UK 6,315 — every one of them named.

func only(t *testing.T, entries []types.SanctionsEntry, err error) types.SanctionsEntry {
	t.Helper()
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	return entries[0]
}

func hasAlias(e types.SanctionsEntry, want string) bool {
	for _, a := range e.Aliases {
		if a == want {
			return true
		}
	}
	return false
}

// The UN splits a name across four elements and writes partial dates: a record
// may carry a full DATE, a YEAR alone, or a FROM_YEAR/TO_YEAR range. Padding a
// year into a fake exact date would put invented precision in a screening field.
func TestParseUNPartialDatesAndFourPartNames(t *testing.T) {
	const fixture = `<?xml version="1.0"?>
<CONSOLIDATED_LIST>
  <INDIVIDUALS>
    <INDIVIDUAL>
      <DATAID>6907993</DATAID>
      <REFERENCE_NUMBER>CDi.001</REFERENCE_NUMBER>
      <FIRST_NAME>ERIC</FIRST_NAME><SECOND_NAME>BADEGE</SECOND_NAME>
      <NATIONALITY><VALUE>Democratic Republic of the Congo</VALUE></NATIONALITY>
      <INDIVIDUAL_ALIAS><QUALITY/><ALIAS_NAME/></INDIVIDUAL_ALIAS>
      <INDIVIDUAL_ADDRESS><COUNTRY>Rwanda</COUNTRY></INDIVIDUAL_ADDRESS>
      <INDIVIDUAL_DATE_OF_BIRTH><TYPE_OF_DATE>EXACT</TYPE_OF_DATE><YEAR>1971</YEAR></INDIVIDUAL_DATE_OF_BIRTH>
    </INDIVIDUAL>
    <INDIVIDUAL>
      <DATAID>222</DATAID>
      <FIRST_NAME>ABD</FIRST_NAME><SECOND_NAME>AL</SECOND_NAME><THIRD_NAME>RAHMAN</THIRD_NAME><FOURTH_NAME>YASIN</FOURTH_NAME>
      <INDIVIDUAL_ALIAS><QUALITY>good</QUALITY><ALIAS_NAME>Taha Yasin</ALIAS_NAME></INDIVIDUAL_ALIAS>
      <INDIVIDUAL_DATE_OF_BIRTH><TYPE_OF_DATE>BETWEEN</TYPE_OF_DATE><FROM_YEAR>1958</FROM_YEAR><TO_YEAR>1960</TO_YEAR></INDIVIDUAL_DATE_OF_BIRTH>
    </INDIVIDUAL>
  </INDIVIDUALS>
  <ENTITIES>
    <ENTITY>
      <DATAID>6908402</DATAID><FIRST_NAME>ADF</FIRST_NAME>
      <ENTITY_ALIAS><QUALITY>good</QUALITY><ALIAS_NAME>Allied Democratic Forces</ALIAS_NAME></ENTITY_ALIAS>
    </ENTITY>
  </ENTITIES>
</CONSOLIDATED_LIST>`

	entries, err := ParseUN([]byte(fixture))
	if err != nil {
		t.Fatalf("ParseUN: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("got %d entries, want 3 (2 individuals + 1 entity)", len(entries))
	}

	if entries[0].Name != "ERIC BADEGE" || entries[0].RefID != "6907993" {
		t.Errorf("entry[0] = %q ref %q", entries[0].Name, entries[0].RefID)
	}
	if entries[0].DOB != "1971" {
		t.Errorf("year-only DOB = %q, want %q — a year must not be padded to a date", entries[0].DOB, "1971")
	}
	if entries[0].Nationality != "Democratic Republic of the Congo" || entries[0].Address != "Rwanda" {
		t.Errorf("entry[0] nat %q addr %q", entries[0].Nationality, entries[0].Address)
	}
	// The list ships empty alias elements; they must not become blank aliases,
	// which match everything at zero length.
	if len(entries[0].Aliases) != 0 {
		t.Errorf("empty <ALIAS_NAME/> became an alias: %q", entries[0].Aliases)
	}

	if entries[1].Name != "ABD AL RAHMAN YASIN" {
		t.Errorf("four-part name = %q", entries[1].Name)
	}
	if entries[1].DOB != "1958/1960" {
		t.Errorf("BETWEEN range DOB = %q, want %q", entries[1].DOB, "1958/1960")
	}

	if entries[2].Type != types.SanctionEntity || entries[2].Name != "ADF" {
		t.Errorf("entity = %q/%q", entries[2].Name, entries[2].Type)
	}
	if !hasAlias(entries[2], "Allied Democratic Forces") {
		t.Errorf("ENTITY_ALIAS was dropped: %q", entries[2].Aliases)
	}
}

// The EU file marks no primary name — strong="true" sits on every alias, so it
// carries no signal — and repeats a name once per amending regulation. The first
// name becomes the display name, the rest are matched, and repeats collapse.
func TestParseEUNoPrimaryNameMarkerAndRepeatedAliases(t *testing.T) {
	const fixture = `<?xml version="1.0"?>
<export>
  <sanctionEntity euReferenceNumber="EU.27.28" logicalId="13">
    <subjectType code="person" classificationCode="P"/>
    <nameAlias wholeName="Saddam Hussein Al-Tikriti" strong="true"/>
    <nameAlias wholeName="Abu Ali" strong="true"/>
    <nameAlias wholeName="Abu Ali" strong="true"/>
    <nameAlias firstName="Abou" lastName="Ali" strong="true"/>
    <citizenship countryIso2Code="IQ" countryDescription="IRAQ"/>
    <birthdate birthdate="1937-04-28" year="1937"/>
  </sanctionEntity>
  <sanctionEntity euReferenceNumber="EU.1.2" logicalId="99">
    <subjectType code="enterprise" classificationCode="E"/>
    <nameAlias wholeName="Some Trading Co"/>
    <citizenship countryIso2Code="00" countryDescription=""/>
  </sanctionEntity>
</export>`

	entries, err := ParseEU([]byte(fixture))
	if err != nil {
		t.Fatalf("ParseEU: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(entries))
	}

	e := entries[0]
	if e.Name != "Saddam Hussein Al-Tikriti" || e.Type != types.SanctionIndividual {
		t.Errorf("entry[0] = %q/%q", e.Name, e.Type)
	}
	if e.RefID != "EU.27.28" {
		t.Errorf("RefID = %q — logicalId repeats across element types and is not unique", e.RefID)
	}
	if e.DOB != "1937-04-28" || e.Nationality != "IRAQ" {
		t.Errorf("entry[0] dob %q nat %q", e.DOB, e.Nationality)
	}
	// "Abu Ali" appears twice in the source and must appear once here.
	n := 0
	for _, a := range e.Aliases {
		if a == "Abu Ali" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("repeated alias collapsed to %d copies, want 1: %q", n, e.Aliases)
	}
	if !hasAlias(e, "Abou Ali") {
		t.Errorf("alias assembled from firstName/lastName was dropped: %q", e.Aliases)
	}

	if entries[1].Type != types.SanctionEntity {
		t.Errorf("classificationCode E gave type %q", entries[1].Type)
	}
	// "00" is the file's sentinel for unknown, not a country.
	if entries[1].Nationality != "" {
		t.Errorf("country sentinel 00 became nationality %q", entries[1].Nationality)
	}
}

// The UK list writes unknown date components as the literal text "dd" and "mm",
// spells NameType in six different casings, and keeps non-Latin-script names in
// a separate element that screening still has to reach.
func TestParseUKPlaceholderDatesAndCaseInconsistentNameType(t *testing.T) {
	const fixture = `<?xml version="1.0"?>
<Designations>
  <Designation>
    <UniqueID>AFG0006</UniqueID>
    <Names>
      <Name><Name1>MOHAMMAD</Name1><Name2>HASSAN</Name2><Name6>AKHUND</Name6><NameType>Primary name</NameType></Name>
      <Name><Name6>Wali Mohammad</Name6><NameType>ALias</NameType></Name>
    </Names>
    <NonLatinNames><NonLatinName><NameNonLatinScript>محمد حسن آخوند</NameNonLatinScript></NonLatinName></NonLatinNames>
    <IndividualEntityShip>Individual</IndividualEntityShip>
    <Addresses><Address><AddressLine6>Kabul</AddressLine6><AddressCountry>Afghanistan</AddressCountry></Address></Addresses>
    <IndividualDetails><Individual>
      <DOBs><DOB>dd/mm/1945</DOB></DOBs>
      <Nationalities><Nationality>Afghanistan</Nationality></Nationalities>
    </Individual></IndividualDetails>
  </Designation>
  <Designation>
    <UniqueID>RUS0999</UniqueID>
    <Names><Name><Name6>SOME VESSEL</Name6><NameType>Primary Name</NameType></Name></Names>
    <IndividualEntityShip>Ship</IndividualEntityShip>
  </Designation>
  <Designation>
    <UniqueID>NOPRIMARY1</UniqueID>
    <Names><Name><Name6>Only An Alias</Name6><NameType>Alias</NameType></Name></Names>
    <IndividualEntityShip>Entity</IndividualEntityShip>
  </Designation>
</Designations>`

	entries, err := ParseUK([]byte(fixture))
	if err != nil {
		t.Fatalf("ParseUK: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("got %d entries, want 3", len(entries))
	}

	e := entries[0]
	// "Primary name" here, "Primary Name" elsewhere in the same file.
	if e.Name != "MOHAMMAD HASSAN AKHUND" {
		t.Errorf("Name = %q — NameType case must be folded before comparison", e.Name)
	}
	if e.DOB != "1945" {
		t.Errorf("DOB = %q, want %q — dd/mm are placeholders, not values to parse", e.DOB, "1945")
	}
	if e.Type != types.SanctionIndividual || e.Nationality != "Afghanistan" {
		t.Errorf("entry[0] type %q nat %q", e.Type, e.Nationality)
	}
	if !strings.Contains(e.Address, "Kabul") {
		t.Errorf("Address = %q", e.Address)
	}
	if !hasAlias(e, "Wali Mohammad") {
		t.Errorf("alias typed \"ALias\" was dropped: %q", e.Aliases)
	}
	if !hasAlias(e, "محمد حسن آخوند") {
		t.Errorf("non-Latin-script name is unreachable by screening: %q", e.Aliases)
	}

	if entries[1].Type != types.SanctionVessel {
		t.Errorf("Ship gave type %q, want vessel", entries[1].Type)
	}
	// A designation with no primary name must still be matchable.
	if entries[2].Name != "Only An Alias" {
		t.Errorf("alias-only designation produced name %q — a nameless entry never matches", entries[2].Name)
	}
}

// Every parser must reject a payload written for a different publisher rather
// than return zero entries, which is what made three of four lists look empty.
func TestParsersRejectAnotherPublishersSchema(t *testing.T) {
	bodies := map[string]string{
		"ofac": `<?xml version="1.0"?><sdnList><sdnEntry><uid>1</uid><lastName>X</lastName></sdnEntry></sdnList>`,
		"un":   `<?xml version="1.0"?><CONSOLIDATED_LIST><INDIVIDUALS><INDIVIDUAL><DATAID>1</DATAID><FIRST_NAME>X</FIRST_NAME></INDIVIDUAL></INDIVIDUALS></CONSOLIDATED_LIST>`,
		"eu":   `<?xml version="1.0"?><export><sanctionEntity euReferenceNumber="1"><nameAlias wholeName="X"/></sanctionEntity></export>`,
		"uk":   `<?xml version="1.0"?><Designations><Designation><UniqueID>1</UniqueID><Names><Name><Name6>X</Name6><NameType>Primary Name</NameType></Name></Names></Designation></Designations>`,
	}
	for name, parse := range map[string]Parser{"ofac": ParseOFAC, "un": ParseUN, "eu": ParseEU, "uk": ParseUK} {
		for other, body := range bodies {
			if other == name {
				continue
			}
			if _, err := parse([]byte(body)); err == nil {
				t.Errorf("%s parser accepted the %s schema instead of failing", name, other)
			}
		}
	}
}
