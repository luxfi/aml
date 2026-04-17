package chain

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestScreenAddress(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/v2/entities" {
			t.Errorf("path = %s, want /v2/entities", r.URL.Path)
		}
		if r.Header.Get("Token") == "" {
			t.Error("missing Token header")
		}

		resp := map[string]interface{}{
			"risk_score": 0.85,
			"category":   "mixer",
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewClient(server.URL, "test-key")
	result, err := client.ScreenAddress(context.Background(), "0xabc123", "ethereum")
	if err != nil {
		t.Fatalf("ScreenAddress error: %v", err)
	}
	if result.RiskScore != 0.85 {
		t.Errorf("risk_score = %.2f, want 0.85", result.RiskScore)
	}
	if result.Category != "mixer" {
		t.Errorf("category = %q, want mixer", result.Category)
	}
	if result.Address != "0xabc123" {
		t.Errorf("address = %q, want 0xabc123", result.Address)
	}
}

func TestScreenTransaction(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}

		resp := map[string]interface{}{
			"risk_score": 0.3,
			"alerts":     []string{},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewClient(server.URL, "test-key")
	result, err := client.ScreenTransaction(context.Background(), "0xdef456", "ethereum")
	if err != nil {
		t.Fatalf("ScreenTransaction error: %v", err)
	}
	if result.RiskScore != 0.3 {
		t.Errorf("risk_score = %.2f, want 0.3", result.RiskScore)
	}
	if result.TxHash != "0xdef456" {
		t.Errorf("tx_hash = %q, want 0xdef456", result.TxHash)
	}
}

func TestScreenAddress_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"error":"invalid api key"}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, "bad-key")
	_, err := client.ScreenAddress(context.Background(), "0xabc", "ethereum")
	if err == nil {
		t.Error("expected error for 403 response")
	}
}

func TestRegisterWebhook(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/v2/webhooks" {
			t.Errorf("path = %s, want /v2/webhooks", r.URL.Path)
		}

		var body map[string]interface{}
		json.NewDecoder(r.Body).Decode(&body)
		if body["url"] != "https://example.com/webhook" {
			t.Errorf("url = %v, want https://example.com/webhook", body["url"])
		}

		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"id":"wh-001"}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, "test-key")
	err := client.RegisterWebhook(context.Background(), "https://example.com/webhook", []string{"alert.new"})
	if err != nil {
		t.Fatalf("RegisterWebhook error: %v", err)
	}
}

func TestAddressRisk_IsHighRisk(t *testing.T) {
	tests := []struct {
		name     string
		risk     AddressRisk
		wantHigh bool
	}{
		{"high score", AddressRisk{RiskScore: 0.8}, true},
		{"mixer category", AddressRisk{RiskScore: 0.3, Category: "mixer"}, true},
		{"darknet", AddressRisk{RiskScore: 0.5, Category: "darknet"}, true},
		{"sanctions", AddressRisk{RiskScore: 0.4, Category: "sanctions"}, true},
		{"clean", AddressRisk{RiskScore: 0.2, Category: "exchange"}, false},
		{"low score unknown", AddressRisk{RiskScore: 0.1, Category: "unknown"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.risk.IsHighRisk(); got != tt.wantHigh {
				t.Errorf("IsHighRisk() = %v, want %v", got, tt.wantHigh)
			}
		})
	}
}

func TestAddressRisk_IsBlocked(t *testing.T) {
	sanctioned := AddressRisk{Category: "sanctions"}
	if !sanctioned.IsBlocked() {
		t.Error("sanctions category should be blocked")
	}

	mixer := AddressRisk{Category: "mixer"}
	if mixer.IsBlocked() {
		t.Error("mixer should not be blocked (flagged, not blocked)")
	}
}

func TestNewClient(t *testing.T) {
	c := NewClient("https://api.chainalysis.com", "test-key")
	if c.baseURL != "https://api.chainalysis.com" {
		t.Errorf("baseURL = %q", c.baseURL)
	}
	if c.client.Timeout != 30*1e9 {
		t.Error("expected 30s timeout")
	}
}
