package payments

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The provider tests pin the trust boundary and the deterministic sandbox:
// an unsigned/tampered/stale webhook never reaches the ledger, the sandbox
// derives deterministic references, and Daraja's callback JSON normalizes
// without leaking PII.

const testWebhookSecret = "payments-unit-test-webhook-secret"

func sandboxForTest() *SandboxProvider {
	return NewSandboxProvider([]byte(testWebhookSecret))
}

func signForTest(t *testing.T, secret string, body []byte, at time.Time) string {
	t.Helper()
	return SignWebhookPayload([]byte(secret), body, at)
}

func TestSandboxInitiateIsDeterministic(t *testing.T) {
	p := sandboxForTest()
	intentID := mustUUID(t, "11111111-1111-1111-1111-111111111111")
	txID := mustUUID(t, "22222222-2222-2222-2222-222222222222")
	amount, err := NewMoney(15_000, CurrencyKES)
	require.NoError(t, err)

	res, err := p.Initiate(context.Background(), InitiateRequest{
		IntentID:      intentID,
		TransactionID: txID,
		Amount:        amount,
		Description:   "Invoice 42",
		PayerPhone:    "+254711000111",
	})
	require.NoError(t, err)
	assert.Equal(t, TxPending, res.Status)
	assert.Equal(t, "SBX-22222222-2222-2222-2222-222222222222", res.ProviderRef)

	// Determinism: the same request yields the same reference.
	res2, err := p.Initiate(context.Background(), InitiateRequest{
		IntentID:      intentID,
		TransactionID: txID,
		Amount:        amount,
	})
	require.NoError(t, err)
	assert.Equal(t, res.ProviderRef, res2.ProviderRef)

	// Invalid money is refused by the adapter itself.
	_, err = p.Initiate(context.Background(), InitiateRequest{
		IntentID:      intentID,
		TransactionID: txID,
		Amount:        Money{AmountMinor: -1, Currency: CurrencyKES},
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrProviderRejected)
}

func TestSandboxQueryReportsInjectedOutcome(t *testing.T) {
	p := sandboxForTest()
	txID := mustUUID(t, "33333333-3333-3333-3333-333333333333")
	ref := SandboxProviderRef(txID)

	amount, _ := NewMoney(5_000, CurrencyKES)
	_, err := p.Initiate(context.Background(), InitiateRequest{
		IntentID:      mustUUID(t, "44444444-4444-4444-4444-444444444444"),
		TransactionID: txID,
		Amount:        amount,
	})
	require.NoError(t, err)

	// Default world: nothing settled yet.
	res, err := p.Query(context.Background(), ref)
	require.NoError(t, err)
	assert.Equal(t, TxPending, res.Status)

	p.SetOutcome(txID, TxSucceeded)
	res, err = p.Query(context.Background(), ref)
	require.NoError(t, err)
	assert.Equal(t, TxSucceeded, res.Status)
	assert.Equal(t, int64(5_000), res.AmountMinor)
	assert.NotEmpty(t, res.ProviderTransactionRef)

	// A malformed reference is a rejection, never a crash.
	_, err = p.Query(context.Background(), "GARBAGE")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrProviderRejected)
}

func TestSandboxRefundCompletesDeterministically(t *testing.T) {
	p := sandboxForTest()
	amount, _ := NewMoney(5_000, CurrencyKES)
	res, err := p.Refund(context.Background(), RefundRequest{
		RefundID:    mustUUID(t, "55555555-5555-5555-5555-555555555555"),
		IntentID:    mustUUID(t, "66666666-6666-6666-6666-666666666666"),
		ProviderRef: "SBX-22222222-2222-2222-2222-222222222222",
		Amount:      amount,
	})
	require.NoError(t, err)
	assert.Equal(t, RefundCompleted, res.Status)
	assert.True(t, strings.HasPrefix(res.ProviderRef, "SBXRF-"))
}

func TestWebhookSignatureTrustBoundary(t *testing.T) {
	p := sandboxForTest()
	now := time.Now()
	body, err := SandboxWebhookBody(WebhookEventTransactionSucceeded,
		"SBX-22222222-2222-2222-2222-222222222222", 15_000, "", now)
	require.NoError(t, err)

	t.Run("valid signature is accepted", func(t *testing.T) {
		r := httptest.NewRequest("POST", "/v1/payments/webhooks/sandbox", bytes.NewReader(body))
		r.Header.Set(WebhookSignatureHeader, signForTest(t, testWebhookSecret, body, now))
		payload, err := p.WebhookEvent(r, body, now)
		require.NoError(t, err)
		assert.Equal(t, WebhookEventTransactionSucceeded, payload.Event)
		assert.Equal(t, int64(15_000), payload.AmountMinor)
	})

	t.Run("missing signature refused", func(t *testing.T) {
		r := httptest.NewRequest("POST", "/x", bytes.NewReader(body))
		_, err := p.WebhookEvent(r, body, now)
		require.Error(t, err)
	})

	t.Run("tampered body refused", func(t *testing.T) {
		tampered := bytes.Replace(body, []byte("15000"), []byte("99999"), 1)
		r := httptest.NewRequest("POST", "/x", bytes.NewReader(tampered))
		r.Header.Set(WebhookSignatureHeader, signForTest(t, testWebhookSecret, body, now))
		_, err := p.WebhookEvent(r, tampered, now)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "does not match")
	})

	t.Run("stale signature refused (replay window)", func(t *testing.T) {
		stale := now.Add(-2 * signatureFreshness)
		r := httptest.NewRequest("POST", "/x", bytes.NewReader(body))
		r.Header.Set(WebhookSignatureHeader, signForTest(t, testWebhookSecret, body, stale))
		_, err := p.WebhookEvent(r, body, now)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "freshness")
	})

	t.Run("wrong secret refused", func(t *testing.T) {
		r := httptest.NewRequest("POST", "/x", bytes.NewReader(body))
		r.Header.Set(WebhookSignatureHeader, signForTest(t, "another-secret", body, now))
		_, err := p.WebhookEvent(r, body, now)
		require.Error(t, err)
	})
}

