package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/amyrm/antimage/internal/panel/resellers"
	"github.com/amyrm/antimage/internal/panel/service"
	"github.com/amyrm/antimage/internal/panel/store"
	"github.com/amyrm/antimage/internal/panel/subjects"
)

// Tenancy over HTTP.
//
// The engine behind these -- the append-only ledger, the credit floor, the
// ceilings, atomic provisioning -- has existed and been tested since the
// reseller engine landed. It had no routes, so none of it was reachable. These
// handlers own HTTP concerns only; permission, scope, the transaction and the
// audit record all live in service.Resellers.

type resellerDTO struct {
	ID            int64  `json:"id"`
	AdminID       int64  `json:"admin_id"`
	DisplayName   string `json:"display_name"`
	Enabled       bool   `json:"enabled"`
	MaxSubjects   *int64 `json:"max_subjects"`
	MaxQuotaBytes *int64 `json:"max_quota_bytes"`
	CreditFloor   int64  `json:"credit_floor"`
	CreatedAt     int64  `json:"created_at"`
	UpdatedAt     int64  `json:"updated_at"`
}

func toResellerDTO(r store.ResellerRow) resellerDTO {
	dto := resellerDTO{
		ID: r.ID, AdminID: r.AdminID, DisplayName: r.DisplayName,
		Enabled: r.Enabled, CreditFloor: r.CreditFloor,
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
	// Nullable in the schema because nil means unlimited, and zero is a real
	// limit meaning "may create nothing". Collapsing them would silently turn
	// an unlimited tenant into a frozen one.
	if r.MaxSubjects.Valid {
		v := r.MaxSubjects.Int64
		dto.MaxSubjects = &v
	}
	if r.MaxQuotaBytes.Valid {
		v := r.MaxQuotaBytes.Int64
		dto.MaxQuotaBytes = &v
	}
	return dto
}

type ledgerDTO struct {
	ID        int64  `json:"id"`
	Delta     int64  `json:"delta"`
	Reason    string `json:"reason"`
	SubjectID *int64 `json:"subject_id"`
	Note      string `json:"note"`
	At        int64  `json:"at"`
}

// writeResellerError maps a service error onto a status.
//
// ErrResellerNotFound is 404 whether the tenant is missing or merely out of
// scope. A 403 would confirm the id is real, which is the enumeration oracle
// the scope predicate is phrased to avoid.
func (d Deps) writeResellerError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, service.ErrResellerNotFound):
		WriteError(w, http.StatusNotFound, "not_found", "reseller not found")
	case errors.Is(err, resellers.ErrInsufficientCredit):
		WriteError(w, http.StatusUnprocessableEntity, "insufficient_credit", err.Error())
	case errors.Is(err, resellers.ErrDisabled):
		WriteError(w, http.StatusUnprocessableEntity, "disabled", err.Error())
	default:
		WriteError(w, http.StatusInternalServerError, "internal", "request failed")
	}
}

func (d Deps) resellerService() *service.Resellers {
	return service.NewResellers(d.Store, resellers.NewStore(d.Store, d.subjectStore(), d.now), d.now)
}

func (d Deps) handleListResellers(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	rows, err := d.resellerService().List(r.Context(), d.svcActor(r, actor))
	if err != nil {
		d.writeServiceError(w, r, actor, "reseller.list", err)
		return
	}
	out := make([]resellerDTO, 0, len(rows))
	for _, row := range rows {
		out = append(out, toResellerDTO(row))
	}
	WriteJSON(w, http.StatusOK, map[string]any{"resellers": out})
}

func (d Deps) handleGetReseller(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	id, err := pathInt64(r, "resellerID")
	if err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "invalid reseller id")
		return
	}
	row, err := d.resellerService().Get(r.Context(), d.svcActor(r, actor), id)
	if err != nil {
		if errors.Is(err, service.ErrResellerNotFound) {
			d.writeResellerError(w, err)
			return
		}
		d.writeServiceError(w, r, actor, "reseller.get", err)
		return
	}
	WriteJSON(w, http.StatusOK, toResellerDTO(*row))
}

type createResellerRequest struct {
	AdminID       int64  `json:"admin_id"`
	DisplayName   string `json:"display_name"`
	MaxSubjects   *int64 `json:"max_subjects"`
	MaxQuotaBytes *int64 `json:"max_quota_bytes"`
	CreditFloor   int64  `json:"credit_floor"`
}

