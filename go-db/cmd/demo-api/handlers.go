package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/go-db/engine"
	"github.com/lowaa/slop-factory/go-db/tenancy"
)

func registerRoutes(mux *http.ServeMux, appEng engine.Engine) {
	mux.HandleFunc("GET /healthz", handleHealthz(appEng))
	mux.HandleFunc("POST /tenants", handleCreateTenant(appEng))
	mux.HandleFunc("GET /tenants", handleListTenants(appEng))
	mux.HandleFunc("POST /accounts", handleCreateAccount(appEng))
	mux.HandleFunc("GET /accounts", handleListAccounts(appEng))
}

type tenant struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type account struct {
	ID           string `json:"id"`
	TenantID     string `json:"tenant_id"`
	Name         string `json:"name"`
	CurrencyCode string `json:"currency_code"`
}

func handleHealthz(eng engine.Engine) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := eng.Ping(r.Context()); err != nil {
			writeError(w, http.StatusServiceUnavailable, "database unreachable")
			return
		}
		w.WriteHeader(http.StatusOK)
	}
}

// handleCreateTenant writes to global_tenants: a global table, so this uses
// eng directly (no tenant context needed or accepted).
func handleCreateTenant(eng engine.Engine) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Name == "" {
			writeError(w, http.StatusBadRequest, "name is required")
			return
		}

		t := tenant{ID: uuid.New().String(), Name: body.Name}
		_, err := eng.ExecContext(r.Context(),
			`INSERT INTO global_tenants (id, name) VALUES ($1, $2)`, t.ID, t.Name,
		)
		if err != nil {
			log.Printf("create tenant: %v", err)
			writeError(w, http.StatusInternalServerError, "failed to create tenant")
			return
		}

		writeJSON(w, http.StatusCreated, t)
	}
}

func handleListTenants(eng engine.Engine) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rows, err := eng.QueryContext(r.Context(), `SELECT id, name FROM global_tenants ORDER BY created_at`)
		if err != nil {
			log.Printf("list tenants: %v", err)
			writeError(w, http.StatusInternalServerError, "failed to list tenants")
			return
		}
		defer rows.Close()

		tenants := []tenant{}
		for rows.Next() {
			var t tenant
			if err := rows.Scan(&t.ID, &t.Name); err != nil {
				log.Printf("scan tenant: %v", err)
				writeError(w, http.StatusInternalServerError, "failed to list tenants")
				return
			}
			tenants = append(tenants, t)
		}
		writeJSON(w, http.StatusOK, tenants)
	}
}

// tenantIDFromRequest resolves the tenant from the X-Tenant-ID header --
// the one canonical source this demo uses -- and fails closed (no tenant
// context set) if it's missing or not a valid UUID.
func tenantIDFromRequest(r *http.Request) (context.Context, uuid.UUID, bool) {
	raw := r.Header.Get("X-Tenant-ID")
	id, err := uuid.Parse(raw)
	if err != nil {
		return r.Context(), uuid.Nil, false
	}
	return tenancy.ContextWithTenantID(r.Context(), id), id, true
}

func handleCreateAccount(eng engine.Engine) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, tenantID, ok := tenantIDFromRequest(r)
		if !ok {
			writeError(w, http.StatusBadRequest, "X-Tenant-ID header must be a valid UUID")
			return
		}

		var body struct {
			Name         string `json:"name"`
			CurrencyCode string `json:"currency_code"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Name == "" || body.CurrencyCode == "" {
			writeError(w, http.StatusBadRequest, "name and currency_code are required")
			return
		}

		tx, err := tenancy.NewTenantTx(ctx, eng, tenantID)
		if err != nil {
			log.Printf("new tenant tx: %v", err)
			writeError(w, http.StatusInternalServerError, "failed to create account")
			return
		}

		a := account{ID: uuid.New().String(), TenantID: tenantID.String(), Name: body.Name, CurrencyCode: body.CurrencyCode}
		_, err = tx.ExecContext(ctx,
			`INSERT INTO accounts (id, tenant_id, name, currency_code) VALUES ($1, $2, $3, $4)`,
			a.ID, a.TenantID, a.Name, a.CurrencyCode,
		)
		if err != nil {
			_ = tx.Rollback(ctx)
			log.Printf("create account: %v", err)
			writeError(w, http.StatusInternalServerError, "failed to create account")
			return
		}
		if err := tx.Commit(ctx); err != nil {
			log.Printf("commit account: %v", err)
			writeError(w, http.StatusInternalServerError, "failed to create account")
			return
		}

		writeJSON(w, http.StatusCreated, a)
	}
}

// handleListAccounts is the endpoint that demonstrates tenant isolation
// end-to-end: switching the X-Tenant-ID header switches which accounts are
// visible, enforced by RLS rather than an application-level filter.
func handleListAccounts(eng engine.Engine) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, tenantID, ok := tenantIDFromRequest(r)
		if !ok {
			writeError(w, http.StatusBadRequest, "X-Tenant-ID header must be a valid UUID")
			return
		}

		tx, err := tenancy.NewTenantTx(ctx, eng, tenantID)
		if err != nil {
			log.Printf("new tenant tx: %v", err)
			writeError(w, http.StatusInternalServerError, "failed to list accounts")
			return
		}
		defer func() { _ = tx.Rollback(ctx) }()

		rows, err := tx.QueryContext(ctx, `SELECT id, tenant_id, name, currency_code FROM accounts ORDER BY created_at`)
		if err != nil {
			log.Printf("list accounts: %v", err)
			writeError(w, http.StatusInternalServerError, "failed to list accounts")
			return
		}
		defer rows.Close()

		accounts := []account{}
		for rows.Next() {
			var a account
			if err := rows.Scan(&a.ID, &a.TenantID, &a.Name, &a.CurrencyCode); err != nil {
				log.Printf("scan account: %v", err)
				writeError(w, http.StatusInternalServerError, "failed to list accounts")
				return
			}
			accounts = append(accounts, a)
		}
		writeJSON(w, http.StatusOK, accounts)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