func TestParseWebhookPayloadValidatesExternalInput(t *testing.T) {
	now := time.Now()
	valid, err := SandboxWebhookBody(WebhookEventTransactionSucceeded, "SBX-1", 1_500, "", now)
	require.NoError(t, err)

	t.Run("valid payload parses", func(t *testing.T) {
		got, err := parseWebhookPayload(valid, "sandbox")
		require.NoError(t, err)
		assert.Equal(t, "sandbox", got.Provider)
	})

	for name, mutate := range map[string]func([]byte) []byte{
		"unknown event": func(b []byte) []byte {
			return bytes.Replace(b, []byte(WebhookEventTransactionSucceeded), []byte("payment.exploded"), 1)
		},
		"foreign provider": func(b []byte) []byte {
			return bytes.Replace(b, []byte(`"provider":"sandbox"`), []byte(`"provider":"daraja"`), 1)
		},
		"foreign currency": func(b []byte) []byte {
			return bytes.Replace(b, []byte(`"currency":"KES"`), []byte(`"currency":"USD"`), 1)
		},
		"zero amount": func(b []byte) []byte {
			return bytes.Replace(b, []byte(`"amount_minor":1500`), []byte(`"amount_minor":0`), 1)
		},
		"negative amount": func(b []byte) []byte {
			return bytes.Replace(b, []byte(`"amount_minor":1500`), []byte(`"amount_minor":-1500`), 1)
		},
		"missing provider_ref": func(b []byte) []byte {
			return bytes.Replace(b, []byte(`"provider_ref":"SBX-1"`), []byte(`"provider_ref":""`), 1)
		},
		"failure reason on success": func(b []byte) []byte {
			return append(bytes.TrimRight(b, "}"), `,"failure_reason":"nope"}`...)
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := parseWebhookPayload(mutate(valid), "sandbox")
			require.Error(t, err)
		})
	}

	t.Run("failed event requires failure_reason", func(t *testing.T) {
		failed, err := SandboxWebhookBody(WebhookEventTransactionFailed, "SBX-1", 1_500, "", now)
		require.NoError(t, err)
		_, err = parseWebhookPayload(failed, "sandbox")
		require.Error(t, err, "failure_reason is missing")

		failed, err = SandboxWebhookBody(WebhookEventTransactionFailed, "SBX-1", 1_500, "cancelled by user", now)
		require.NoError(t, err)
		got, err := parseWebhookPayload(failed, "sandbox")
		require.NoError(t, err)
		assert.Equal(t, "cancelled by user", got.FailureReason)
	})

	t.Run("trailing data refused", func(t *testing.T) {
		_, err := parseWebhookPayload(append(append([]byte{}, valid...), []byte("{}")...), "sandbox")
		require.Error(t, err)
	})

	t.Run("far-future occurred_at refused", func(t *testing.T) {
		future, err := SandboxWebhookBody(WebhookEventTransactionSucceeded, "SBX-1", 1_500, "", now.Add(2*webhookFutureSkew))
		require.NoError(t, err)
		_, err = parseWebhookPayload(future, "sandbox")
		require.Error(t, err)
	})
}

