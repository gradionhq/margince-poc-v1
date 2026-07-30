// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package people

// The logo write (A55): a resolved mark lands with its provenance and shows up
// as a URL on the record, a human's own logo is never replaced by one a machine
// found, and reading a logo's location is a read of the record — an
// out-of-scope organization is existence-hidden like every other read.

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func seedLogoOrg(ctx context.Context, t *testing.T, e *dedupeEnv, name, domain string) ids.OrganizationID {
	t.Helper()
	org, err := e.store.CreateOrganization(ctx, CreateOrganizationInput{
		DisplayName: name, Source: "manual",
		Domains: []OrgDomainInput{{Domain: domain, IsPrimary: true}},
	})
	if err != nil {
		t.Fatalf("seed org %s: %v", name, err)
	}
	return ids.From[ids.OrganizationKind](ids.UUID(org.Id))
}

func TestSetOrganizationLogoRecordsTheMarkItsProvenanceAndItsURL(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()
	orgID := seedLogoOrg(ctx, t, e, "Voltaq Systems GmbH", "voltaq.test")

	before, err := e.store.GetOrganization(ctx, orgID, storekit.LiveOnly)
	if err != nil {
		t.Fatalf("read before: %v", err)
	}
	if before.LogoUrl != nil {
		t.Fatalf("a fresh organization has no logo, got %q", *before.LogoUrl)
	}
	if _, err := e.store.OrganizationLogoKey(ctx, orgID); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("an organization with no logo must answer not-found, got %v", err)
	}

	key := e.ws.String() + "/organization_logo/" + orgID.String()
	written, err := e.store.SetOrganizationLogo(ctx, orgID, key, "https://voltaq.test/touch.png")
	if err != nil {
		t.Fatalf("SetOrganizationLogo: %v", err)
	}
	if !written {
		t.Fatal("the write reported no change on an organization with no logo")
	}

	after, err := e.store.GetOrganization(ctx, orgID, storekit.LiveOnly)
	if err != nil {
		t.Fatalf("read after: %v", err)
	}
	wantURL := "/v1/organizations/" + orgID.String() + "/logo"
	if after.LogoUrl == nil || *after.LogoUrl != wantURL {
		t.Fatalf("logo_url = %v, want %q", after.LogoUrl, wantURL)
	}
	// The storage key is where the bytes are, and it must never reach the wire.
	if after.LogoUrl != nil && *after.LogoUrl == key {
		t.Fatal("the storage key leaked onto the wire")
	}
	gotKey, err := e.store.OrganizationLogoKey(ctx, orgID)
	if err != nil {
		t.Fatalf("OrganizationLogoKey: %v", err)
	}
	if gotKey != key {
		t.Fatalf("stored key = %q, want %q", gotKey, key)
	}

	// The provenance layer must name the field, the source and where it came
	// from, the same way every other enriched field is traceable.
	var source, capturedBy string
	var evidence *string
	err = e.store.tx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT source, captured_by, evidence_ref FROM field_provenance
			WHERE object_type = 'organization' AND object_id = $1 AND field_name = 'logo'`,
			orgID).Scan(&source, &capturedBy, &evidence)
	})
	if err != nil {
		t.Fatalf("read logo provenance: %v", err)
	}
	if source != companySourceSiteRead {
		t.Fatalf("provenance source = %q, want %q", source, companySourceSiteRead)
	}
	if evidence == nil || *evidence != "https://voltaq.test/touch.png" {
		t.Fatalf("provenance evidence_ref = %v, want the asset URL", evidence)
	}
	if capturedBy == "" {
		t.Fatal("the write recorded no author")
	}
}

func TestSetOrganizationLogoNeverReplacesTheOneAPersonSet(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()
	orgID := seedLogoOrg(ctx, t, e, "Nordwind Energie AG", "nordwind.test")

	humanKey := e.ws.String() + "/organization_logo/human"
	err := e.store.tx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx,
			`UPDATE organization SET logo_object_key = $2, logo_origin = 'upload' WHERE id = $1`,
			orgID, humanKey); err != nil {
			return err
		}
		return storekit.StampFields(ctx, tx, "organization", orgID.UUID, "human", "human:"+e.rep.String(),
			[]storekit.FieldStamp{{Field: "logo"}})
	})
	if err != nil {
		t.Fatalf("seed a human-set logo: %v", err)
	}

	// The caller must be able to learn this BEFORE it writes any bytes: the
	// object key is derived from the organization id, so a resolve that stored
	// first and asked afterwards would already have overwritten the person's
	// own image whatever the row guard then decided.
	held, err := e.store.LogoHeldByHuman(ctx, orgID)
	if err != nil {
		t.Fatalf("LogoHeldByHuman: %v", err)
	}
	if !held {
		t.Fatal("a human-set logo must be reported as held before any byte is written")
	}

	written, err := e.store.SetOrganizationLogo(ctx, orgID,
		e.ws.String()+"/organization_logo/"+orgID.String(), "https://nordwind.test/favicon.ico")
	if err != nil {
		t.Fatalf("SetOrganizationLogo: %v", err)
	}
	if written {
		t.Fatal("a resolved logo replaced a human's own without a confirm")
	}
	gotKey, err := e.store.OrganizationLogoKey(ctx, orgID)
	if err != nil {
		t.Fatalf("OrganizationLogoKey: %v", err)
	}
	if gotKey != humanKey {
		t.Fatalf("stored key = %q, want the human's %q", gotKey, humanKey)
	}
}

func TestSetOrganizationLogoRefusesAHalfResolvedWrite(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()
	orgID := seedLogoOrg(ctx, t, e, "Halbmond GmbH", "halbmond.test")

	if _, err := e.store.SetOrganizationLogo(ctx, orgID, "", "https://halbmond.test/f.png"); err == nil {
		t.Fatal("a logo with no storage key must be refused")
	}
	if _, err := e.store.SetOrganizationLogo(ctx, orgID, "k", ""); err == nil {
		t.Fatal("a logo with no source URL must be refused — its provenance would be blank")
	}
}

func TestOrganizationLogoIsRowScopedLikeEveryOtherRead(t *testing.T) {
	e := setupDedupe(t)
	owner := e.as()
	orgID := seedLogoOrg(owner, t, e, "Fremdfirma GmbH", "fremd.test")
	key := e.ws.String() + "/organization_logo/" + orgID.String()
	if _, err := e.store.SetOrganizationLogo(owner, orgID, key, "https://fremd.test/touch.png"); err != nil {
		t.Fatalf("seed the logo: %v", err)
	}
	// An ownerless organization is workspace-shared, so bind it to this rep:
	// own-only row scope can only hide a row that somebody owns.
	if err := e.store.tx(owner, func(tx pgx.Tx) error {
		_, err := tx.Exec(owner, `UPDATE organization SET owner_id = $2 WHERE id = $1`, orgID, e.rep)
		return err
	}); err != nil {
		t.Fatalf("bind the organization's owner: %v", err)
	}

	// A caller scoped to their own records only: the organization is another
	// rep's, so both the location read and the write answer not-found rather
	// than confirming it exists.
	stranger := e.asOwnScoped(ids.NewV7())
	if _, err := e.store.OrganizationLogoKey(stranger, orgID); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("an out-of-scope logo read must be existence-hidden, got %v", err)
	}
	if _, err := e.store.LogoHeldByHuman(stranger, orgID); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("an out-of-scope provenance read must be existence-hidden, got %v", err)
	}
	if _, err := e.store.SetOrganizationLogo(stranger, orgID, key, "https://fremd.test/other.png"); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("an out-of-scope logo write must be existence-hidden, got %v", err)
	}
}