func (d Deps) handleCreateReseller(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	var req createResellerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "malformed request body")
		return
	}

	id, err := d.resellerService().Create(r.Context(), d.svcActor(r, actor), resellers.CreateInput{
		AdminID:       req.AdminID,
		DisplayName:   req.DisplayName,
		MaxSubjects:   req.MaxSubjects,
		MaxQuotaBytes: req.MaxQuotaBytes,
		CreditFloor:   req.CreditFloor,
	})
	if err != nil {
		// admin_id is UNIQUE: one admin operates one tenant, which is what
		// makes "my reseller" resolvable from a session alone.
		if isUniqueViolation(err) {
			WriteError(w, http.StatusConflict, "conflict",
				"that panel user already operates a reseller")
			return
		}
		d.writeServiceError(w, r, actor, "reseller.create", err)
		return
	}
	WriteJSON(w, http.StatusCreated, map[string]any{"id": id})
}

type updateResellerRequest struct {
	DisplayName *string `json:"display_name"`
	Enabled     *bool   `json:"enabled"`
	CreditFloor *int64  `json:"credit_floor"`
	// Present-but-null must mean "unlimited", which a plain *int64 cannot
	// express: it would be indistinguishable from the field being absent. So
	// the raw message is decoded in a second step.
	MaxSubjects   json.RawMessage `json:"max_subjects"`
	MaxQuotaBytes json.RawMessage `json:"max_quota_bytes"`
}

// optionalLimit turns a raw field into the service's double pointer.
//
// Three states have to survive the round trip: absent (leave alone), null (no
// limit) and a number (that limit). Collapsing null into absent would make an
// unlimited tenant unreachable through the API once a limit had been set.
func optionalLimit(raw json.RawMessage) (**int64, error) {
	if len(raw) == 0 {
		return nil, nil // absent
	}
	if string(raw) == "null" {
		var unlimited *int64
		return &unlimited, nil
	}
	var v int64
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	p := &v
	return &p, nil
}

func (d Deps) handleUpdateReseller(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	id, err := pathInt64(r, "resellerID")
	if err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "invalid reseller id")
		return
	}
	var req updateResellerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "malformed request body")
		return
	}

	maxSubjects, err := optionalLimit(req.MaxSubjects)
	if err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "max_subjects must be a number or null")
		return
	}
	maxQuota, err := optionalLimit(req.MaxQuotaBytes)
	if err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "max_quota_bytes must be a number or null")
		return
	}

	err = d.resellerService().Update(r.Context(), d.svcActor(r, actor), id, resellers.UpdateInput{
		DisplayName:   req.DisplayName,
		Enabled:       req.Enabled,
		CreditFloor:   req.CreditFloor,
		MaxSubjects:   maxSubjects,
		MaxQuotaBytes: maxQuota,
	})
	if err != nil {
		if errors.Is(err, service.ErrResellerNotFound) {
			d.writeResellerError(w, err)
			return
		}
		d.writeServiceError(w, r, actor, "reseller.update", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (d Deps) handleGetResellerBalance(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	id, err := pathInt64(r, "resellerID")
	if err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "invalid reseller id")
		return
	}
	balance, err := d.resellerService().Balance(r.Context(), d.svcActor(r, actor), id)
	if err != nil {
		if errors.Is(err, service.ErrResellerNotFound) {
			d.writeResellerError(w, err)
			return
		}
		d.writeServiceError(w, r, actor, "reseller.balance", err)
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{"reseller_id": id, "balance": balance})
}

func (d Deps) handleListResellerLedger(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	id, err := pathInt64(r, "resellerID")
	if err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "invalid reseller id")
		return
	}
	rows, err := d.resellerService().Ledger(
		r.Context(), d.svcActor(r, actor), id, ledgerLimit(r))
	if err != nil {
		if errors.Is(err, service.ErrResellerNotFound) {
			d.writeResellerError(w, err)
			return
		}
		d.writeServiceError(w, r, actor, "reseller.ledger", err)
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{"movements": toLedgerDTOs(rows)})
}

