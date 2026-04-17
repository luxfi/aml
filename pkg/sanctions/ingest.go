package sanctions

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/luxfi/aml/pkg/types"
)

// maxBodySize is the maximum sanctions list download size (100 MB).
const maxBodySize = 100 * 1024 * 1024

// Ingester fetches and parses sanctions lists.
type Ingester struct {
	client *http.Client
}

// NewIngester creates a new Ingester with a 60s timeout.
func NewIngester() *Ingester {
	return &Ingester{
		client: &http.Client{Timeout: 60 * time.Second},
	}
}

// FetchOFAC fetches and parses the OFAC SDN XML list.
func (ing *Ingester) FetchOFAC(ctx context.Context, url string) ([]types.SanctionsEntry, string, error) {
	body, hash, err := ing.fetch(ctx, url)
	if err != nil {
		return nil, "", fmt.Errorf("fetch OFAC: %w", err)
	}

	// RED-05: Use streaming decoder with entity expansion disabled to prevent
	// billion-laughs XML bomb attacks. Strict mode + empty entity map means
	// any entity reference in the XML causes a parse error instead of OOM.
	var sdnList sdnListXML
	d := xml.NewDecoder(bytes.NewReader(body))
	d.Strict = true
	d.Entity = map[string]string{} // disable entity expansion
	if err := d.Decode(&sdnList); err != nil {
		return nil, "", fmt.Errorf("parse OFAC XML: %w", err)
	}

	entries := make([]types.SanctionsEntry, 0, len(sdnList.Entries))
	for _, e := range sdnList.Entries {
		entryType := types.SanctionIndividual
		if e.SDNType == "Entity" {
			entryType = types.SanctionEntity
		} else if e.SDNType == "Vessel" {
			entryType = types.SanctionVessel
		} else if e.SDNType == "Aircraft" {
			entryType = types.SanctionAircraft
		}

		name := strings.TrimSpace(e.FirstName + " " + e.LastName)
		aliases := make([]string, 0, len(e.Aliases))
		for _, a := range e.Aliases {
			an := strings.TrimSpace(a.FirstName + " " + a.LastName)
			if an != "" {
				aliases = append(aliases, an)
			}
		}

		var dob string
		for _, d := range e.DateOfBirth {
			dob = d.DateOfBirth
			break
		}

		var nationality string
		for _, n := range e.Nationalities {
			nationality = n.Country
			break
		}

		entries = append(entries, types.SanctionsEntry{
			RefID:       fmt.Sprintf("%d", e.UID),
			Name:        name,
			Aliases:     aliases,
			DOB:         dob,
			Nationality: nationality,
			Type:        entryType,
		})
	}

	return entries, hash, nil
}

// fetch downloads a URL and returns the body bytes + SHA256 hex hash.
func (ing *Ingester) fetch(ctx context.Context, url string) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent", "luxfi-aml/1.0")

	resp, err := ing.client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("HTTP %d from %s", resp.StatusCode, url)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodySize))
	if err != nil {
		return nil, "", err
	}

	h := sha256.Sum256(body)
	return body, fmt.Sprintf("%x", h), nil
}

// OFAC SDN XML structures.

type sdnListXML struct {
	XMLName xml.Name      `xml:"sdnList"`
	Entries []sdnEntryXML `xml:"sdnEntry"`
}

type sdnEntryXML struct {
	UID           int             `xml:"uid"`
	FirstName     string          `xml:"firstName"`
	LastName      string          `xml:"lastName"`
	SDNType       string          `xml:"sdnType"`
	Aliases       []sdnAliasXML   `xml:"akaList>aka"`
	DateOfBirth   []sdnDOBXML     `xml:"dateOfBirthList>dateOfBirthItem"`
	Nationalities []sdnNatXML     `xml:"nationalityList>nationality"`
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
