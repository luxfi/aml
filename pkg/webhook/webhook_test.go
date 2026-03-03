package webhook

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/luxfi/aml/pkg/types"
)

func TestSign(t *testing.T) {
	sig := Sign([]byte("hello"), "secret")
	if sig == "" {
		t.Fatal("signature should not be empty")
	}
	// Deterministic.
	sig2 := Sign([]byte("hello"), "secret")
	if sig != sig2 {
		t.Error("same input should produce same signature")
	}
	// Different input.
	sig3 := Sign([]byte("world"), "secret")
	if sig == sig3 {
		t.Error("different input should produce different signature")
	}
}

func TestVerify(t *testing.T) {
	body := []byte(`{"event":"test"}`)
	secret := "my-secret-key"
	sig := Sign(body, secret)

	if !Verify(body, secret, sig) {
		t.Error("valid signature should verify")
	}
	if Verify(body, "wrong-secret", sig) {
		t.Error("wrong secret should not verify")
	}
	if Verify([]byte("tampered"), secret, sig) {
		t.Error("tampered body should not verify")
	}
}

func TestDeliver(t *testing.T) {
	var received Payload
	var gotSig, gotEvent string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSig = r.Header.Get("X-Webhook-Signature")
		gotEvent = r.Header.Get("X-Webhook-Event")
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &received)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	wh := types.Webhook{
		URL:    srv.URL,
		Secret: "test-secret",
	}

	err := Deliver(context.Background(), wh, "aml.flagged", map[string]string{"tx_id": "tx1"})
	if err != nil {
		t.Fatalf("Deliver error: %v", err)
	}

	if received.Event != "aml.flagged" {
		t.Errorf("event = %s, want aml.flagged", received.Event)
	}
	if gotEvent != "aml.flagged" {
		t.Errorf("X-Webhook-Event = %s, want aml.flagged", gotEvent)
	}
	if gotSig == "" {
		t.Error("X-Webhook-Signature should not be empty")
	}
}

func TestDeliverFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	wh := types.Webhook{
		URL:    srv.URL,
		Secret: "secret",
	}

	err := Deliver(context.Background(), wh, "test", nil)
	if err == nil {
		t.Error("expected error for 500 response")
	}
}
