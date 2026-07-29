// Package chain provides blockchain analytics integration for AML screening.
// Supports address risk assessment and transaction exposure analysis.
package chain

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Client is a Chainalysis KYT (Know Your Transaction) API client.
// API key must be stored in KMS and injected at startup.
type Client struct {
	baseURL string
	apiKey  string
	client  *http.Client
}

// maxResponseSize limits API response body reads (1 MB).
const maxResponseSize = 1024 * 1024

// NewClient creates a Chainalysis client. apiKey must come from KMS.
func NewClient(baseURL, apiKey string) *Client {
	return &Client{
		baseURL: baseURL,
		apiKey:  apiKey,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// AddressRisk is the risk assessment for a blockchain address.
type AddressRisk struct {
	Address   string          `json:"address"`
	Chain     string          `json:"chain"`
	RiskScore float64         `json:"risk_score"` // 0.0 (clean) to 1.0 (high risk)
	Category  string          `json:"category"`   // exchange, mixer, darknet, sanctions, unknown
	Cluster   string          `json:"cluster_name,omitempty"`
	Exposure  []ExposureEntry `json:"exposure,omitempty"`
}

// ExposureEntry describes exposure to a particular category.
type ExposureEntry struct {
	Category   string  `json:"category"`
	Percentage float64 `json:"percentage"` // 0-100
	Amount     float64 `json:"amount,omitempty"`
	Currency   string  `json:"currency,omitempty"`
}

// TxRisk is the risk assessment for a blockchain transaction.
type TxRisk struct {
	TxHash           string          `json:"tx_hash"`
	Chain            string          `json:"chain"`
	RiskScore        float64         `json:"risk_score"`
	DirectExposure   []ExposureEntry `json:"direct_exposure,omitempty"`
	IndirectExposure []ExposureEntry `json:"indirect_exposure,omitempty"`
	Alerts           []string        `json:"alerts,omitempty"`
}

// ScreenAddress checks a blockchain address against Chainalysis risk data.
func (c *Client) ScreenAddress(ctx context.Context, address, chain string) (*AddressRisk, error) {
	body := map[string]string{
		"address": address,
		"chain":   chain,
	}

	resp, err := c.post(ctx, "/v2/entities", body)
	if err != nil {
		return nil, fmt.Errorf("screen address: %w", err)
	}

	var result AddressRisk
	if err := json.Unmarshal(resp, &result); err != nil {
		return nil, fmt.Errorf("parse address risk: %w", err)
	}

	result.Address = address
	result.Chain = chain
	return &result, nil
}

// ScreenTransaction checks a transaction against Chainalysis.
func (c *Client) ScreenTransaction(ctx context.Context, txHash, chain string) (*TxRisk, error) {
	path := fmt.Sprintf("/v2/transfers/%s?chain=%s", txHash, chain)

	resp, err := c.get(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("screen transaction: %w", err)
	}

	var result TxRisk
	if err := json.Unmarshal(resp, &result); err != nil {
		return nil, fmt.Errorf("parse tx risk: %w", err)
	}

	result.TxHash = txHash
	result.Chain = chain
	return &result, nil
}

// RegisterWebhook subscribes to Chainalysis alert events.
func (c *Client) RegisterWebhook(ctx context.Context, callbackURL string, events []string) error {
	body := map[string]interface{}{
		"url":    callbackURL,
		"events": events,
	}

	_, err := c.post(ctx, "/v2/webhooks", body)
	if err != nil {
		return fmt.Errorf("register webhook: %w", err)
	}
	return nil
}

// IsHighRisk returns true if the address is categorized as high risk.
func (ar *AddressRisk) IsHighRisk() bool {
	if ar.RiskScore >= 0.7 {
		return true
	}
	switch ar.Category {
	case "mixer", "darknet", "sanctions", "scam", "ransomware", "stolen_funds":
		return true
	}
	return false
}

// IsBlocked returns true if the address is associated with sanctioned entities.
func (ar *AddressRisk) IsBlocked() bool {
	return ar.Category == "sanctions"
}

func (c *Client) post(ctx context.Context, path string, body interface{}) ([]byte, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Token", c.apiKey)

	return c.doRequest(req)
}

func (c *Client) get(ctx context.Context, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Token", c.apiKey)

	return c.doRequest(req)
}

func (c *Client) doRequest(req *http.Request) ([]byte, error) {
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		return nil, err
	}

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("API error %d: %s", resp.StatusCode, string(data))
	}

	return data, nil
}