// ledgerLimit reads ?limit=, leaving the bounds to the store.
//
// An unparseable or absent value becomes zero, which ListLedgerScoped clamps to
// its default along with anything negative or above 500. Rejecting a bad limit
// with a 400 would be defensible, but the ceiling has to live in the store
// regardless -- it is what stops an unbounded scan -- and having one place
// decide is what keeps the two ledger routes from drifting apart.
func ledgerLimit(r *http.Request) int {
	raw := r.URL.Query().Get("limit")
	if raw == "" {
		return 0
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil {
		return 0
	}
	return parsed
}

// toLedgerDTOs is shared by the platform and self-service ledger routes so the
// two cannot come to disagree about what a movement looks like.
func toLedgerDTOs(rows []store.LedgerRow) []ledgerDTO {
	out := make([]ledgerDTO, 0, len(rows))
	for _, row := range rows {
		dto := ledgerDTO{
			ID: row.ID, Delta: row.Delta, Reason: row.Reason,
			Note: row.Note, At: row.At,
		}
		if row.SubjectID.Valid {
			v := row.SubjectID.Int64
			dto.SubjectID = &v
		}
		out = append(out, dto)
	}
	return out
}

type creditRequest struct {
	Delta          int64  `json:"delta"`
	Reason         string `json:"reason"`
	Note           string `json:"note"`
	IdempotencyKey string `json:"idempotency_key"`
}

func (d Deps) handleGrantCredit(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	id, err := pathInt64(r, "resellerID")
	if err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "invalid reseller id")
		return
	}
	var req creditRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "malformed request body")
		return
	}
	if req.IdempotencyKey == "" {
		// Required, not defaulted. A credit movement with a generated key
		// cannot be retried safely: the retry would mint the credit twice.
		WriteError(w, http.StatusBadRequest, "bad_request",
			"idempotency_key is required; without one a retry grants the credit twice")
		return
	}

	ledgerID, balance, err := d.resellerService().GrantCredit(
		r.Context(), d.svcActor(r, actor), resellers.CreditInput{
			ResellerID:     id,
			Delta:          req.Delta,
			Reason:         req.Reason,
			Note:           req.Note,
			IdempotencyKey: req.IdempotencyKey,
		})
	if err != nil {
		d.writeServiceError(w, r, actor, "reseller.credit", err)
		return
	}
	WriteJSON(w, http.StatusCreated, map[string]any{
		"ledger_id": ledgerID, "balance": balance,
	})
}

// handleGetMyReseller serves the calling tenant's own record.
//
// Self-service, like /auth/me and the Telegram link routes: the admin id comes
// from the session, never from the request. That is what lets the reseller role
// hold NO reseller:* permission -- a tenant reading their own record is served
// by scope, and granting reseller:read would let one tenant enumerate the
// others.
func (d Deps) handleGetMyReseller(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	bal, err := d.subjectService().Balance(r.Context(), d.svcActor(r, actor))
	if err != nil {
		if errors.Is(err, service.ErrNoReseller) {
			WriteError(w, http.StatusNotFound, "not_found",
				"this account does not operate a reseller")
			return
		}
		d.writeServiceError(w, r, actor, "reseller.me", err)
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{
		"reseller_id":  bal.ResellerID,
		"display_name": bal.DisplayName,
		"enabled":      bal.Enabled,
		"balance":      bal.Balance,
		"credit_floor": bal.CreditFloor,
	})
}

// handleGetMyLedger serves the calling tenant's own credit movements.
//
// The counterpart to handleGetMyReseller, and gated the same way: by SCOPE
// rather than by permission. There is no reseller id in the path, so a tenant
// cannot ask for anybody else's history -- the request has nowhere to put the
// question. That is a stronger guarantee than refusing one, because it holds
// without depending on a check being present and correct.
//
// It also means the reseller role still needs no reseller:* permission.
// Granting reseller:read to reach this would hand a tenant the platform list
// route as well, which is the enumeration this whole design avoids.
//
// A balance without its movements is a number an operator has to take on
// trust. The ledger is append-only and the balance is its sum, so showing the
// movements is what makes the figure checkable by the person being billed.
func (d Deps) handleGetMyLedger(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	rows, err := d.subjectService().Ledger(r.Context(), d.svcActor(r, actor), ledgerLimit(r))
	if err != nil {
		if errors.Is(err, service.ErrNoReseller) {
			// 404 rather than 403, matching /me/reseller: "you are not a
			// tenant" is the same answer whether or not tenancy exists, and it
			// lets the UI tell "no tenancy" from "a tenancy with nothing in it".
			WriteError(w, http.StatusNotFound, "not_found",
				"this account does not operate a reseller")
			return
		}
		d.writeServiceError(w, r, actor, "reseller.me.ledger", err)
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{"movements": toLedgerDTOs(rows)})
}