func TestDarajaWebhookNormalization(t *testing.T) {
	now := time.Now()
	p := NewDarajaProvider(DarajaConfig{WebhookSecret: []byte("daraja-webhook-secret")})

	success := []byte(`{"Body":{"stkCallback":{"MerchantRequestID":"29115-34620561-1","` +
		`CheckoutRequestID":"ws_CO_191220191020363925","ResultCode":0,"ResultDesc":"The service request is processed successfully.",` +
		`"CallbackMetadata":{"Item":[{"Name":"Amount","Value":1500},{"Name":"MpesaReceiptNumber","Value":"NLJ7RT61SV"},` +
		`{"Name":"TransactionDate","Value":"20191219102115"},{"Name":"PhoneNumber","Value":254708374149}]}}}}`)
	r := httptest.NewRequest("POST", "/v1/payments/webhooks/daraja", bytes.NewReader(success))
	r.Header.Set(WebhookSignatureHeader, signForTest(t, "daraja-webhook-secret", success, now))
	payload, err := p.WebhookEvent(r, success, now)
	require.NoError(t, err)
	assert.Equal(t, WebhookEventTransactionSucceeded, payload.Event)
	assert.Equal(t, "ws_CO_191220191020363925", payload.ProviderRef)
	assert.Equal(t, int64(1500), payload.AmountMinor)
	// PII minimization: the normalized payload carries no phone number.
	wire, err := json.Marshal(payload)
	require.NoError(t, err)
	assert.NotContains(t, string(wire), "254708374149")

	failure := []byte(`{"Body":{"stkCallback":{"MerchantRequestID":"29115-34620561-1",` +
		`"CheckoutRequestID":"ws_CO_191220191020363926","ResultCode":1032,"ResultDesc":"Request cancelled by user"}}}`)
	r = httptest.NewRequest("POST", "/v1/payments/webhooks/daraja", bytes.NewReader(failure))
	r.Header.Set(WebhookSignatureHeader, signForTest(t, "daraja-webhook-secret", failure, now))
	payload, err = p.WebhookEvent(r, failure, now)
	require.NoError(t, err)
	assert.Equal(t, WebhookEventTransactionFailed, payload.Event)
	assert.Equal(t, "Request cancelled by user", payload.FailureReason)

	t.Run("unsigned daraja callback refused (fail-closed)", func(t *testing.T) {
		r := httptest.NewRequest("POST", "/x", bytes.NewReader(success))
		_, err := p.WebhookEvent(r, success, now)
		require.Error(t, err)
	})

	t.Run("unconfigured secret disables daraja webhooks entirely", func(t *testing.T) {
		open := NewDarajaProvider(DarajaConfig{})
		r := httptest.NewRequest("POST", "/x", bytes.NewReader(success))
		_, err := open.WebhookEvent(r, success, now)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not configured")
	})
}

func TestDarajaConfigFromEnv(t *testing.T) {
	getenv := func(key string) string {
		switch key {
		case envDarajaConsumerKey:
			return "key"
		case envDarajaConsumerSecret:
			return "secret"
		case envDarajaShortcode:
			return "174379"
		case envDarajaPasskey:
			return "passkey"
		case envDarajaCallbackURL:
			return "https://example.com/v1/payments/webhooks/daraja"
		default:
			return ""
		}
	}
	cfg, complete := DarajaConfigFromEnv(getenv)
	assert.True(t, complete)
	assert.Equal(t, defaultDarajaBaseURL, cfg.BaseURL)

	// Missing any STK credential reports the adapter as incomplete.
	_, incomplete := DarajaConfigFromEnv(func(key string) string {
		if key == envDarajaConsumerSecret {
			return ""
		}
		return getenv(key)
	})
	assert.False(t, incomplete)
}

func TestDarajaTimestampAndPasswordFormat(t *testing.T) {
	at := time.Date(2026, 1, 2, 15, 4, 5, 0, time.UTC)
	assert.Equal(t, "20260102150405", darajaTimestamp(at))
	// base64 of "174379" + "passkey" + "20260102150405".
	assert.Equal(t, "MTc0Mzc5cGFzc2tleTIwMjYwMTAyMTUwNDA1", darajaPassword("174379", "passkey", at))
}

func TestDarajaQueryOutcomeMapping(t *testing.T) {
	succeeded, terminal := darajaQueryOutcome("0", "ok")
	assert.Equal(t, TxSucceeded, succeeded.Status)
	assert.True(t, terminal)

	failed, terminal := darajaQueryOutcome("1032", "Request cancelled by user")
	assert.Equal(t, TxFailed, failed.Status)
	assert.Equal(t, "Request cancelled by user", failed.FailureReason)
	assert.True(t, terminal)

	pending, terminal := darajaQueryOutcome("500.001.1001", "still processing")
	assert.Equal(t, TxPending, pending.Status)
	assert.False(t, terminal)
}

func TestDarajaAccountReferenceIsBounded(t *testing.T) {
	id := mustUUID(t, "abcdefab-cdef-abcd-efab-cdefabcdefab")
	ref := darajaAccountReference(id)
	assert.Len(t, ref, 11)
	assert.True(t, strings.HasPrefix(ref, "MVR"))
}

// mustUUID parses a fixed uuid or fails the test.
func mustUUID(t *testing.T, s string) uuid.UUID {
	t.Helper()
	id, err := uuid.Parse(s)
	require.NoError(t, err)
	return id
}

// readWebhookBody covers the size cap.
func TestReadWebhookBodySizeCap(t *testing.T) {
	big := bytes.Repeat([]byte("a"), int(MaxWebhookBodyBytes)+10)
	r := httptest.NewRequest("POST", "/x", bytes.NewReader(big))
	_, err := readWebhookBody(r)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds")

	ok := httptest.NewRequest("POST", "/x", bytes.NewReader([]byte(`{}`)))
	body, err := readWebhookBody(ok)
	require.NoError(t, err)
	assert.Equal(t, []byte("{}"), body)
}
