// Package webhook provides signed outbound webhook delivery with
// exponential backoff and dead-letter semantics.
package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/luxfi/aml/pkg/types"
)

// MaxRetries is the maximum delivery attempts before dead-lettering.
const MaxRetries = 5

// maxResponseBody is the max response body to read from webhook targets.
const maxResponseBody = 1024

// Payload is the webhook delivery body.
type Payload struct {
	Event     string      `json:"event"`
	Timestamp time.Time   `json:"timestamp"`
	Data      interface{} `json:"data"`
}

// Deliver sends a signed webhook to the subscriber URL.
// The signature is HMAC-SHA256 of the JSON body using the webhook secret.
// Returns nil on 2xx, error otherwise.
func Deliver(ctx context.Context, wh types.Webhook, event string, data interface{}) error {
	payload := Payload{
		Event:     event,
		Timestamp: time.Now().UTC(),
		Data:      data,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("webhook marshal: %w", err)
	}

	sig := Sign(body, wh.Secret)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, wh.URL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("webhook request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Webhook-Signature", sig)
	req.Header.Set("X-Webhook-Event", event)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("webhook deliver: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseBody))

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("webhook %s returned %d", wh.URL, resp.StatusCode)
	}
	return nil
}

// Sign computes HMAC-SHA256 of body using secret and returns hex-encoded.
func Sign(body []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

// Verify checks that a signature matches the expected HMAC-SHA256.
func Verify(body []byte, secret, signature string) bool {
	expected := Sign(body, secret)
	return hmac.Equal([]byte(expected), []byte(signature))
}

// DeliverWithRetry attempts delivery with exponential backoff.
// Sleeps 1s, 2s, 4s, 8s, 16s between retries.
func DeliverWithRetry(ctx context.Context, wh types.Webhook, event string, data interface{}) error {
	var lastErr error
	for attempt := 0; attempt < MaxRetries; attempt++ {
		if err := Deliver(ctx, wh, event, data); err != nil {
			lastErr = err
			backoff := time.Duration(1<<uint(attempt)) * time.Second
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
			}
			continue
		}
		return nil
	}
	return fmt.Errorf("webhook dead-letter after %d attempts: %w", MaxRetries, lastErr)
}
