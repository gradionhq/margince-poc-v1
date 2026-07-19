// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package identity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

const (
	// OnboardingPathCreator is the first-installation administrator route.
	OnboardingPathCreator = "creator"
	// OnboardingPathMember is the later invited-human route.
	OnboardingPathMember = "member"

	// OnboardingStepRead selects website or manual company entry.
	OnboardingStepRead = "read"
	// OnboardingStepConfirm reviews the company draft.
	OnboardingStepConfirm = "confirm"
	// OnboardingStepVoice captures optional writing examples.
	OnboardingStepVoice = "voice"
	// OnboardingStepResults reveals confirmed understanding.
	OnboardingStepResults = "results"
	// OnboardingStepConnect offers the optional inbox connection.
	OnboardingStepConnect = "connect"
	// OnboardingStepComplete is the terminal checkpoint.
	OnboardingStepComplete = "complete"

	// OnboardingSourceWebsite identifies a public-site-assisted draft.
	OnboardingSourceWebsite = "website"
	// OnboardingSourceManual identifies a zero-egress human-entered draft.
	OnboardingSourceManual = "manual"

	maxOnboardingDraftBytes = 64 * 1024
	maxSelectedFacts        = 100
	maxSelectedFactKeyBytes = 256
	httpScheme              = "http"
	httpsScheme             = "https"
	onboardingAuditUserID   = "user_id"
)

var onboardingSteps = map[string]struct{}{
	OnboardingStepRead: {}, OnboardingStepConfirm: {}, OnboardingStepVoice: {},
	OnboardingStepResults: {}, OnboardingStepConnect: {}, OnboardingStepComplete: {},
}

// OnboardingCompanyDraft is intentionally partial. Confirmed values are owned
// by the company profile; this copy exists only so a half-finished form can be
// resumed before confirmation.
type OnboardingCompanyDraft struct {
	DisplayName       *string `json:"display_name,omitempty"`
	OfferSummary      *string `json:"offer_summary,omitempty"`
	ICP               *string `json:"icp,omitempty"`
	ValueProposition  *string `json:"value_proposition,omitempty"`
	USP               *string `json:"usp,omitempty"`
	CustomerPains     *string `json:"customer_pains,omitempty"`
	DesiredOutcomes   *string `json:"desired_outcomes,omitempty"`
	BuyingCenter      *string `json:"buying_center,omitempty"`
	BuyingIntents     *string `json:"buying_intents,omitempty"`
	CommonObjections  *string `json:"common_objections,omitempty"`
	SalesMotion       *string `json:"sales_motion,omitempty"`
	LegalName         *string `json:"legal_name,omitempty"`
	RegisteredAddress *string `json:"registered_address,omitempty"`
	RegisterVAT       *string `json:"register_vat,omitempty"`
	Industry          *string `json:"industry,omitempty"`
	History           *string `json:"history,omitempty"`
}

// OnboardingState is operational per-human progress, not confirmed business truth.
type OnboardingState struct {
	ID               ids.UUID
	Path             string
	Step             string
	SourceMode       *string
	WebsiteURL       *string
	SiteReadID       *ids.UUID
	CompanyDraft     OnboardingCompanyDraft
	SelectedFactKeys []string
	VoiceSkipped     bool
	ConnectSkipped   bool
	Version          int64
	CompletedAt      *time.Time
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// PutOnboardingStateInput carries a versioned wizard checkpoint plus the
// compose-derived company state used to enforce creator/member progression.
type PutOnboardingStateInput struct {
	ExpectedVersion  int64
	Step             string
	SourceMode       *string
	WebsiteURL       *string
	SiteReadID       *ids.UUID
	CompanyDraft     OnboardingCompanyDraft
	SelectedFactKeys []string
	VoiceSkipped     bool
	ConnectSkipped   bool
	CompanyExists    bool
	CompanyComplete  bool
}

// InvalidOnboardingStateError identifies one client-correctable checkpoint field.
type InvalidOnboardingStateError struct {
	Field  string
	Reason string
}

func (e *InvalidOnboardingStateError) Error() string {
	return fmt.Sprintf("invalid onboarding %s: %s", e.Field, e.Reason)
}

func invalidOnboarding(field, reason string) error {
	return &InvalidOnboardingStateError{Field: field, Reason: reason}
}

// OnboardingStore owns per-human resumable wizard checkpoints.
type OnboardingStore struct{ pool *pgxpool.Pool }

// NewOnboardingStore builds the workspace-scoped checkpoint store.
func NewOnboardingStore(pool *pgxpool.Pool) *OnboardingStore {
	return &OnboardingStore{pool: pool}
}

func onboardingActor(ctx context.Context, mutate bool) (principal.Principal, error) {
	actor, ok := principal.Actor(ctx)
	if !ok || actor.Type != principal.PrincipalHuman || actor.UserID.IsZero() {
		return principal.Principal{}, apperrors.ErrPermissionDenied
	}
	if mutate && !actor.SeatType.CanMutate() {
		return principal.Principal{}, apperrors.ErrPermissionDenied
	}
	return actor, nil
}

// Get returns the authenticated human's checkpoint.
func (s *OnboardingStore) Get(ctx context.Context) (OnboardingState, error) {
	actor, err := onboardingActor(ctx, false)
	if err != nil {
		return OnboardingState{}, err
	}
	var state OnboardingState
	err = database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		var draft []byte
		row := tx.QueryRow(ctx, `SELECT id, path, step, source_mode, website_url, site_read_id,
			company_draft, selected_fact_keys, voice_skipped, connect_skipped, version,
			completed_at, created_at, updated_at
			FROM onboarding_wizard_state WHERE user_id = $1`, actor.UserID)
		if err := scanOnboardingState(row, &state, &draft); err != nil {
			return err
		}
		return json.Unmarshal(draft, &state.CompanyDraft)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return OnboardingState{}, apperrors.ErrNotFound
	}
	return state, err
}

