package payments

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/Roy-Wanyoike/Motivra/backend/platform"
)

// maxRequestBytes caps JSON request bodies accepted by the handlers.
const maxRequestBytes int64 = 64 << 10

// Routes registers the Payments HTTP API on r (issue #62):
//
//	POST /v1/payments/intents                          initiate a payment (money-mover roles)
//	GET  /v1/payments/intents                          list intents (authenticated, scoped)
//	GET  /v1/payments/intents/{intentID}               fetch one intent (authenticated, scoped)
//	GET  /v1/payments/intents/{intentID}/transactions  list the intent's settlement attempts (authenticated, scoped)
//	GET  /v1/payments/transactions/{transactionID}     fetch one transaction (authenticated, scoped)
//	POST /v1/payments/intents/{intentID}/refund        request a full refund (money-mover roles)
//	GET  /v1/payments/refunds/{refundID}               fetch one refund (authenticated, scoped)
//	POST /v1/payments/webhooks/{provider}              provider webhook receiver (HMAC-verified, NO JWT)
//	POST /v1/payments/reconcile                        run one reconciliation batch (operator roles)
//
// Reads are tenant-scoped from the verified access token (never client
// input); invisible and unknown resources both answer 404 so responses
// leak no existence information (ADR-0004). Money routes are gated to the
// money-mover roles (GARAGE, DISPATCHER, ADMIN, SUPER_ADMIN); reconciliation
// to the operator roles.
//
// The webhook receiver is deliberately NOT behind the JWT middleware: the
// caller is a payment provider, not an identity principal, and the HMAC
// signature (WebhookSignatureHeader) is the trust boundary — verification
// is fail-closed inside the provider adapters.
func Routes(r chi.Router, svc *Service, rec *Reconciler, requireAuth func(http.HandlerFunc) http.HandlerFunc) {
	requireMoneyMover := func(h http.HandlerFunc) http.HandlerFunc {
		return requireAuth(platform.RequireRole(PaymentRoles, h))
	}
	requireOperator := func(h http.HandlerFunc) http.HandlerFunc {
		return requireAuth(platform.RequireRole(OperatorRoles, h))
	}
	r.Route("/v1/payments/intents", func(r chi.Router) {
		r.Post("/", requireMoneyMover(svc.handleInitiate))
		r.Get("/", requireAuth(svc.handleListIntents))
		r.Route("/{intentID}", func(r chi.Router) {
			r.Get("/", requireAuth(svc.handleGetIntent))
			r.Get("/transactions", requireAuth(svc.handleListTransactions))
			r.Post("/refund", requireMoneyMover(svc.handleRefund))
		})
	})
	r.Get("/v1/payments/transactions/{transactionID}", requireAuth(svc.handleGetTransaction))
	r.Get("/v1/payments/refunds/{refundID}", requireAuth(svc.handleGetRefund))
	r.Post("/v1/payments/webhooks/{provider}", svc.handleWebhook)
	r.Post("/v1/payments/reconcile", requireOperator(handleReconcile(rec)))
}

// initiateRequest is the POST /v1/payments/intents body. provider is
// optional (empty selects the service's default adapter); payer_phone is
// used transiently by STK-push providers and never persisted.
type initiateRequest struct {
	AmountMinor int64      `json:"amount_minor"`
	Currency    string     `json:"currency"`
	Provider    string     `json:"provider,omitempty"`
	CustomerID  *uuid.UUID `json:"customer_id,omitempty"`
	JobID       *uuid.UUID `json:"job_id,omitempty"`
	VehicleID   *uuid.UUID `json:"vehicle_id,omitempty"`
	Description string     `json:"description,omitempty"`
	PayerPhone  string     `json:"payer_phone,omitempty"`
}

// refundRequest is the POST .../refund body. The body itself is optional
// (a bare refund request carries no required fields).
type refundRequest struct {
	Reason string `json:"reason,omitempty"`
}

