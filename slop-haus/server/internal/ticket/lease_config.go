package ticket

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
)

// ServedConfig is the configuration the server serves an agent's session,
// which a lease is bound to when it is granted.
type ServedConfig struct {
	// Revision is the tenant's config revision the settings were read at.
	Revision int64
	// Settings is the effective settings, harness to name to value.
	Settings any
	// Variant is the version the agent runs on, nil for an agent in none.
	Variant *ServedVariant
	// FleetInstructionsVersionID is the version served in the briefings, nil
	// for none.
	FleetInstructionsVersionID *uuid.UUID
	// Run is what the run that claimed reports about itself; set by the claim,
	// not by the resolver.
	Run RunRecord
}

// RunRecord is what a lease records of the run beside the configuration it
// was served. Each field is "" when not known.
type RunRecord struct {
	Harness        string
	HarnessVersion string
	Model          string
	BundleHash     string
}

var bundleHashPattern = regexp.MustCompile(`^[0-9a-f]{1,64}$`)

// reported is the options with what a run reports about itself made safe to
// store: it is free text from the wire, and recording it must never refuse a
// claim, so what does not fit is dropped.
func (o *ClaimOptions) reported() *ClaimOptions {
	out := *o
	if !bundleHashPattern.MatchString(out.BundleHash) {
		out.BundleHash = ""
	}
	out.HarnessVersion = strings.TrimSpace(out.HarnessVersion)
	if utf8.RuneCountInString(out.HarnessVersion) > maxHarnessVersion ||
		strings.ContainsFunc(out.HarnessVersion, unicode.IsControl) {
		out.HarnessVersion = ""
	}
	return &out
}

const maxHarnessVersion = 100

// withRun records the run on a copy of the served configuration. harness is
// the one the claim was checked against (see claimHarness), so what is
// recorded is what passed the check; a variant's harness is the last resort.
// The model is the one the served settings name for it.
func (c *ServedConfig) withRun(harness string, opts *ClaimOptions) *ServedConfig {
	if c == nil {
		return nil
	}
	if harness == "" && c.Variant != nil {
		harness = c.Variant.Harness
	}
	out := *c
	out.Run = RunRecord{
		Harness: harness, HarnessVersion: opts.HarnessVersion, BundleHash: opts.BundleHash,
		Model: modelOf(c.Settings, harness),
	}
	return &out
}

// modelOf reads the model the settings (harness to name to value) name for
// the harness. Settings is held as any because the resolver owns its type.
func modelOf(settings any, harness string) string {
	raw, err := json.Marshal(settings)
	if err != nil {
		return ""
	}
	var byHarness map[string]map[string]any
	if json.Unmarshal(raw, &byHarness) != nil {
		return ""
	}
	model, _ := byHarness[harness]["model"].(string)
	return model
}

// ServedVariant names the variant version an agent runs on.
type ServedVariant struct {
	VersionID uuid.UUID
	Name      string
	Version   int
	Harness   string
}

// ConfigResolver resolves what the agent is served, with the code that serves
// it, so what a lease records cannot differ from what the agent ran on.
type ConfigResolver interface {
	Resolve(ctx context.Context, agentID uuid.UUID) (*ServedConfig, error)
}

// VariantClaimError refuses a claim by an agent in a variant whose session
// runs another harness than the variant's: it would run the attempt on a
// configuration it is not on. Which attempts an agent in a variant may take
// is the routing (attemptVariantMatches), not this.
type VariantClaimError struct {
	Variant *ServedVariant
	// SessionHarness is the harness the agent's current session reports, or
	// the one its claim declared when it has no session, ""
	// when it reports none.
	SessionHarness string
}

// ErrVariantClaim matches a *VariantClaimError.
var ErrVariantClaim = errors.New("ticket: an agent in a variant cannot claim")

func (e *VariantClaimError) Is(target error) bool { return target == ErrVariantClaim }

// HarnessMismatch is whether the session runs a harness other than the variant's.
func (e *VariantClaimError) HarnessMismatch() bool {
	return e.SessionHarness != "" && e.SessionHarness != e.Variant.Harness
}

func (e *VariantClaimError) Error() string {
	v := fmt.Sprintf("variant %s v%d", e.Variant.Name, e.Variant.Version)
	if e.HarnessMismatch() {
		return fmt.Sprintf("This agent is in %s, which runs the %s harness, but this session runs %s. "+
			"Start a session on the %s harness, or ask an owner to take the agent out of the variant. "+
			"No ticket can be claimed.", v, e.Variant.Harness, e.SessionHarness, e.Variant.Harness)
	}
	return fmt.Sprintf("This agent is in %s and runs its harness: nothing to refuse.", v)
}

func resolveServed(ctx context.Context, configs ConfigResolver, agentID uuid.UUID) (*ServedConfig, error) {
	if configs == nil {
		return nil, nil
	}
	served, err := configs.Resolve(ctx, agentID)
	if err != nil {
		return nil, fmt.Errorf("resolve the agent's configuration: %w", err)
	}
	return served, nil
}

// claimHarness is the harness a claim runs on: the session's when the agent
// has one, else the one the claim declared. A headless run has no session yet
// when it claims, so the declared harness stands in; a session that exists is
// the better evidence and wins.
func claimHarness(ctx context.Context, q db.Querier, agentID uuid.UUID, declared string) (string, error) {
	var harness string
	err := q.QueryRow(ctx, `SELECT harness FROM sessions WHERE agent_id = $1 AND ended_at IS NULL`, agentID).Scan(&harness)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", fmt.Errorf("read the session's harness: %w", err)
	}
	if harness == "" {
		harness = declared
	}
	return harness, nil
}

// checkVariantClaim refuses a claim by an agent in a variant whose run is on
// another harness (see claimHarness).
func checkVariantClaim(served *ServedConfig, harness string) error {
	if served == nil || served.Variant == nil {
		return nil
	}
	refused := &VariantClaimError{Variant: served.Variant, SessionHarness: harness}
	if !refused.HarnessMismatch() {
		return nil
	}
	return refused
}

// recordLeaseConfig binds the configuration served to the lease just granted.
func recordLeaseConfig(ctx context.Context, q db.Querier, leaseID int64, served *ServedConfig) error {
	if served == nil {
		return nil
	}
	var variantID *uuid.UUID
	if served.Variant != nil {
		variantID = &served.Variant.VersionID
	}
	if _, err := q.Exec(ctx, `
		INSERT INTO lease_configs (lease_id, revision, settings, variant_id, fleet_instructions_version_id,
		                           harness, harness_version, model, bundle_hash)
		VALUES ($1, $2, $3, $4, $5, NULLIF($6, ''), NULLIF($7, ''), NULLIF($8, ''), NULLIF($9, ''))`,
		leaseID, served.Revision, served.Settings, variantID, served.FleetInstructionsVersionID,
		served.Run.Harness, served.Run.HarnessVersion, served.Run.Model, served.Run.BundleHash); err != nil {
		return fmt.Errorf("record lease configuration: %w", err)
	}
	return nil
}
