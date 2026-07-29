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
	"net"
	"net/http"
	"net/url"
	"strings"
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

// privateNetworks lists CIDR ranges that must be rejected for webhook URLs.
// RED-18: Prevents SSRF by blocking delivery to internal/private networks.
var privateNetworks = []string{
	"10.0.0.0/8",
	"172.16.0.0/12",
	"192.168.0.0/16",
	"127.0.0.0/8",
	"169.254.0.0/16", // link-local + cloud metadata
	"::1/128",
	"fc00::/7",  // unique local
	"fe80::/10", // link-local IPv6
}

// internalHostSuffixes are DNS suffixes that resolve to internal services.
var internalHostSuffixes = []string{
	".svc.cluster.local",
	".internal",
	".local",
}

// ValidateURL checks that a webhook URL is safe to deliver to.
// Rejects private IPs, localhost, cloud metadata, and K8s internal hostnames.
func ValidateURL(rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}

	if parsed.Scheme != "https" {
		return fmt.Errorf("webhook URL must use HTTPS")
	}

	hostname := parsed.Hostname()

	// Check internal hostname suffixes.
	lower := strings.ToLower(hostname)
	for _, suffix := range internalHostSuffixes {
		if strings.HasSuffix(lower, suffix) {
			return fmt.Errorf("webhook URL hostname %q is internal", hostname)
		}
	}

	// Block well-known metadata hostnames.
	if lower == "metadata.google.internal" || lower == "metadata" {
		return fmt.Errorf("webhook URL hostname %q is a cloud metadata endpoint", hostname)
	}

	// Resolve hostname and check all IPs against private ranges.
	ips, err := net.LookupHost(hostname)
	if err != nil {
		// If resolution fails, check if hostname is already an IP.
		ip := net.ParseIP(hostname)
		if ip != nil {
			ips = []string{hostname}
		} else {
			return fmt.Errorf("cannot resolve hostname %q: %w", hostname, err)
		}
	}

	for _, ipStr := range ips {
		ip := net.ParseIP(ipStr)
		if ip == nil {
			continue
		}
		for _, cidr := range privateNetworks {
			_, network, _ := net.ParseCIDR(cidr)
			if network != nil && network.Contains(ip) {
				return fmt.Errorf("webhook URL resolves to private IP %s", ipStr)
			}
		}
	}

	return nil
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