// intentResponse is the wire form of a payment intent.
type intentResponse struct {
	ID            uuid.UUID  `json:"id"`
	TenantID      *uuid.UUID `json:"tenant_id,omitempty"`
	CustomerID    *uuid.UUID `json:"customer_id,omitempty"`
	CreatedBy     uuid.UUID  `json:"created_by"`
	AmountMinor   int64      `json:"amount_minor"`
	Currency      string     `json:"currency"`
	Status        string     `json:"status"`
	Provider      string     `json:"provider"`
	ProviderRef   string     `json:"provider_ref,omitempty"`
	JobID         *uuid.UUID `json:"job_id,omitempty"`
	VehicleID     *uuid.UUID `json:"vehicle_id,omitempty"`
	Description   string     `json:"description,omitempty"`
	FailureReason string     `json:"failure_reason,omitempty"`
	SucceededAt   *time.Time `json:"succeeded_at,omitempty"`
	FailedAt      *time.Time `json:"failed_at,omitempty"`
	CanceledAt    *time.Time `json:"canceled_at,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

// transactionResponse is the wire form of a settlement attempt.
type transactionResponse struct {
	ID              uuid.UUID  `json:"id"`
	IntentID        uuid.UUID  `json:"intent_id"`
	Provider        string     `json:"provider"`
	ProviderRef     string     `json:"provider_ref,omitempty"`
	ProviderReceipt string     `json:"provider_receipt,omitempty"`
	Status          string     `json:"status"`
	AmountMinor     int64      `json:"amount_minor"`
	Currency        string     `json:"currency"`
	FailureReason   string     `json:"failure_reason,omitempty"`
	OccurredAt      *time.Time `json:"occurred_at,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

// refundResponse is the wire form of a refund.
type refundResponse struct {
	ID            uuid.UUID  `json:"id"`
	IntentID      uuid.UUID  `json:"intent_id"`
	AmountMinor   int64      `json:"amount_minor"`
	Currency      string     `json:"currency"`
	Status        string     `json:"status"`
	Provider      string     `json:"provider"`
	ProviderRef   string     `json:"provider_ref,omitempty"`
	Reason        string     `json:"reason,omitempty"`
	FailureReason string     `json:"failure_reason,omitempty"`
	RequestedBy   *uuid.UUID `json:"requested_by,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

// reconcileResponse is the wire form of one reconciliation run.
type reconcileResponse struct {
	Scanned      int `json:"scanned"`
	Repaired     int `json:"repaired"`
	StillPending int `json:"still_pending"`
	Errors       int `json:"errors"`
}

func newIntentResponse(i *PaymentIntent) intentResponse {
	return intentResponse{
		ID:            i.ID,
		TenantID:      i.TenantID,
		CustomerID:    i.CustomerID,
		CreatedBy:     i.CreatedBy,
		AmountMinor:   i.AmountMinor,
		Currency:      i.Currency,
		Status:        string(i.Status),
		Provider:      i.Provider,
		ProviderRef:   i.ProviderRef,
		JobID:         i.JobID,
		VehicleID:     i.VehicleID,
		Description:   i.Description,
		FailureReason: i.FailureReason,
		SucceededAt:   i.SucceededAt,
		FailedAt:      i.FailedAt,
		CanceledAt:    i.CanceledAt,
		CreatedAt:     i.CreatedAt,
		UpdatedAt:     i.UpdatedAt,
	}
}

func newTransactionResponse(t *Transaction) transactionResponse {
	return transactionResponse{
		ID:              t.ID,
		IntentID:        t.IntentID,
		Provider:        t.Provider,
		ProviderRef:     t.ProviderRef,
		ProviderReceipt: t.ProviderReceipt,
		Status:          string(t.Status),
		AmountMinor:     t.AmountMinor,
		Currency:        t.Currency,
		FailureReason:   t.FailureReason,
		OccurredAt:      t.OccurredAt,
		CreatedAt:       t.CreatedAt,
		UpdatedAt:       t.UpdatedAt,
	}
}

func newRefundResponse(r *Refund) refundResponse {
	return refundResponse{
		ID:            r.ID,
		IntentID:      r.IntentID,
		AmountMinor:   r.AmountMinor,
		Currency:      r.Currency,
		Status:        string(r.Status),
		Provider:      r.Provider,
		ProviderRef:   r.ProviderRef,
		Reason:        r.Reason,
		FailureReason: r.FailureReason,
		RequestedBy:   r.RequestedBy,
		CreatedAt:     r.CreatedAt,
		UpdatedAt:     r.UpdatedAt,
	}
}

// handleInitiate implements POST /v1/payments/intents. Tenant and creator
// come from the verified token claims — never from the body. A
// deterministic provider rejection answers 422 with the FAILED intent row
// recorded in the ledger; an unreachable provider answers 503 leaving the
// intent CREATED and retryable.
func (s *Service) handleInitiate(w http.ResponseWriter, r *http.Request) {
	actorID, scope, ok := actorAndScope(w, r)
	if !ok {
		return
	}
	req, err := platform.DecodeJSON[initiateRequest](r, maxRequestBytes)
	if err != nil {
		platform.WriteError(w, err)
		return
	}

	initiated, err := s.InitiatePayment(r.Context(), InitiateInput{
		TenantID:    scope.TenantID,
		CreatedBy:   actorID,
		CustomerID:  req.CustomerID,
		AmountMinor: req.AmountMinor,
		Currency:    req.Currency,
		Provider:    req.Provider,
		JobID:       req.JobID,
		VehicleID:   req.VehicleID,
		Description: req.Description,
		PayerPhone:  req.PayerPhone,
	})
	if err != nil {
		writeDomainError(w, err)
		return
	}
	resp := struct {
		Intent      intentResponse       `json:"intent"`
		Transaction *transactionResponse `json:"transaction,omitempty"`
	}{Intent: newIntentResponse(&initiated.Intent)}
	if initiated.Transaction != nil {
		tx := newTransactionResponse(initiated.Transaction)
		resp.Transaction = &tx
	}
	writeJSON(w, http.StatusCreated, resp)
}

// handleListIntents implements GET /v1/payments/intents with optional
// ?status=&customer_id=&job_id=&vehicle_id=&limit=&offset= filters.
func (s *Service) handleListIntents(w http.ResponseWriter, r *http.Request) {
	scope, ok := scopeFromRequest(w, r)
	if !ok {
		return
	}
	query := r.URL.Query()

	var filter IntentFilter
	if raw := trimQuery(query.Get("status")); raw != "" {
		status := IntentStatus(raw)
		switch status {
		case IntentCreated, IntentProcessing, IntentSucceeded, IntentFailed, IntentCanceled:
		default:
			platform.WriteError(w, platform.ErrValidation("status is not a known payment intent status",
				platform.FieldError{Field: "status", Issue: "must be one of the documented intent statuses"}))
			return
		}
		filter.Status = &status
	}
	if raw := trimQuery(query.Get("customer_id")); raw != "" {
		id, err := parseQueryUUID("customer_id", raw)
		if err != nil {
			platform.WriteError(w, err)
			return
		}
		filter.CustomerID = &id
	}
	if raw := trimQuery(query.Get("job_id")); raw != "" {
		id, err := parseQueryUUID("job_id", raw)
		if err != nil {
			platform.WriteError(w, err)
			return
		}
		filter.JobID = &id
	}
	if raw := trimQuery(query.Get("vehicle_id")); raw != "" {
		id, err := parseQueryUUID("vehicle_id", raw)
		if err != nil {
			platform.WriteError(w, err)
			return
		}
		filter.VehicleID = &id
	}
	limit, offset, err := listPagination(r, DefaultIntentsLimit, MaxIntentsLimit)
	if err != nil {
		platform.WriteError(w, err)
		return
	}

	intents, err := s.Intents(r.Context(), scope, filter, limit, offset)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	out := make([]intentResponse, 0, len(intents))
	for i := range intents {
		out = append(out, newIntentResponse(&intents[i]))
	}
	writeJSON(w, http.StatusOK, out)
}

// handleGetIntent implements GET /v1/payments/intents/{intentID}.
func (s *Service) handleGetIntent(w http.ResponseWriter, r *http.Request) {
	scope, ok := scopeFromRequest(w, r)
	if !ok {
		return
	}
	intentID, err := uuidFromRequest(r, "intentID")
	if err != nil {
		platform.WriteError(w, err)
		return
	}
	intent, err := s.GetIntent(r.Context(), intentID, scope)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, newIntentResponse(&intent))
}

// handleListTransactions implements GET
// /v1/payments/intents/{intentID}/transactions: the intent must be visible
// to scope first (an invisible intent reads as missing).
func (s *Service) handleListTransactions(w http.ResponseWriter, r *http.Request) {
	scope, ok := scopeFromRequest(w, r)
	if !ok {
		return
	}
	intentID, err := uuidFromRequest(r, "intentID")
	if err != nil {
		platform.WriteError(w, err)
		return
	}
	transactions, err := s.Transactions(r.Context(), intentID, scope)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	out := make([]transactionResponse, 0, len(transactions))
	for i := range transactions {
		out = append(out, newTransactionResponse(&transactions[i]))
	}
	writeJSON(w, http.StatusOK, out)
}

// handleGetTransaction implements GET /v1/payments/transactions/{transactionID}.
func (s *Service) handleGetTransaction(w http.ResponseWriter, r *http.Request) {
	scope, ok := scopeFromRequest(w, r)
	if !ok {
		return
	}
	txID, err := uuidFromRequest(r, "transactionID")
	if err != nil {
		platform.WriteError(w, err)
		return
	}
	tx, err := s.GetTransaction(r.Context(), txID, scope)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, newTransactionResponse(&tx))
}

// handleGetRefund implements GET /v1/payments/refunds/{refundID}.
func (s *Service) handleGetRefund(w http.ResponseWriter, r *http.Request) {
	scope, ok := scopeFromRequest(w, r)
	if !ok {
		return
	}
	refundID, err := uuidFromRequest(r, "refundID")
	if err != nil {
		platform.WriteError(w, err)
		return
	}
	refund, err := s.GetRefund(r.Context(), refundID, scope)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, newRefundResponse(&refund))
}

// handleRefund implements POST /v1/payments/intents/{intentID}/refund: the
// full-refund flow (issue #62). The body is optional (reason only).
func (s *Service) handleRefund(w http.ResponseWriter, r *http.Request) {
	actorID, scope, ok := actorAndScope(w, r)
	if !ok {
		return
	}
	intentID, err := uuidFromRequest(r, "intentID")
	if err != nil {
		platform.WriteError(w, err)
		return
	}
	req, err := decodeOptionalJSON[refundRequest](r, maxRequestBytes)
	if err != nil {
		platform.WriteError(w, err)
		return
	}
	refund, err := s.RefundPayment(r.Context(), intentID, req.Reason, actorID, scope)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, newRefundResponse(&refund))
}

// handleWebhook implements POST /v1/payments/webhooks/{provider}: the
// provider webhook receiver. There is NO JWT here — the provider adapter's
// HMAC verification is the trust boundary and it runs before any parsing.
// A replayed delivery answers 200 {"status":"duplicate"}; a first delivery
// answers 200 {"status":"applied"}.
func (s *Service) handleWebhook(w http.ResponseWriter, r *http.Request) {
	providerName := chi.URLParam(r, "provider")
	if _, err := s.Provider(providerName); err != nil {
		// An unknown provider path is a routing miss, not a payload problem.
		platform.WriteError(w, platform.ErrNotFound("payment provider not configured"))
		return
	}
	body, err := readWebhookBody(r)
	if err != nil {
		platform.WriteError(w, platform.ErrValidation(err.Error()))
		return
	}
	result, err := s.HandleWebhook(r.Context(), providerName, r.Header, body, time.Now())
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// handleReconcile implements POST /v1/payments/reconcile: the
// operator-triggered reconciliation batch (the cmd ticker runs the same
// Reconciler on a schedule).
func handleReconcile(rec *Reconciler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if rec == nil {
			platform.WriteError(w, platform.ErrNotFound("reconciliation is not configured"))
			return
		}
		result, err := rec.Run(r.Context())
		if err != nil {
			writeDomainError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, reconcileResponse(result))
	}
}

// actorAndScope extracts the authenticated actor and scope shared by the
// mutation handlers, writing the error response itself when extraction
// fails.
func actorAndScope(w http.ResponseWriter, r *http.Request) (actorID uuid.UUID, scope Scope, ok bool) {
	claims, present := platform.ClaimsFromContext(r.Context())
	if !present {
		platform.WriteError(w, platform.ErrUnauthorized("authentication required"))
		return uuid.Nil, Scope{}, false
	}
	actorID, err := uuid.Parse(claims.UserID)
	if err != nil {
		platform.WriteError(w, platform.ErrUnauthorized("authenticated subject is not a user identifier"))
		return uuid.Nil, Scope{}, false
	}
	scope, err = ScopeFromClaims(claims)
	if err != nil {
		platform.WriteError(w, err)
		return uuid.Nil, Scope{}, false
	}
	return actorID, scope, true
}

// scopeFromRequest derives the caller's Scope from the validated access
// token in the request context — never from any client-supplied field.
func scopeFromRequest(w http.ResponseWriter, r *http.Request) (Scope, bool) {
	claims, present := platform.ClaimsFromContext(r.Context())
	if !present {
		platform.WriteError(w, platform.ErrUnauthorized("authentication required"))
		return Scope{}, false
	}
	scope, err := ScopeFromClaims(claims)
	if err != nil {
		platform.WriteError(w, err)
		return Scope{}, false
	}
	return scope, true
}

// trimQuery trims a raw query-parameter value.
func trimQuery(raw string) string {
	return strings.TrimSpace(raw)
}

// parseQueryUUID validates a uuid query parameter.
func parseQueryUUID(field, raw string) (uuid.UUID, error) {
	id, err := uuid.Parse(raw)
	if err != nil {
		return uuid.Nil, platform.ErrValidation(field+" is not a valid identifier",
			platform.FieldError{Field: field, Issue: "must be a uuid"})
	}
	return id, nil
}

// uuidFromRequest parses the named path parameter as a UUID.
func uuidFromRequest(r *http.Request, name string) (uuid.UUID, error) {
	raw := chi.URLParam(r, name)
	if raw == "" {
		return uuid.Nil, platform.ErrValidation("resource id is required",
			platform.FieldError{Field: name, Issue: "is required"})
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		return uuid.Nil, platform.ErrValidation("resource id is not a valid identifier",
			platform.FieldError{Field: name, Issue: "must be a uuid"})
	}
	return id, nil
}

// listPagination parses ?limit and ?offset against the given defaults and
// cap.
func listPagination(r *http.Request, def, max int) (limit int, offset int, err error) {
	query := r.URL.Query()
	limit = def
	offset = 0
	if raw := query.Get("limit"); raw != "" {
		n, parseErr := strconv.Atoi(raw)
		if parseErr != nil {
			return 0, 0, platform.ErrValidation("limit is not a number",
				platform.FieldError{Field: "limit", Issue: "must be an integer"})
		}
		limit = n
	}
	if raw := query.Get("offset"); raw != "" {
		n, parseErr := strconv.Atoi(raw)
		if parseErr != nil {
			return 0, 0, platform.ErrValidation("offset is not a number",
				platform.FieldError{Field: "offset", Issue: "must be an integer"})
		}
		offset = n
	}
	if limit < 1 {
		return 0, 0, platform.ErrValidation("limit must be positive",
			platform.FieldError{Field: "limit", Issue: "must be at least 1"})
	}
	if limit > max {
		limit = max
	}
	if offset < 0 {
		return 0, 0, platform.ErrValidation("offset must be non-negative",
			platform.FieldError{Field: "offset", Issue: "must be at least 0"})
	}
	return limit, offset, nil
}

// writeDomainError maps payments domain errors onto platform API errors.
// Anything unrecognized falls through to platform.WriteError, which
// renders unknown errors as a 500 without leaking internals.
func writeDomainError(w http.ResponseWriter, err error) {
	switch {
	case err == nil:
		return
	case errors.Is(err, ErrWebhookUnauthorized):
		// The trust boundary refused the delivery.
		platform.WriteError(w, platform.ErrUnauthorized(err.Error()))
	case errors.Is(err, ErrWebhookRejected):
		platform.WriteError(w, platform.ErrValidation(err.Error()))
	case errors.Is(err, ErrConflict):
		platform.WriteError(w, platform.ErrConflict(err.Error()))
	case errors.Is(err, ErrNotRefundable), errors.Is(err, ErrAlreadyRefunded):
		platform.WriteError(w, platform.ErrConflict(err.Error()))
	case errors.Is(err, ErrInvalidInput):
		// Domain-authored validation text — safe to surface.
		platform.WriteError(w, platform.ErrValidation(err.Error()))
	case errors.Is(err, ErrProviderRejected):
		// Deterministic provider rejection. The provider's raw body excerpts
		// stay out of the response: the full reason is recorded on the
		// ledger's failure_reason, visible through the transaction to scoped
		// readers.
		platform.WriteError(w, platform.ErrValidation("payment request rejected by the provider"))
	case errors.Is(err, ErrProviderUnavailable):
		platform.WriteError(w, &platform.Error{
			Status: http.StatusServiceUnavailable,
			Code:   "provider_unavailable",
			Title:  "Payment provider unavailable",
		})
	case errors.Is(err, ErrIntentNotFound):
		platform.WriteError(w, platform.ErrNotFound("payment intent not found"))
	case errors.Is(err, ErrTransactionNotFound):
		platform.WriteError(w, platform.ErrNotFound("transaction not found"))
	case errors.Is(err, ErrRefundNotFound):
		platform.WriteError(w, platform.ErrNotFound("refund not found"))
	default:
		platform.WriteError(w, err)
	}
}

// decodeOptionalJSON decodes an optional JSON request body: an absent or
// whitespace-only body yields the zero value, a present body must be valid
// JSON within maxBytes, and anything else is a platform validation error.
// Used where the request semantics make the body optional (the refund
// request carries only an optional reason).
func decodeOptionalJSON[T any](r *http.Request, maxBytes int64) (T, error) {
	var out T
	body, err := io.ReadAll(http.MaxBytesReader(nil, r.Body, maxBytes))
	if err != nil {
		return out, platform.ErrValidation("request body exceeds the size limit or could not be read")
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return out, nil
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return out, platform.ErrValidation("request body is not valid JSON: " + err.Error())
	}
	return out, nil
}

// writeJSON renders body as a JSON response with the given status.
func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