// Put creates or version-advances the authenticated human's checkpoint.
func (s *OnboardingStore) Put(ctx context.Context, in PutOnboardingStateInput) (OnboardingState, error) {
	actor, err := onboardingActor(ctx, true)
	if err != nil {
		return OnboardingState{}, err
	}
	draft, err := validateOnboardingInput(&in)
	if err != nil {
		return OnboardingState{}, err
	}

	var state OnboardingState
	err = database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		if in.ExpectedVersion == 0 {
			path := OnboardingPathCreator
			if in.CompanyExists {
				path = OnboardingPathMember
			}
			if err := validateOnboardingAdvance(path, in.Step, in.CompanyComplete); err != nil {
				return err
			}
			return s.createOnboardingState(ctx, tx, actor, path, in, draft, &state)
		}
		return s.updateOnboardingState(ctx, tx, actor, in, draft, &state)
	})
	return state, err
}

func validateOnboardingInput(in *PutOnboardingStateInput) ([]byte, error) {
	if in.ExpectedVersion < 0 {
		return nil, invalidOnboarding("expected_version", "must not be negative")
	}
	if _, ok := onboardingSteps[in.Step]; !ok {
		return nil, invalidOnboarding("step", "is not a known wizard step")
	}
	if err := validateOnboardingSource(in); err != nil {
		return nil, err
	}
	if err := normalizeSelectedFactKeys(in); err != nil {
		return nil, err
	}
	draft, err := json.Marshal(in.CompanyDraft)
	if err != nil || len(draft) > maxOnboardingDraftBytes {
		return nil, invalidOnboarding("company_draft", "is too large")
	}
	return draft, nil
}

func validateOnboardingSource(in *PutOnboardingStateInput) error {
	if in.SourceMode != nil {
		mode := strings.TrimSpace(*in.SourceMode)
		if mode != OnboardingSourceWebsite && mode != OnboardingSourceManual {
			return invalidOnboarding("source_mode", "must be website or manual")
		}
		in.SourceMode = &mode
	}
	websiteMode := in.SourceMode != nil && *in.SourceMode == OnboardingSourceWebsite
	if websiteMode && in.WebsiteURL != nil && !validOnboardingURL(*in.WebsiteURL) {
		return invalidOnboarding("website_url", "must be an HTTP or HTTPS URL")
	}
	if !websiteMode && in.SiteReadID != nil {
		return invalidOnboarding("site_read_id", "requires website source mode")
	}
	return nil
}

func normalizeSelectedFactKeys(in *PutOnboardingStateInput) error {
	if len(in.SelectedFactKeys) > maxSelectedFacts {
		return invalidOnboarding("selected_fact_keys", "contains too many facts")
	}
	seen := make(map[string]struct{}, len(in.SelectedFactKeys))
	for i, key := range in.SelectedFactKeys {
		key = strings.TrimSpace(key)
		if key == "" || len(key) > maxSelectedFactKeyBytes {
			return invalidOnboarding("selected_fact_keys", "contains an empty or oversized key")
		}
		if _, exists := seen[key]; exists {
			return invalidOnboarding("selected_fact_keys", "contains a duplicate key")
		}
		seen[key] = struct{}{}
		in.SelectedFactKeys[i] = key
	}
	return nil
}

func validOnboardingURL(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	return err == nil && u.Hostname() != "" && (u.Scheme == httpScheme || u.Scheme == httpsScheme)
}

func validateOnboardingAdvance(path, step string, companyComplete bool) error {
	if path == OnboardingPathMember && (step == OnboardingStepRead || step == OnboardingStepConfirm) {
		return invalidOnboarding("step", "members begin at Voice")
	}
	if path == OnboardingPathCreator && !companyComplete &&
		step != OnboardingStepRead && step != OnboardingStepConfirm {
		return apperrors.ErrConflict
	}
	return nil
}

