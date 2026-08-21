// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The configuration the census measures THROUGH, and the guard that keeps it
// honest.
//
// Which job kinds a role wires is deployment-dependent — nine JobRunnerConfig
// conditions gate roughly fifteen kinds, and Embedder alone takes opposite
// postures on its two — so a census over a bare config would report a third of
// the contract as unwired and be right about none of it. This file supplies
// every one of those conditions, and refuses to let the census build when it
// has fallen behind a declaration that names a new one.

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/modules/integrations"
	"github.com/gradionhq/margince/backend/internal/modules/webhooks"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/geocode"
	"github.com/gradionhq/margince/backend/internal/platform/jobs"
	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// everyDeclaredDependencySupplied refuses a census configuration that has
// fallen behind the declaration. A kind gated on a JobRunnerConfig field this
// file forgot to fill would be absent from the wiring for a reason that has
// nothing to do with the composition, and the totality check would blame the
// wiring for it.
func everyDeclaredDependencySupplied(cfg JobRunnerConfig) error {
	supplied := configDependencies(cfg)
	intervals := operatorIntervals(cfg)
	unmet := map[string]struct{}{}
	for kind, spec := range jobs.Declared() {
		for _, path := range spec.Registration.When {
			if !supplied[path] {
				unmet[fmt.Sprintf("%s is gated on JobRunnerConfig.%s", kind, path)] = struct{}{}
			}
		}
		for _, path := range []string{spec.Cadence.OperatorField, spec.Cadence.ScheduleWhenPositive} {
			if path != "" && intervals[path] <= 0 {
				unmet[fmt.Sprintf("%s reads its cadence from JobRunnerConfig.%s", kind, path)] = struct{}{}
			}
		}
	}
	if len(unmet) == 0 {
		return nil
	}
	return fmt.Errorf("the census configuration does not supply every declared dependency, so it would measure less than it claims — fill these in censusJobConfig:\n  %s",
		strings.Join(slices.Sorted(maps.Keys(unmet)), "\n  "))
}

// censusJobConfig is the maximally-configured role: every credential
// custodian, connector registry, model lane and operator dial the declaration
// names, so that every kind the contract declares is one this assembly wires.
//
// It is not a deployment anyone runs. A real role supplies what it can reach,
// and which kinds that leaves wired is the whole subject of the registration
// postures; this one exists so the census reads the contract's full extent
// rather than one deployment's slice of it.
func censusJobConfig() JobRunnerConfig {
	seam := censusSeam{}
	return JobRunnerConfig{
		SendRegistry: &capture.Registry{},
		// Present so the scheduled-send alarm counts as wired. Firing one
		// stages a delivery, so the kind is gated on this machinery existing;
		// the census supplies every declared dependency so a kind can never be
		// declared and silently unregistered.
		SendDelivery:  censusDeliveryMachinery{},
		GmailRegistry: &capture.Registry{},
		// The watch pass is the one conjunction in the file: a registry alone
		// does not wire it, so the topic has to be here too.
		GmailWatch:             GmailWatchConfig{Topic: "projects/census/topics/census", Interval: censusInterval},
		ChannelVault:           keyvault.NewMemory(),
		OverlayVault:           keyvault.NewMemory(),
		ClassifyBrain:          seam,
		EnrichBrain:            seam,
		VerdictBrain:           seam,
		DeepReadBrain:          seam,
		TranscriptProposeBrain: seam,
		Geocoder:               censusGeocoder{},
		DocumentExtractBrain:   censusDocumentSeam{},
		VoiceBrain:             seam,
		Embedder:               seam,
		AgentScheduler:         AgentSchedulerConfig{Interval: censusInterval, Service: &RunnerService{}},
		WebhookRetry:           WebhookRetryConfig{Interval: censusInterval, Deliverer: func(*database.DB) *webhooks.Deliverer { return &webhooks.Deliverer{} }},
		ProviderRuns:           ProviderRunsConfig{Registry: censusProviderRegistry(), Vault: keyvault.NewMemory()},
		PrivacyRetention:       PrivacyRetentionConfig{Interval: censusInterval},
		CloseDateInterval:      censusInterval,
		ReconcileInterval:      censusInterval,
		TimeScanInterval:       censusInterval,
		OverlayInterval:        censusInterval,
	}
}

// censusDeliveryMachinery is present and inert: the census asks which kinds a
// fully-configured build registers, and never works a job, so a stager that
// stages nothing answers that question exactly.
type censusDeliveryMachinery struct{}

func (censusDeliveryMachinery) StageTx(context.Context, pgx.Tx, activities.DeliveryRequest) error {
	return nil
}

func (censusDeliveryMachinery) StageChannelTx(context.Context, pgx.Tx, activities.ChannelDeliveryRequest) error {
	return nil
}

// censusProviderRegistry is an empty adapter registry: present, so the
// provider-run kinds count as wired, and closed, so the census could never
// name a provider it would then be asked to call.
func censusProviderRegistry() *integrations.Registry {
	reg, err := integrations.NewRegistry()
	if err != nil {
		panic("compose: an empty provider registry cannot fail to construct: " + err.Error())
	}
	return reg
}

// censusInterval is a positive duration and nothing more: the kinds that
// declare schedule_when_positive stay wired on any positive dial, and the
// census never places a tick.
const censusInterval = time.Minute

// errCensusSeam is what every seam below answers with. A census wires these to
// be COUNTED, never called, and an honest refusal is what keeps a caller that
// mistook this assembly for a runnable one from getting a plausible zero back.
var errCensusSeam = errors.New("compose: this seam belongs to the job census, which counts wiring and works no jobs")

// censusSeam stands in for every model lane and embed lane the wiring consults.
// It answers the one question the assembly asks — is this supplied — and
// refuses the work behind it.
type censusSeam struct{}

func (censusSeam) Complete(context.Context, model.Request) (model.Response, error) {
	return model.Response{}, errCensusSeam
}

func (censusSeam) Embed(context.Context, model.EmbedRequest) (model.Embeddings, error) {
	return model.Embeddings{}, errCensusSeam
}

// censusDocumentSeam is censusSeam for the one lane that must also answer what
// it can carry. It declares nothing: the census asks only whether a lane is
// supplied, and a stand-in that claimed carriage would be answering a question
// about a binding that does not exist.
type censusDocumentSeam struct{ censusSeam }

func (censusDocumentSeam) AttachmentMIMEs() []string { return nil }

// EmbedIdentity answers a BOUND lane. The drift sweep registers nothing behind
// an unbound one — an empty identity is what --ai-fake leaves, and there is
// then no binding marker for the sweep to compare a row against — so a seam
// that answered "" would leave two declared kinds looking unwired for a reason
// no configuration field expresses.
func (censusSeam) EmbedIdentity() (string, int) { return "census/embedder@1", 1 }

// censusGeocoder stands in for a geocoding provider so the kind counts as
// wired, and REFUSES every lookup — the census measures what is registered,
// never what a provider would answer, and a stand-in that returned a point
// would let a test pass on a coordinate nobody resolved.
type censusGeocoder struct{}

func (censusGeocoder) Resolve(context.Context, string) (geocode.Point, bool, error) {
	return geocode.Point{}, false, errCensusSeam
}
