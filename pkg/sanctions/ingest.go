// Copyright 2024-2026 Lux Partners Limited. All rights reserved.
// Use of this source code is governed by the Apache 2.0
// license that can be found in the LICENSE file.

package sanctions

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/luxfi/aml/pkg/types"
)

// maxBodySize bounds a list download. The largest published list is around
// 30 MB, so 100 MB leaves room for growth while keeping a redirect to something
// unbounded from exhausting memory.
const maxBodySize = 100 * 1024 * 1024

// fetchTimeout covers connect, transfer, and read of a whole list. It is minutes
// rather than seconds because the payloads are tens of megabytes: measured
// against the live sources, the EU file alone does not complete inside 60s, so a
// short timeout fails the fetch on a healthy list.
const fetchTimeout = 5 * time.Minute

// Ingester downloads sanctions lists. It owns transport only; schema knowledge
// lives in the Parser for each source.
type Ingester struct {
	client *http.Client
}

// NewIngester creates an Ingester.
func NewIngester() *Ingester {
	return &Ingester{client: &http.Client{Timeout: fetchTimeout}}
}

// Fetch downloads a list and parses it with the parser registered for its
// source, returning the entries and the SHA-256 of the payload.
//
// A list that parses to zero entries is an error. No published list is empty, so
// an empty parse means the download or the schema is wrong, and reporting that
// as success is the failure that lets a designated party through screening
// against a list nobody knows is blank.
func (ing *Ingester) Fetch(ctx context.Context, list types.SanctionsList) ([]types.SanctionsEntry, string, error) {
	parse, err := ParserFor(list.Source)
	if err != nil {
		return nil, "", err
	}

	body, hash, err := ing.fetch(ctx, list.URL)
	if err != nil {
		return nil, "", fmt.Errorf("fetch %s: %w", list.Source, err)
	}

	entries, err := parse(body)
	if err != nil {
		return nil, "", fmt.Errorf("%s: %w", list.Source, err)
	}
	if len(entries) == 0 {
		return nil, "", fmt.Errorf("%s: parsed 0 entries from %d bytes at %s", list.Source, len(body), list.URL)
	}
	return entries, hash, nil
}

// fetch downloads a URL and returns the body bytes and their SHA-256 hex digest.
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
