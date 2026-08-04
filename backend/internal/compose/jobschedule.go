// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The ONE place a dispatcher's schedule is built. A periodic entry answers
// three separate questions — does this kind register at all, on what cadence,
// and is there a cadence to place — and api/jobs.yaml states all three per
// kind. Resolving them here rather than at each wiring site is what stops a
// schedule from being a property of where a call happens to sit: a nested
// guard reads as a rule about the enclosing block, and the rule is the
// declaration's.

import (
	"time"

	"github.com/riverqueue/river"

	"github.com/gradionhq/margince/backend/internal/platform/jobs"
)

// periodicFor is the periodic entry a kind's declaration asks for, and no
// entry at all when it asks for none. The answer depends on the contract and
// on this boot's configuration and on nothing about the caller, so moving a
// wiring site cannot quietly move a schedule with it.
//
// RunOnStart is not a per-kind choice. Every pass scheduled here is a catch-up
// over a backlog that accrued while nobody was working it, and deferring the
// first pass by a whole interval after a restart is wrong for all of them: a
// retention pass would sit out a window it was already too late for, and a
// webhook backoff that elapsed during the outage would wait out a second one.
func periodicFor[A declaredJobArgs](cfg JobRunnerConfig, args A) []*river.PeriodicJob {
	spec, declared := jobs.SpecFor(args.Kind())
	if !declared {
		panic("compose: scheduling " + args.Kind() + ", which api/jobs.yaml does not declare")
	}
	if !registers(cfg, spec.Registration) {
		return nil
	}
	interval, scheduled := scheduleInterval(cfg, spec)
	if !scheduled {
		return nil
	}
	return []*river.PeriodicJob{river.NewPeriodicJob(
		river.PeriodicInterval(interval),
		func() (river.JobArgs, *river.InsertOpts) { return args, sweepInsertOpts() },
		&river.PeriodicJobOpts{RunOnStart: true},
	)}
}

// registers answers whether a kind is wired at all under this boot's
// configuration. A declaration's When is a CONJUNCTION, so one absent field
// decides — and what it decides is the declared posture:
//
//   - registers nothing — a row nothing here could work is never queued at all;
//   - registers anyway — the kind stays wired, so a row that IS picked up
//     fails with an actionable message instead of rotting queued.
//
// The two are not interchangeable, and the SAME field takes opposite postures
// on different kinds, so the answer is never a property of the field alone.
func registers(cfg JobRunnerConfig, r jobs.Registration) bool {
	supplied := configDependencies(cfg)
	for _, path := range r.When {
		present, answered := supplied[path]
		if !answered {
			panic("compose: api/jobs.yaml gates a kind on JobRunnerConfig." + path +
				", which configDependencies does not answer — add it there")
		}
		if !present {
			return r.AbsentRegistersAnyway
		}
	}
	return true
}

// scheduleInterval resolves a declared cadence to the interval River schedules
// on, and reports whether there is a schedule at all. Three forms, and the
// difference between them is the whole content of the field:
//
//   - on demand — a human's confirm enqueues this dispatcher and no clock ever
//     does, so there is no entry to place. Declared rather than inferred,
//     because a schedule someone forgot looks otherwise identical.
//   - schedule when positive — the kind stays wired and only the tick goes
//     away. River offers no cadence for a non-positive duration and refuses
//     none either: PeriodicInterval(0) yields Next(t) == t, so the enqueuer
//     re-derives a run time that never advances and dispatches as fast as
//     Postgres accepts an insert. Only the kinds that DECLARE this read a
//     non-positive dial that way; it is a posture, not a rule over every dial.
//   - otherwise — the declared literal, or the operator dial it names.
func scheduleInterval(cfg JobRunnerConfig, spec jobs.Spec) (time.Duration, bool) {
	cadence := spec.Cadence
	if cadence.OnDemand {
		return 0, false
	}
	if cadence.ScheduleWhenPositive != "" && operatorInterval(cfg, spec.Kind, cadence.ScheduleWhenPositive) <= 0 {
		return 0, false
	}
	switch {
	case cadence.OperatorField != "":
		return operatorInterval(cfg, spec.Kind, cadence.OperatorField), true
	case cadence.Fixed > 0:
		return cadence.Fixed, true
	}
	panic("compose: " + spec.Kind + " declares no cadence, so it has no schedule to place — only a dispatcher is scheduled here")
}

// operatorInterval is the configured duration behind a field path a cadence
// names, either as the cadence itself or as the dial whose positivity decides
// whether there is one.
func operatorInterval(cfg JobRunnerConfig, kind, path string) time.Duration {
	interval, answered := operatorIntervals(cfg)[path]
	if !answered {
		panic("compose: " + kind + " takes its cadence from JobRunnerConfig." + path +
			", which operatorIntervals does not answer — add it there")
	}
	return interval
}

// configDependencies is what this boot SUPPLIED, keyed by the JobRunnerConfig
// field path api/jobs.yaml names. Each entry spells its own presence test,
// because presence is not one rule: a credential custodian is a nil check and
// a Pub/Sub topic is an empty string. Named fields rather than reflection, so
// renaming one below fails the build here instead of a boot elsewhere; the
// other direction — a declared path with no entry — is a fitness test.
func configDependencies(cfg JobRunnerConfig) map[string]bool {
	return map[string]bool{
		"AgentScheduler.Service": cfg.AgentScheduler.Service != nil,
		"ChannelVault":           cfg.ChannelVault != nil,
		"ClassifyBrain":          cfg.ClassifyBrain != nil,
		"DeepReadBrain":          cfg.DeepReadBrain != nil,
		"Embedder":               cfg.Embedder != nil,
		"EnrichBrain":            cfg.EnrichBrain != nil,
		"GmailRegistry":          cfg.GmailRegistry != nil,
		"GmailWatch.Topic":       cfg.GmailWatch.Topic != "",
		"OverlayVault":           cfg.OverlayVault != nil,
		"SendRegistry":           cfg.SendRegistry != nil,
		"VoiceBrain":             cfg.VoiceBrain != nil,
		"WebhookRetry.Deliverer": cfg.WebhookRetry.Deliverer != nil,
	}
}

// operatorIntervals is every cadence an operator dials, keyed by the same
// field paths, and answered the same way and for the same reasons.
func operatorIntervals(cfg JobRunnerConfig) map[string]time.Duration {
	return map[string]time.Duration{
		"AgentScheduler.Interval":   cfg.AgentScheduler.Interval,
		"CloseDateInterval":         cfg.CloseDateInterval,
		"GmailWatch.Interval":       cfg.GmailWatch.Interval,
		"OverlayInterval":           cfg.OverlayInterval,
		"PrivacyRetention.Interval": cfg.PrivacyRetention.Interval,
		"ReconcileInterval":         cfg.ReconcileInterval,
		"TimeScanInterval":          cfg.TimeScanInterval,
		"WebhookRetry.Interval":     cfg.WebhookRetry.Interval,
	}
}
