// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// enrich is the one tool on this surface that reaches the open internet, so
// what it accepts before it fetches is the part worth pinning: a depth it does
// not serve, and a target that is not an absolute http(s) URL. netguard refuses
// the private address ranges underneath; this refuses the arguments that never
// should have travelled at all.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// stubOrgProvider answers one organization record, so the staging path can be
// driven without a database. It embeds the package's probe provider so only the
// method StageInfo actually reaches — Read — is spelled here; any other call is
// the probe's loud failure rather than a silent zero value.
type stubOrgProvider struct {
	seamProbeProvider
	rec datasource.Record
}

func (p stubOrgProvider) Read(context.Context, datasource.EntityRef) (datasource.Record, error) {
	return p.rec, nil
}

func orgRecord(id ids.UUID, authoritative bool) datasource.Record {
	return datasource.Record{
		Ref:       datasource.EntityRef{Type: datasource.EntityOrganization, ID: id},
		Fields:    json.RawMessage(`{"name":"Acme"}`),
		Version:   4,
		Freshness: datasource.FreshnessInfo{Authoritative: authoritative},
	}
}

// What a human is asked to approve must say what will be read and from where —
// an approval whose summary omits the target is a decision made blind.
func TestEnrichStagesTheReadItIsAboutToPerform(t *testing.T) {
	id := ids.NewV7()
	tool := enrichCompany{p: stubOrgProvider{rec: orgRecord(id, true)}}

	info, err := tool.StageInfo(context.Background(),
		json.RawMessage(`{"organization_id":"`+id.String()+`","url":"https://acme.test/about","depth":"site"}`))
	if err != nil {
		t.Fatal(err)
	}
	if info.TargetType != string(datasource.EntityOrganization) || info.TargetID != id {
		t.Fatalf("staged against %s/%s, want the organization", info.TargetType, info.TargetID)
	}
	if info.TargetVersion == nil || *info.TargetVersion != 4 {
		t.Fatalf("target version = %v, want the version the read returned", info.TargetVersion)
	}
	if !strings.Contains(info.Summary, "https://acme.test/about") || !strings.Contains(info.Summary, "site") {
		t.Fatalf("summary = %q, want the URL and depth the call will actually use", info.Summary)
	}

	info, err = tool.StageInfo(context.Background(), json.RawMessage(`{"organization_id":"`+id.String()+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(info.Summary, "its own domain") {
		t.Fatalf("summary = %q, want the default target named", info.Summary)
	}
}

// A mirror-held organization cannot be released, so staging one mints an
// approval no human could ever act on.
func TestEnrichRefusesToStageAMirrorHeldOrganization(t *testing.T) {
	id := ids.NewV7()
	tool := enrichCompany{p: stubOrgProvider{rec: orgRecord(id, false)}}

	_, err := tool.StageInfo(context.Background(), json.RawMessage(`{"organization_id":"`+id.String()+`"}`))
	if !errors.Is(err, apperrors.ErrUnsupportedBySoR) {
		t.Fatalf("err = %v, want ErrUnsupportedBySoR", err)
	}
}

type recordingEnricher struct {
	url, depth string
}

func (e *recordingEnricher) EnrichCompany(_ context.Context, _ ids.UUID, url, depth string) (json.RawMessage, error) {
	e.url, e.depth = url, depth
	return json.RawMessage(`{}`), nil
}

func TestEnrichHandlePassesTheAdmittedArgumentsThrough(t *testing.T) {
	seam := &recordingEnricher{}
	tool := enrichCompany{enricher: seam}

	if _, err := tool.Handle(context.Background(),
		json.RawMessage(`{"organization_id":"`+ids.NewV7().String()+`","url":"https://acme.test"}`)); err != nil {
		t.Fatal(err)
	}
	if seam.url != "https://acme.test" {
		t.Fatalf("seam saw url %q", seam.url)
	}
	if seam.depth != enrichDepthPage {
		t.Fatalf("seam saw depth %q, want the default %q applied before the fetch", seam.depth, enrichDepthPage)
	}
}

func TestReadEnrichArgsDefaultsTheDepthAndRefusesAnUnservedOne(t *testing.T) {
	id := ids.NewV7().String()

	args, err := readEnrichArgs(json.RawMessage(`{"organization_id":"` + id + `"}`))
	if err != nil {
		t.Fatal(err)
	}
	if args.Depth != enrichDepthPage {
		t.Fatalf("depth defaulted to %q, want %q — the cheaper read is the one an omission gets",
			args.Depth, enrichDepthPage)
	}

	if _, err := readEnrichArgs(json.RawMessage(`{"organization_id":"` + id + `","depth":"crawl"}`)); err == nil {
		t.Fatal("depth \"crawl\" was accepted; the tool serves two depths")
	}
}

func TestReadEnrichArgsRefusesATargetThatIsNotAnAbsoluteHTTPURL(t *testing.T) {
	id := ids.NewV7().String()
	for _, target := range []string{
		"example.com",         // scheme-less: not a URL the fetcher can resolve
		"file:///etc/passwd",  // a scheme that is not the web
		"https://",            // no host
		"javascript:alert(1)", // not a fetch at all
	} {
		t.Run(target, func(t *testing.T) {
			_, err := readEnrichArgs(json.RawMessage(`{"organization_id":"` + id + `","url":"` + target + `"}`))
			if err == nil {
				t.Fatalf("%q was accepted as a fetch target", target)
			}
			if !strings.Contains(err.Error(), "absolute http(s) URL") {
				t.Fatalf("err = %v, want the requirement named so a caller can fix it", err)
			}
		})
	}

	if _, err := readEnrichArgs(json.RawMessage(`{"organization_id":"` + id + `","url":"https://example.com/about"}`)); err != nil {
		t.Fatalf("an absolute https URL must be accepted: %v", err)
	}
}

// The tool is 🟡, so the only way to complete one is to re-present it with the
// approval — a surface that does not advertise the argument cannot be driven
// by a client that validates against it.
func TestEnrichAdvertisesTheArgumentItsOwnRedemptionNeeds(t *testing.T) {
	spec := enrichCompany{}.Spec()
	if !strings.Contains(string(spec.InputSchema), `"approval_id"`) {
		t.Errorf("enrich is confirm-first but its input schema omits approval_id, and it forbids "+
			"additional properties:\n%s", spec.InputSchema)
	}
}
