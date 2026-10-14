package payments

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Webhook trust boundary (issue #62 security section): every webhook is
// HMAC-SHA256-signed over "<timestamp>.<raw body>" with a per-provider
// shared secret and carries the signature in
// X-Motivra-Payments-Signature: "t=<unix>,v1=<hex>". Verification is
// constant-time, freshness-bounded and fail-closed: an absent, malformed,
// stale or mismatching signature refuses the webhook before the body is
// parsed. Provider payloads are external input and are fully re-validated
// after verification.
const (
	// WebhookSignatureHeader carries the signature and its timestamp.
	WebhookSignatureHeader = "X-Motivra-Payments-Signature"

	// MaxWebhookBodyBytes caps the raw webhook body (STK callbacks are a
	// few KB; 64 KiB is generous and bounds parser exposure).
	MaxWebhookBodyBytes int64 = 64 << 10

	// signatureFreshness bounds how old a webhook signature may be: a
	// replayed capture older than this is refused even with a valid HMAC.
	signatureFreshness = 5 * time.Minute

	// webhookFutureSkew bounds how far in the future a provider-attested
	// occurred_at may sit (clock-skew tolerance).
	webhookFutureSkew = 5 * time.Minute

	// maxWebhookFailureReason caps the provider-supplied failure reason.
	maxWebhookFailureReason = 500
)

// Normalized webhook event names (the domain events they drive are
// transaction.succeeded.v1 / transaction.failed.v1).
const (
	WebhookEventTransactionSucceeded = "transaction.succeeded"
	WebhookEventTransactionFailed    = "transaction.failed"
)

// webhookEnvelope is the normalized webhook wire format every adapter
// produces. The sandbox emits it directly; the Daraja adapter translates
// the STK callback into it.
type webhookEnvelope struct {
	Event           string    `json:"event"`
	Provider        string    `json:"provider"`
	ProviderRef     string    `json:"provider_ref"`
	ProviderReceipt string    `json:"provider_receipt,omitempty"`
	AmountMinor     int64     `json:"amount_minor"`
	Currency        string    `json:"currency"`
	FailureReason   string    `json:"failure_reason,omitempty"`
	OccurredAt      time.Time `json:"occurred_at"`
}

// SignWebhookPayload renders the signature header value for body at the
// given time. It is the exact algorithm verifyWebhookSignature checks and
// is exported for the sandbox adapter's local-dev helper and tests.
func SignWebhookPayload(secret, body []byte, at time.Time) string {
	mac := hmac.New(sha256.New, secret)
	fmt.Fprintf(mac, "%d.", at.Unix())
	mac.Write(body)
	return fmt.Sprintf("t=%d,v1=%s", at.Unix(), hex.EncodeToString(mac.Sum(nil)))
}

// verifyWebhookSignature checks a raw webhook request against the shared
// secret: the header must parse, the timestamp must be fresh (replay
// window) and the HMAC must match in constant time.
func verifyWebhookSignature(secret, body []byte, header string, now time.Time) error {
	if len(secret) == 0 {
		return errWebhookUnauthorized("webhook signature verification is not configured")
	}
	if header == "" {
		return errWebhookUnauthorized("webhook signature header %s is missing", WebhookSignatureHeader)
	}
	var tsPart, sigPart string
	for _, part := range strings.Split(header, ",") {
		part = strings.TrimSpace(part)
		switch {
		case strings.HasPrefix(part, "t="):
			tsPart = strings.TrimPrefix(part, "t=")
		case strings.HasPrefix(part, "v1="):
			sigPart = strings.TrimPrefix(part, "v1=")
		}
	}
	if tsPart == "" || sigPart == "" {
		return errWebhookUnauthorized("webhook signature is malformed (want t=<unix>,v1=<hex>)")
	}
	ts, err := strconv.ParseInt(tsPart, 10, 64)
	if err != nil {
		return errWebhookUnauthorized("webhook signature timestamp is not a unix number")
	}
	skew := now.Sub(time.Unix(ts, 0))
	if skew < -signatureFreshness || skew > signatureFreshness {
		return errWebhookUnauthorized("webhook signature timestamp is outside the %s freshness window", signatureFreshness)
	}
	mac := hmac.New(sha256.New, secret)
	fmt.Fprintf(mac, "%d.", ts)
	mac.Write(body)
	want := mac.Sum(nil)
	got, err := hex.DecodeString(sigPart)
	if err != nil {
		return errWebhookUnauthorized("webhook signature is not valid hex")
	}
	if !hmac.Equal(want, got) {
		return errWebhookUnauthorized("webhook signature does not match the payload")
	}
	return nil
}

// parseWebhookPayload decodes and fully validates the normalized webhook
// body for provider. Every field is external input: unknown events,
// foreign providers, non-positive amounts, foreign currencies, oversized
// failure reasons and far-future timestamps are all rejections.
func parseWebhookPayload(body []byte, provider string) (WebhookPayload, error) {
	if len(body) == 0 {
		return WebhookPayload{}, fmt.Errorf("payments: webhook body is empty")
	}
	var env webhookEnvelope
	dec := json.NewDecoder(bytes.NewReader(body))
	if err := dec.Decode(&env); err != nil {
		return WebhookPayload{}, fmt.Errorf("payments: webhook body is not valid JSON: %w", err)
	}
	if err := dec.Decode(new(json.RawMessage)); err == nil {
		return WebhookPayload{}, fmt.Errorf("payments: webhook body carries trailing data")
	}

	switch env.Event {
	case WebhookEventTransactionSucceeded, WebhookEventTransactionFailed:
	default:
		return WebhookPayload{}, fmt.Errorf("payments: webhook event %q is not a known event", env.Event)
	}
	if env.Provider != provider {
		return WebhookPayload{}, fmt.Errorf("payments: webhook provider %q does not match route %q", env.Provider, provider)
	}
	if strings.TrimSpace(env.ProviderRef) == "" {
		return WebhookPayload{}, fmt.Errorf("payments: webhook provider_ref is required")
	}
	if env.Currency != CurrencyKES {
		return WebhookPayload{}, fmt.Errorf("payments: webhook currency %q is not %s", env.Currency, CurrencyKES)
	}
	if env.AmountMinor <= 0 {
		return WebhookPayload{}, fmt.Errorf("payments: webhook amount %d is not a positive integer", env.AmountMinor)
	}
	if len(env.FailureReason) > maxWebhookFailureReason {
		return WebhookPayload{}, fmt.Errorf("payments: webhook failure_reason exceeds %d characters", maxWebhookFailureReason)
	}
	if env.Event == WebhookEventTransactionSucceeded && env.FailureReason != "" {
		return WebhookPayload{}, fmt.Errorf("payments: a succeeded webhook may not carry a failure_reason")
	}
	if env.Event == WebhookEventTransactionFailed && strings.TrimSpace(env.FailureReason) == "" {
		return WebhookPayload{}, fmt.Errorf("payments: a failed webhook must carry a failure_reason")
	}
	if env.OccurredAt.IsZero() {
		return WebhookPayload{}, fmt.Errorf("payments: webhook occurred_at is required")
	}
	if env.OccurredAt.After(time.Now().Add(webhookFutureSkew)) {
		return WebhookPayload{}, fmt.Errorf("payments: webhook occurred_at may not sit more than %s in the future", webhookFutureSkew)
	}

	return WebhookPayload{
		Event:           env.Event,
		Provider:        env.Provider,
		ProviderRef:     env.ProviderRef,
		ProviderReceipt: env.ProviderReceipt,
		AmountMinor:     env.AmountMinor,
		Currency:        env.Currency,
		FailureReason:   env.FailureReason,
		OccurredAt:      env.OccurredAt,
	}, nil
}

// readWebhookBody reads and size-caps the raw webhook body. The body is
// hashed for signature verification BEFORE any parsing, so the trust
// boundary precedes the parser.
func readWebhookBody(r *http.Request) ([]byte, error) {
	if r.Body == nil {
		return nil, fmt.Errorf("payments: webhook body is required")
	}
	body, err := io.ReadAll(http.MaxBytesReader(nil, r.Body, MaxWebhookBodyBytes))
	if err != nil {
		return nil, fmt.Errorf("payments: webhook body exceeds %d bytes or could not be read", MaxWebhookBodyBytes)
	}
	return body, nil
}
