// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The guard half of the census configuration: everyDeclaredDependencySupplied
// refuses to let the census run over a JobRunnerConfig that has fallen behind
// the declaration, and censusSeam is the fake every model/embed lane resolves
// to. Both are exercised here on their failure paths — the success path is
// already pinned by TestJobCensusMatchesTheContract building the census for
// real, which is why this file targets only what that happy path never
// reaches: a config missing a registration or cadence dependency, and a seam
// actually being called.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

func TestEveryDeclaredDependencySuppliedRefusesAConfigMissingARegistrationOrCadenceDependency(t *testing.T) {
	// The zero JobRunnerConfig supplies nothing: every kind gated on a
	// Registration.When path and every kind whose Cadence reads an operator
	// field is, from this config's point of view, unmet. That drives both the
	// registration-gap branch and the cadence-gap branch in the same call,
	// which is the census's own definition of "fallen behind the declaration".
	err := everyDeclaredDependencySupplied(JobRunnerConfig{})
	if err == nil {
		t.Fatal("everyDeclaredDependencySupplied(JobRunnerConfig{}) = nil, want an error — the zero config supplies no registry, vault, brain, or interval, so every gated kind is unmet")
	}
	// gmail_watch_renew is gated on GmailRegistry AND GmailWatch.Topic
	// (api/jobs.yaml's Registration.When), so its absence is the
	// registration-gap branch.
	if !strings.Contains(err.Error(), "GmailRegistry") {
		t.Errorf("error does not name GmailRegistry as an unmet registration dependency: %v", err)
	}
	// close_date_hygiene reads its cadence from CloseDateInterval
	// (api/jobs.yaml's {operator: CloseDateInterval}), so a zero interval is
	// the cadence-gap branch.
	if !strings.Contains(err.Error(), "CloseDateInterval") {
		t.Errorf("error does not name CloseDateInterval as an unmet cadence dependency: %v", err)
	}
}

func TestEveryDeclaredDependencySuppliedAcceptsTheCensusConfig(t *testing.T) {
	// The maximal config the census actually builds through must clear its
	// own guard — NewJobCensus already relies on this, but pinning it directly
	// here is what makes a future kind gated on a field censusJobConfig
	// forgot fail loudly at this test rather than surfacing only as a missing
	// worker three checks downstream.
	if err := everyDeclaredDependencySupplied(censusJobConfig()); err != nil {
		t.Fatalf("everyDeclaredDependencySupplied(censusJobConfig()) = %v, want nil — censusJobConfig is supposed to supply every declared dependency", err)
	}
}

func TestCensusSeamRefusesEveryCallItStandsIn(t *testing.T) {
	seam := censusSeam{}

	_, err := seam.Complete(context.Background(), model.Request{})
	if !errors.Is(err, errCensusSeam) {
		t.Errorf("censusSeam.Complete error = %v, want errCensusSeam — a census seam that did any work could return a plausible zero value instead of refusing", err)
	}

	_, err = seam.Embed(context.Background(), model.EmbedRequest{})
	if !errors.Is(err, errCensusSeam) {
		t.Errorf("censusSeam.Embed error = %v, want errCensusSeam — same reasoning as Complete: the seam counts wiring and must refuse to work", err)
	}

	if id, bound := seam.EmbedIdentity(); id == "" || bound <= 0 {
		t.Errorf("censusSeam.EmbedIdentity() = (%q, %d), want a non-empty id and a positive dimension — an unbound identity would leave the drift sweep with no binding marker to compare a row against", id, bound)
	}
}
