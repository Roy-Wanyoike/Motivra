package payments

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Daraja env-var contract (owner secrets — never in-repo, see
// backend/payments/README.md):
//
//	MOTIVRA_PAYMENTS_DARAJA_BASE_URL        (default https://api.safaricom.co.ke)
//	MOTIVRA_PAYMENTS_DARAJA_CONSUMER_KEY    (owner secret)
//	MOTIVRA_PAYMENTS_DARAJA_CONSUMER_SECRET (owner secret)
//	MOTIVRA_PAYMENTS_DARAJA_SHORTCODE       (paybill/till number)
//	MOTIVRA_PAYMENTS_DARAJA_PASSKEY         (Lipa na M-Pesa passkey)
//	MOTIVRA_PAYMENTS_DARAJA_CALLBACK_URL    (public STK callback URL)
//	MOTIVRA_PAYMENTS_DARAJA_INITIATOR       (reversal API initiator)
//	MOTIVRA_PAYMENTS_DARAJA_SECURITY_CREDENTIAL (reversal API credential)
//	MOTIVRA_PAYMENTS_DARAJA_WEBHOOK_SECRET  (HMAC secret our callback
//	        ingress adds to forwarded callbacks; REQUIRED for webhook
//	        processing — Daraja signs nothing itself)
const (
	envDarajaBaseURL          = "MOTIVRA_PAYMENTS_DARAJA_BASE_URL"
	envDarajaConsumerKey      = "MOTIVRA_PAYMENTS_DARAJA_CONSUMER_KEY"
	envDarajaConsumerSecret   = "MOTIVRA_PAYMENTS_DARAJA_CONSUMER_SECRET"
	envDarajaShortcode        = "MOTIVRA_PAYMENTS_DARAJA_SHORTCODE"
	envDarajaPasskey          = "MOTIVRA_PAYMENTS_DARAJA_PASSKEY"
	envDarajaCallbackURL      = "MOTIVRA_PAYMENTS_DARAJA_CALLBACK_URL"
	envDarajaInitiator        = "MOTIVRA_PAYMENTS_DARAJA_INITIATOR"
	envDarajaSecurityCred     = "MOTIVRA_PAYMENTS_DARAJA_SECURITY_CREDENTIAL"
	envDarajaWebhookSecret    = "MOTIVRA_PAYMENTS_DARAJA_WEBHOOK_SECRET"
	defaultDarajaBaseURL      = "https://api.safaricom.co.ke"
	darajaHTTPTimeout         = 15 * time.Second
	darajaTokenRefreshBuffer  = 60 * time.Second
	maxDarajaFailureReasonLen = maxWebhookFailureReason
)

// DarajaConfig carries the M-Pesa Daraja credentials. They live only in
// the process environment — never in the repository (issue #62).
type DarajaConfig struct {
	BaseURL            string
	ConsumerKey        string
	ConsumerSecret     string
	Shortcode          string
	Passkey            string
	CallbackURL        string
	Initiator          string
	SecurityCredential string
	WebhookSecret      []byte
}

// DarajaConfigFromEnv reads the adapter configuration from the process
// environment. complete reports whether the STK-push subset is present;
// the reversal subset (initiator + security credential) is optional until
// the owner enables refunds through Daraja.
func DarajaConfigFromEnv(getenv func(string) string) (DarajaConfig, bool) {
	cfg := DarajaConfig{
		BaseURL:            getenv(envDarajaBaseURL),
		ConsumerKey:        getenv(envDarajaConsumerKey),
		ConsumerSecret:     getenv(envDarajaConsumerSecret),
		Shortcode:          getenv(envDarajaShortcode),
		Passkey:            getenv(envDarajaPasskey),
		CallbackURL:        getenv(envDarajaCallbackURL),
		Initiator:          getenv(envDarajaInitiator),
		SecurityCredential: getenv(envDarajaSecurityCred),
		WebhookSecret:      []byte(getenv(envDarajaWebhookSecret)),
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = defaultDarajaBaseURL
	}
	complete := cfg.ConsumerKey != "" && cfg.ConsumerSecret != "" &&
		cfg.Shortcode != "" && cfg.Passkey != "" && cfg.CallbackURL != ""
	return cfg, complete
}

// DarajaProvider is the M-Pesa Daraja adapter (STK push + C2B callback
// handling + status query + reversal-based refund). It is selected with
// MOTIVRA_PAYMENTS_PROVIDER=daraja; without owner-provisioned credentials
// the service boots the sandbox adapter instead.
type DarajaProvider struct {
	cfg  DarajaConfig
	http *http.Client

	// cached OAuth token
	mu          sync.Mutex
	oauthToken  string
	tokenExpiry time.Time

	// nowFunc is overridable in tests.
	nowFunc func() time.Time
}

// NewDarajaProvider builds the adapter from cfg.
func NewDarajaProvider(cfg DarajaConfig) *DarajaProvider {
	return &DarajaProvider{
		cfg:     cfg,
		http:    &http.Client{Timeout: darajaHTTPTimeout},
		nowFunc: time.Now,
	}
}

// Name implements Provider.
func (p *DarajaProvider) Name() string { return "daraja" }

// darajaTimestamp renders the STK-push timestamp format (YYYYMMDDHHmmss).
func darajaTimestamp(t time.Time) string {
	return t.Format("20060102150405")
}

// darajaPassword renders base64(shortcode + passkey + timestamp).
func darajaPassword(shortcode, passkey string, t time.Time) string {
	return base64.StdEncoding.EncodeToString([]byte(shortcode + passkey + darajaTimestamp(t)))
}

// cachedToken fetches (and caches) the OAuth token for the API calls.
func (p *DarajaProvider) cachedToken(ctx context.Context) (string, error) {
	p.mu.Lock()
	if p.oauthToken != "" && p.nowFunc().Before(p.tokenExpiry) {
		tok := p.oauthToken
		p.mu.Unlock()
		return tok, nil
	}
	p.mu.Unlock()

	endpoint := p.cfg.BaseURL + "/oauth/v1/generate?grant_type=client_credentials"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", providerUnavailable("build oauth request: %v", err)
	}
	req.SetBasicAuth(url.QueryEscape(p.cfg.ConsumerKey), url.QueryEscape(p.cfg.ConsumerSecret))
	resp, err := p.http.Do(req)
	if err != nil {
		return "", providerUnavailable("oauth call failed: %v", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return "", providerUnavailable("read oauth response: %v", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return "", providerUnavailable("oauth returned %d", resp.StatusCode)
	}
	var granted struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   string `json:"expires_in"`
	}
	if err := json.Unmarshal(raw, &granted); err != nil || granted.AccessToken == "" {
		return "", providerRejected("oauth response is malformed")
	}
	secs, err := strconv.ParseInt(granted.ExpiresIn, 10, 64)
	if err != nil || secs <= 0 {
		secs = 3599
	}
	p.mu.Lock()
	p.oauthToken = granted.AccessToken
	p.tokenExpiry = p.nowFunc().Add(time.Duration(secs)*time.Second - darajaTokenRefreshBuffer)
	p.mu.Unlock()
	return granted.AccessToken, nil
}

// postJSON performs one authenticated Daraja API call and decodes the
// response into out.
func (p *DarajaProvider) postJSON(ctx context.Context, path string, payload any, out any) error {
	tok, err := p.cachedToken(ctx)
	if err != nil {
		return err
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return providerRejected("marshal %s request: %v", path, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.cfg.BaseURL+path, bytes.NewReader(body))
	if err != nil {
		return providerUnavailable("build %s request: %v", path, err)
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.http.Do(req)
	if err != nil {
		return providerUnavailable("%s call failed: %v", path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
	if err != nil {
		return providerUnavailable("read %s response: %v", path, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		// 5xx are retryable (unavailable); 4xx are deterministic rejections.
		if resp.StatusCode >= 500 {
			return providerUnavailable("%s returned %d", path, resp.StatusCode)
		}
		return providerRejected("%s returned %d: %s", path, resp.StatusCode, truncateForError(raw))
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return providerRejected("%s response is malformed: %v", path, err)
	}
	return nil
}

// truncateForError bounds provider body excerpts in errors (no secrets,
// no runaway bodies).
func truncateForError(raw []byte) string {
	s := string(raw)
	if len(s) > 256 {
		s = s[:256]
	}
	return s
}

// Initiate implements Provider via the STK push (Lipa na M-Pesa Online)
// API. A ResponseCode other than "0" is a synchronous rejection; an
// accepted push returns PENDING with the CheckoutRequestID as the
// provider reference the callback correlates by.
func (p *DarajaProvider) Initiate(ctx context.Context, req InitiateRequest) (InitiateResult, error) {
	if req.IntentID == uuid.Nil || req.TransactionID == uuid.Nil {
		return InitiateResult{}, providerRejected("daraja: intent_id and transaction_id are required")
	}
	if err := req.Amount.Validate(); err != nil {
		return InitiateResult{}, providerRejected("daraja: %v", err)
	}
	if strings.TrimSpace(req.PayerPhone) == "" {
		return InitiateResult{}, providerRejected("daraja: payer_phone is required for STK push")
	}

	now := p.nowFunc()
	wholeKES, err := darajaWholeKES(req.Amount)
	if err != nil {
		return InitiateResult{}, err
	}
	payload := map[string]any{
		"BusinessShortCode": p.cfg.Shortcode,
		"Password":          darajaPassword(p.cfg.Shortcode, p.cfg.Passkey, now),
		"Timestamp":         darajaTimestamp(now),
		"TransactionType":   "CustomerPayBillOnline",
		"Amount":            wholeKES, // Daraja takes whole KES only (cents refused above, never truncated)
		"PartyA":            req.PayerPhone,
		"PartyB":            p.cfg.Shortcode,
		"PhoneNumber":       req.PayerPhone,
		"CallBackURL":       p.cfg.CallbackURL,
		"AccountReference":  darajaAccountReference(req.IntentID),
		"TransactionDesc":   darajaDescription(req.Description),
	}
	var resp struct {
		MerchantRequestID   string `json:"MerchantRequestID"`
		CheckoutRequestID   string `json:"CheckoutRequestID"`
		ResponseCode        string `json:"ResponseCode"`
		ResponseDescription string `json:"ResponseDescription"`
		CustomerMessage     string `json:"CustomerMessage"`
	}
	if err := p.postJSON(ctx, "/mpesa/stkpush/v1/processrequest", payload, &resp); err != nil {
		return InitiateResult{}, err
	}
	if resp.ResponseCode != "0" {
		return InitiateResult{}, providerRejected("daraja: STK push refused: %s", resp.ResponseDescription)
	}
	if resp.CheckoutRequestID == "" {
		return InitiateResult{}, providerRejected("daraja: STK push response carries no CheckoutRequestID")
	}
	return InitiateResult{
		Status:      TxPending,
		ProviderRef: resp.CheckoutRequestID,
	}, nil
}

// darajaWholeKES renders the amount Daraja accepts: M-Pesa STK push and
// reversal carry whole KES only. Amounts with a cent remainder are
// REFUSED, never truncated: rounding money at the adapter would settle a
// different amount than the ledger attempted and the attested-amount
// check would then strand the intent in PROCESSING forever (issue #62:
// no rounding anywhere in this domain). Callers set integer-KES amounts.
func darajaWholeKES(amount Money) (int64, error) {
	if amount.AmountMinor%100 != 0 {
		return 0, providerRejected("daraja: %s is not a whole-KES amount (M-Pesa carries no cents); use integer KES minor units divisible by 100", amount.String())
	}
	return amount.AmountMinor / 100, nil
}

// darajaAccountReference renders the intent linkage the STK push carries
// (AccountReference is limited to 12 alphanumeric characters on Daraja:
// "MVR" plus the first 8 hex characters of the intent id is deterministic
// and correlatable).
func darajaAccountReference(intentID uuid.UUID) string {
	return "MVR" + strings.ToUpper(fmt.Sprintf("%x", intentID[:4]))
}

// darajaDescription caps the statement memo at Daraja's limit.
func darajaDescription(desc string) string {
	if len(desc) > 60 {
		desc = desc[:60]
	}
	if strings.TrimSpace(desc) == "" {
		return "Motivra payment"
	}
	return desc
}

// darajaQueryOutcome maps Daraja result codes onto transaction states.
// 0 = success; 1032 = cancelled by the user; 1037 = DS timeout; 1 = the
// balance is insufficient; 2001 = wrong pin; everything else stays
// pending (the push is still making progress, or the code is unknown —
// reconciliation runs again). The bool reports whether the outcome is
// terminal.
func darajaQueryOutcome(resultCode string, resultDesc string) (QueryResult, bool) {
	result := QueryResult{Currency: CurrencyKES}
	switch resultCode {
	case "0":
		result.Status = TxSucceeded
		return result, true
	case "1032", "1037", "1", "2001":
		result.Status = TxFailed
		result.FailureReason = truncateFailureReason(resultDesc)
		return result, true
	default:
		result.Status = TxPending
		return result, false
	}
}

// truncateFailureReason bounds provider-supplied failure text.
func truncateFailureReason(s string) string {
	if len(s) > maxDarajaFailureReasonLen {
		return s[:maxDarajaFailureReasonLen]
	}
	return s
}

// Query implements Provider via the STK push status query API.
func (p *DarajaProvider) Query(ctx context.Context, providerRef string) (QueryResult, error) {
	if strings.TrimSpace(providerRef) == "" {
		return QueryResult{}, providerRejected("daraja: provider_ref is required")
	}
	now := p.nowFunc()
	payload := map[string]any{
		"BusinessShortCode": p.cfg.Shortcode,
		"Password":          darajaPassword(p.cfg.Shortcode, p.cfg.Passkey, now),
		"Timestamp":         darajaTimestamp(now),
		"CheckoutRequestID": providerRef,
	}
	var resp struct {
		ResponseCode        string `json:"ResponseCode"`
		ResponseDescription string `json:"ResponseDescription"`
		ResultCode          string `json:"ResultCode"`
		ResultDesc          string `json:"ResultDesc"`
	}
	if err := p.postJSON(ctx, "/mpesa/stkpushquery/v1/query", payload, &resp); err != nil {
		return QueryResult{}, err
	}
	if resp.ResponseCode != "0" {
		return QueryResult{}, providerRejected("daraja: status query refused: %s", resp.ResponseDescription)
	}
	result, _ := darajaQueryOutcome(resp.ResultCode, resp.ResultDesc)
	result.ProviderRef = providerRef
	result.OccurredAt = p.nowFunc()
	return result, nil
}

// Refund implements Provider via the Daraja reversal API. Reversal is
// asynchronous on Daraja; the request acceptance maps to RefundRequested→
// a completed result is confirmed by the reversal result callback handled
// through the same webhook ingress. This adapter treats an accepted
// reversal request as COMPLETED when the response carries no error, and
// the reconciliation query remains the fallback for the owner's review.
func (p *DarajaProvider) Refund(ctx context.Context, req RefundRequest) (RefundResult, error) {
	if req.RefundID == uuid.Nil || req.IntentID == uuid.Nil {
		return RefundResult{}, providerRejected("daraja: refund_id and intent_id are required")
	}
	if err := req.Amount.Validate(); err != nil {
		return RefundResult{}, providerRejected("daraja: %v", err)
	}
	if p.cfg.Initiator == "" || p.cfg.SecurityCredential == "" {
		return RefundResult{}, providerRejected("daraja: reversal credentials are not configured (owner action)")
	}
	if strings.TrimSpace(req.ProviderRef) == "" {
		return RefundResult{}, providerRejected("daraja: the original payment's provider_ref is required for reversal")
	}

	wholeKES, err := darajaWholeKES(req.Amount)
	if err != nil {
		return RefundResult{}, err
	}
	payload := map[string]any{
		"Initiator":              p.cfg.Initiator,
		"SecurityCredential":     p.cfg.SecurityCredential,
		"CommandID":              "TransactionReversal",
		"TransactionID":          req.ProviderRef,
		"Amount":                 wholeKES, // Daraja takes whole KES only (cents refused above, never truncated)
		"ReceiverParty":          p.cfg.Shortcode,
		"RecieverIdentifierType": "11",
		"ResultURL":              p.cfg.CallbackURL,
		"Remarks":                darajaDescription(req.Reason),
		"QueueTimeOutURL":        p.cfg.CallbackURL,
	}
	var resp struct {
		ResponseCode        string `json:"ResponseCode"`
		ResponseDescription string `json:"ResponseDescription"`
	}
	if err := p.postJSON(ctx, "/mpesa/reversal/v1/request", payload, &resp); err != nil {
		return RefundResult{}, err
	}
	if resp.ResponseCode != "0" {
		return RefundResult{
			Status:        RefundFailed,
			FailureReason: truncateFailureReason(resp.ResponseDescription),
		}, nil
	}
	return RefundResult{
		Status:      RefundCompleted,
		ProviderRef: req.ProviderRef,
	}, nil
}

// darajaWebhookEnvelope is the STK callback wire format.
type darajaWebhookEnvelope struct {
	Body struct {
		StkCallback struct {
			MerchantRequestID string `json:"MerchantRequestID"`
			CheckoutRequestID string `json:"CheckoutRequestID"`
			ResultCode        int    `json:"ResultCode"`
			ResultDesc        string `json:"ResultDesc"`
			CallbackMetadata  *struct {
				Item []struct {
					Name  string          `json:"Name"`
					Value json.RawMessage `json:"Value"`
				} `json:"Item"`
			} `json:"CallbackMetadata"`
		} `json:"stkCallback"`
	} `json:"Body"`
}

// WebhookEvent implements Provider: the callback ingress MUST add the
// HMAC (Daraja signs nothing itself — see README's webhook setup). With
// MOTIVRA_PAYMENTS_DARAJA_WEBHOOK_SECRET unset, daraja webhooks are
// DISABLED (fail-closed), not unsigned.
func (p *DarajaProvider) WebhookEvent(header http.Header, body []byte, now time.Time) (WebhookPayload, error) {
	if err := verifyWebhookSignature(p.cfg.WebhookSecret, body, header.Get(WebhookSignatureHeader), now); err != nil {
		return WebhookPayload{}, err
	}
	var env darajaWebhookEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return WebhookPayload{}, fmt.Errorf("payments: daraja callback body is not valid JSON: %w", err)
	}
	cb := env.Body.StkCallback
	if cb.CheckoutRequestID == "" {
		return WebhookPayload{}, fmt.Errorf("payments: daraja callback carries no CheckoutRequestID")
	}

	amount := int64(0)
	receipt := ""
	if cb.CallbackMetadata != nil {
		for _, item := range cb.CallbackMetadata.Item {
			switch item.Name {
			case "Amount":
				_ = json.Unmarshal(item.Value, &amount)
			case "MpesaReceiptNumber":
				// The provider's settlement identifier: stamped on the
				// transaction with the outcome (provider_receipt).
				_ = json.Unmarshal(item.Value, &receipt)
			}
			// PhoneNumber and other PII in the metadata are deliberately
			// ignored: the ledger stores identifiers and provider
			// references only (PII minimization, issue #62).
		}
	}
	// The STK callback Amount is WHOLE KES — M-Pesa carries no cents (the
	// same discipline darajaWholeKES enforces at initiation), so the
	// callback echoes exactly the whole-KES amount that was pushed. The
	// ledger is minor units: scale it back up, never truncate.
	amountMinor := amount * 100

	payload := WebhookPayload{
		Provider:        p.Name(),
		ProviderRef:     cb.CheckoutRequestID,
		ProviderReceipt: receipt,
		AmountMinor:     amountMinor,
		Currency:        CurrencyKES,
		OccurredAt:      now,
	}
	switch {
	case cb.ResultCode == 0:
		payload.Event = WebhookEventTransactionSucceeded
		if payload.AmountMinor <= 0 {
			return WebhookPayload{}, fmt.Errorf("payments: daraja success callback carries no Amount metadata")
		}
	default:
		payload.Event = WebhookEventTransactionFailed
		payload.FailureReason = truncateFailureReason(cb.ResultDesc)
	}
	return payload, nil
}

// osGetenv adapts os.Getenv to the getenv func signature used by
// DarajaConfigFromEnv.
func osGetenv(key string) string { return os.Getenv(key) }

// DarajaConfigFromEnvironment reads the adapter configuration from the
// real process environment.
func DarajaConfigFromEnvironment() (DarajaConfig, bool) {
	return DarajaConfigFromEnv(osGetenv)
}
