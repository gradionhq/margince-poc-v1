// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package orgdossier

// The model lane held to the rule the whole surface rests on: a band is only
// as good as the records behind it, and the completeness gate outranks the
// model's confidence.

import (
	"context"
	"errors"
	"testing"
	"time"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

type scriptedLane struct{ reply string }

func (l scriptedLane) Complete(context.Context, model.Request) (model.Response, error) {
	return model.Response{Text: l.reply}, nil
}

type failingLane struct{}

func (failingLane) Complete(context.Context, model.Request) (model.Response, error) {
	return model.Response{}, errors.New("budget exhausted")
}

type proseLane struct{}

func (proseLane) Complete(context.Context, model.Request) (model.Response, error) {
	return model.Response{Text: "I'm afraid I can't do that."}, nil
}

func at() time.Time { return assessedAt }

// sevenOfSeven is a company complete enough for a band to be served, so a test
// about the MODEL is not silently answering a question about the floor.
func sevenOfSeven() Input {
	in := fourOfSeven()
	fresh := assessedAt.Add(-24 * time.Hour)
	in.ProfileFields = append(in.ProfileFields,
		machineField("buying_center", "Head of Operations", fresh),
		machineField("buying_intents", "Cutting peak demand charges", fresh))
	in.Facts = append(in.Facts, machineFact("technology", "SAP S/4HANA", fresh))
	return in
}

// citing renders a reply citing one of the input's own records.
func citing(band string, in Input) string {
	id := in.ProfileFields[0].Id.String()
	return `{"band":"` + band + `","positive_factors":[
		{"text":"They sell load-shifting software.","nature":"fact",
		 "evidence":[{"entity_type":"profile_field","entity_id":"` + id + `"}]}]}`
}

// Every model-side failure lands the reader on the floor rather than on an
// error page, and `generated_by` says which writer they are reading.
func TestEveryModelFailureDegradesToTheFloorAndSaysSo(t *testing.T) {
	in := sevenOfSeven()
	for name, lane := range map[string]Completer{
		"no lane configured":     nil,
		"lane over budget":       failingLane{},
		"lane answering prose":   proseLane{},
		"lane citing nothing":    scriptedLane{reply: `{"band":"strong","positive_factors":[]}`},
		"lane naming no band":    scriptedLane{reply: `{"band":"excellent","positive_factors":[]}`},
		"lane abstaining itself": scriptedLane{reply: citing("unknown", in)},
		"lane citing a stranger": scriptedLane{reply: citing("strong", fourOfSeven())},
	} {
		t.Run(name, func(t *testing.T) {
			got, by := WriteGrowthFit(context.Background(), lane, in, true, at)

			if by != crmcontracts.Deterministic {
				t.Errorf("generated_by = %q, want deterministic — the model did not produce this", by)
			}
			if got.Band != crmcontracts.GrowthFitBandUnknown {
				t.Errorf("band = %q, want unknown — the floor abstains", got.Band)
			}
			if got.NextStep == "" {
				t.Error("the floor produced no next step, leaving the reader nothing to act on")
			}
		})
	}
}

// The happy path: a grounded reply is served as the model's, with its claims.
func TestAGroundedReplyIsServedAsTheModelsWithItsClaims(t *testing.T) {
	in := sevenOfSeven()

	got, by := WriteGrowthFit(context.Background(), scriptedLane{reply: citing("strong", in)}, in, true, at)

	if by != crmcontracts.Model {
		t.Fatalf("generated_by = %q, want model", by)
	}
	if got.Band != crmcontracts.GrowthFitBandStrong {
		t.Errorf("band = %q, want strong — seven of seven clears the floor", got.Band)
	}
	if len(got.Claims.PositiveFactors) != 1 {
		t.Fatalf("positive factors = %d, want the one grounded claim", len(got.Claims.PositiveFactors))
	}
}

// The model proposes; the counting decides. A confident band over four facts
// is exactly the failure DOSS-FORM-2 exists to catch, so the floor overrules
// it — and takes the reasoning with it, because "not enough evidence" beside
// four confident reasons reads as a band the surface is merely too shy to say.
func TestTheCompletenessFloorOverrulesAConfidentModelAndWithholdsItsReasons(t *testing.T) {
	thin := Input{
		OrganizationID: fourOfSeven().OrganizationID,
		ProfileFields: []crmcontracts.CompanyProfileField{
			machineField("offer_summary", "Load-shifting software", assessedAt.Add(-24*time.Hour)),
		},
	}

	got, by := WriteGrowthFit(context.Background(), scriptedLane{reply: citing("strong", thin)}, thin, true, at)

	if got.Band != crmcontracts.GrowthFitBandUnknown {
		t.Errorf("band = %q, want unknown — one of seven cannot support a judgment", got.Band)
	}
	if !got.Claims.empty() {
		t.Error("the abstention carried the model's reasons, which reads as a band withheld out of shyness")
	}
	if by != crmcontracts.Model {
		t.Errorf("generated_by = %q, want model — the model did answer; the counting overruled its band", by)
	}
	if got.NextStep == "" {
		t.Error("the abstention names nothing to gather")
	}
}

// The DOSS-AC-13 cap applies to the MODEL's band too. Without this, wiring a
// lane would quietly lift a ceiling the deterministic path respects.
func TestTheCapAppliesToTheModelsBandAsWell(t *testing.T) {
	in := sevenOfSeven()

	got, _ := WriteGrowthFit(context.Background(), scriptedLane{reply: citing("strong", in)}, in, false, at)

	if got.Band != crmcontracts.GrowthFitBandModerate {
		t.Errorf("band = %q, want moderate — our own offering is unconfirmed", got.Band)
	}
	if got.CappedReason == "" {
		t.Error("the model's band was lowered and the surface does not say why")
	}
}

// DOSS-AC-6: our own offering reaches the model as context and must never come
// back as evidence about them. The known set holds target-side records only, so
// a claim citing anything else has nowhere to land.
func TestAClaimCitingSomethingOutsideTheCompanyIsDropped(t *testing.T) {
	in := sevenOfSeven()
	// A well-formed uuid that names no record of this company — which is what a
	// citation of our own profile would look like on the wire.
	const stranger = "00000000-0000-7000-8000-000000000000"
	reply := `{"band":"strong","positive_factors":[
		{"text":"They match who we sell to.","nature":"assessment",
		 "evidence":[{"entity_type":"profile_field","entity_id":"` + stranger + `"}]}]}`

	_, _, err := ParseGrowthFit(reply, in)

	if err == nil {
		t.Fatal("a reply whose only claim cited a record outside the company was accepted")
	}
}