func (d Deps) handleDeleteReseller(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	id, err := pathInt64(r, "resellerID")
	if err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "invalid reseller id")
		return
	}

	err = d.resellerService().Delete(r.Context(), d.svcActor(r, actor), id)
	switch {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, service.ErrResellerNotFound):
		d.writeResellerError(w, err)
	case errors.Is(err, resellers.ErrHasCustomers):
		// 409 rather than 422: the request is well formed and would be valid
		// once the customers are reassigned or removed. The message says how
		// many, and deactivating is offered because it is what an operator
		// usually wants -- it stops provisioning without cutting anybody off.
		WriteError(w, http.StatusConflict, "conflict",
			err.Error()+"; reassign or remove them first, or set enabled=false to deactivate")
	default:
		d.writeServiceError(w, r, actor, "reseller.delete", err)
	}
}

type provisionRequest struct {
	Name       string  `json:"name"`
	Note       string  `json:"note"`
	ServiceIDs []int64 `json:"service_ids"`
	// Cost is charged to the reseller. Zero provisions without touching the
	// ledger, which is how an operator migrates an existing customer in.
	Cost int64 `json:"cost"`
	// QuotaBytes counts against the reseller's max_quota_bytes ceiling.
	QuotaBytes     int64  `json:"quota_bytes"`
	IdempotencyKey string `json:"idempotency_key"`
}

// handleProvisionSubject creates a customer owned by a reseller, debiting them.
//
// This is the operation the whole engine exists for, and until now it was only
// reachable through CSV import. The debit and the creation share one
// transaction, so a customer nobody paid for and a charge for a customer who
// does not exist are both impossible.
func (d Deps) handleProvisionSubject(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	id, err := pathInt64(r, "resellerID")
	if err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "invalid reseller id")
		return
	}
	var req provisionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "malformed request body")
		return
	}
	if req.IdempotencyKey == "" {
		// Required for the same reason a credit grant needs one: a retry would
		// both double-charge and duplicate the customer.
		WriteError(w, http.StatusBadRequest, "bad_request",
			"idempotency_key is required; without one a retry both double-charges and duplicates the customer")
		return
	}

	// Naming the owner is a billing decision, so it takes the tenancy
	// permission on top of subject:write -- the same rule the CSV import
	// follows. Establishing it here also confirms the tenant is in scope.
	if _, err := d.resellerService().Get(r.Context(), d.svcActor(r, actor), id); err != nil {
		if errors.Is(err, service.ErrResellerNotFound) {
			d.writeResellerError(w, err)
			return
		}
		d.writeServiceError(w, r, actor, "reseller.provision", err)
		return
	}

	out, err := d.subjectService().Provision(r.Context(), d.svcActor(r, actor),
		resellers.ProvisionInput{
			ResellerID: id,
			Cost:       req.Cost,
			QuotaBytes: req.QuotaBytes,
			Subject: subjects.CreateInput{
				Name:       req.Name,
				Note:       req.Note,
				ServiceIDs: req.ServiceIDs,
			},
			IdempotencyKey: req.IdempotencyKey,
		})
	if err != nil {
		switch {
		case errors.Is(err, resellers.ErrInsufficientCredit),
			errors.Is(err, resellers.ErrLimitExceeded),
			errors.Is(err, resellers.ErrDisabled):
			// The engine's own message names the ceiling and how much of it is
			// used, which is more use than a generic refusal.
			WriteError(w, http.StatusUnprocessableEntity, "refused", err.Error())
		default:
			d.writeServiceError(w, r, actor, "reseller.provision", err)
		}
		return
	}

	WriteJSON(w, http.StatusCreated, map[string]any{
		"subject_id": out.SubjectID,
		"ledger_id":  out.LedgerID,
		"balance":    out.Balance,
	})
}