func (s *OnboardingStore) createOnboardingState(
	ctx context.Context,
	tx pgx.Tx,
	actor principal.Principal,
	path string,
	in PutOnboardingStateInput,
	draft []byte,
	out *OnboardingState,
) error {
	completed := in.Step == OnboardingStepComplete
	var storedDraft []byte
	row := tx.QueryRow(ctx, `INSERT INTO onboarding_wizard_state
		(workspace_id, user_id, path, step, source_mode, website_url, site_read_id,
		 company_draft, selected_fact_keys, voice_skipped, connect_skipped, completed_at)
		VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid, $1, $2, $3, $4,
		        $5, $6, $7, $8, $9, $10, CASE WHEN $11 THEN now() ELSE NULL END)
		RETURNING id, path, step, source_mode, website_url, site_read_id, company_draft,
		 selected_fact_keys, voice_skipped, connect_skipped, version, completed_at, created_at, updated_at`,
		actor.UserID, path, in.Step, in.SourceMode, in.WebsiteURL, in.SiteReadID,
		draft, in.SelectedFactKeys, in.VoiceSkipped, in.ConnectSkipped, completed)
	if err := scanOnboardingState(row, out, &storedDraft); err != nil {
		if storekit.IsUniqueViolation(err) {
			return apperrors.ErrVersionSkew
		}
		return err
	}
	if err := json.Unmarshal(storedDraft, &out.CompanyDraft); err != nil {
		return err
	}
	return auditOnboardingState(ctx, tx, actor.UserID, nil, *out)
}

func (s *OnboardingStore) updateOnboardingState(
	ctx context.Context,
	tx pgx.Tx,
	actor principal.Principal,
	in PutOnboardingStateInput,
	draft []byte,
	out *OnboardingState,
) error {
	var current OnboardingState
	var currentDraft []byte
	row := tx.QueryRow(ctx, `SELECT id, path, step, source_mode, website_url, site_read_id,
		company_draft, selected_fact_keys, voice_skipped, connect_skipped, version,
		completed_at, created_at, updated_at
		FROM onboarding_wizard_state WHERE user_id = $1 FOR UPDATE`, actor.UserID)
	if err := scanOnboardingState(row, &current, &currentDraft); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return apperrors.ErrVersionSkew
		}
		return err
	}
	if current.Version != in.ExpectedVersion {
		return apperrors.ErrVersionSkew
	}
	if current.Step == OnboardingStepComplete {
		return apperrors.ErrConflict
	}
	if err := validateOnboardingAdvance(current.Path, in.Step, in.CompanyComplete); err != nil {
		return err
	}
	completed := in.Step == OnboardingStepComplete
	var storedDraft []byte
	row = tx.QueryRow(ctx, `UPDATE onboarding_wizard_state SET
		step = $2, source_mode = $3, website_url = $4, site_read_id = $5,
		company_draft = $6, selected_fact_keys = $7, voice_skipped = $8,
		connect_skipped = $9, version = version + 1,
		completed_at = CASE WHEN $10 THEN now() ELSE NULL END, updated_at = now()
		WHERE user_id = $1 AND version = $11
		RETURNING id, path, step, source_mode, website_url, site_read_id, company_draft,
		 selected_fact_keys, voice_skipped, connect_skipped, version, completed_at, created_at, updated_at`,
		actor.UserID, in.Step, in.SourceMode, in.WebsiteURL, in.SiteReadID, draft,
		in.SelectedFactKeys, in.VoiceSkipped, in.ConnectSkipped, completed, in.ExpectedVersion)
	if err := scanOnboardingState(row, out, &storedDraft); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return apperrors.ErrVersionSkew
		}
		return err
	}
	if err := json.Unmarshal(storedDraft, &out.CompanyDraft); err != nil {
		return err
	}
	return auditOnboardingState(ctx, tx, actor.UserID, &current, *out)
}

type rowScanner interface{ Scan(...any) error }

func scanOnboardingState(row rowScanner, state *OnboardingState, draft *[]byte) error {
	return row.Scan(&state.ID, &state.Path, &state.Step, &state.SourceMode, &state.WebsiteURL,
		&state.SiteReadID, draft, &state.SelectedFactKeys, &state.VoiceSkipped,
		&state.ConnectSkipped, &state.Version, &state.CompletedAt, &state.CreatedAt, &state.UpdatedAt)
}

func auditOnboardingState(
	ctx context.Context,
	tx pgx.Tx,
	userID ids.UUID,
	before *OnboardingState,
	after OnboardingState,
) error {
	action := "create"
	var beforeImage map[string]any
	if before != nil {
		action = "update"
		beforeImage = onboardingAuditImage(*before)
	}
	afterImage := onboardingAuditImage(after)
	auditID, err := storekit.Audit(ctx, tx, action, "onboarding_wizard_state", after.ID,
		beforeImage, afterImage)
	if err != nil {
		return err
	}
	payload := map[string]any{
		onboardingAuditUserID: userID, "path": after.Path, "step": after.Step, "version": after.Version,
		"voice_skipped": after.VoiceSkipped, "connect_skipped": after.ConnectSkipped,
		"completed": after.CompletedAt != nil,
	}
	return storekit.Emit(ctx, tx, auditID, "onboarding.state_changed",
		"onboarding_wizard_state", after.ID, payload)
}

func onboardingAuditImage(state OnboardingState) map[string]any {
	return map[string]any{
		"path": state.Path, "step": state.Step, "version": state.Version,
		"voice_skipped": state.VoiceSkipped, "connect_skipped": state.ConnectSkipped,
		"completed": state.CompletedAt != nil,
	}
}
