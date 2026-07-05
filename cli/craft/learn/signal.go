// Package learn captures the craftsmanship gate's learning signals and the
// dispute-adjudication queue (foundation architecture/17). The signals feed the
// exemplar memory (B-EP11.8a) and the golden set (B-EP11.6); adjudications are the
// gold signal because they sit on the decision boundary.
//
// EP07's approval-inbox is not built yet, so the queue + signal store are an
// interim append-only JSONL store. When EP07 lands, the queue migrates to the
// shared approval-inbox; the Signal/Dispute shapes here are the contract.
package learn

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// SignalType is one of the four learning signals, in descending order of value.
type SignalType string

const (
	// SignalAdjudication — a human dispute resolution. The gold signal: it sits
	// on the decision boundary (a false positive or a confirmed correct block).
	SignalAdjudication SignalType = "adjudication"
	// SignalSpotAudit — a sampled review of an auto-PASSed PR; catches false negatives.
	SignalSpotAudit SignalType = "spot_audit"
	// SignalPostMergeDefect — slop found after merge; a gate miss with a known-bad signature.
	SignalPostMergeDefect SignalType = "post_merge_defect"
	// SignalResolvedFixPair — a CRAFT-FIX that was fixed and cleared; ground truth
	// for "this was real slop" and "this is what a good fix looks like".
	SignalResolvedFixPair SignalType = "resolved_fix_pair"
)

// Polarity records whether a signal is a positive (confirmed slop) or negative
// (false positive) example for the exemplar memory.
type Polarity string

const (
	// Positive marks a confirmed-slop example.
	Positive Polarity = "positive"
	// Negative marks a false-positive example.
	Negative Polarity = "negative"
)

// Signal is one captured learning event. Provenance is required — every exemplar
// must be traceable to the adjudication, audit, or PR it came from (B-EP11.8a).
type Signal struct {
	Type       SignalType `json:"type"`
	FindingID  string     `json:"finding_id,omitempty"`
	Category   string     `json:"category,omitempty"`
	Polarity   Polarity   `json:"polarity"`
	Provenance string     `json:"provenance"`
	Detail     string     `json:"detail,omitempty"`
}

// Store is the append-only signal log. It only grows — signals are never rewritten.
type Store struct{ path string }

// NewStore returns a Store backed by the append-only JSONL log at path.
func NewStore(path string) *Store { return &Store{path: path} }

// Append writes one signal as a JSON line. It refuses a signal without provenance:
// an untraceable exemplar degrades the gate (architecture/17 §3).
func (s *Store) Append(sig Signal) error {
	if sig.Provenance == "" {
		return fmt.Errorf("signal %s has no provenance", sig.Type)
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o750); err != nil {
		return err
	}
	f, err := os.OpenFile(s.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	line, err := json.Marshal(sig)
	if err != nil {
		//craft:ignore swallowed-errors nothing was written yet; the marshal error is the one to report
		_ = f.Close()
		return err
	}
	if _, err := fmt.Fprintln(f, string(line)); err != nil {
		//craft:ignore swallowed-errors the write already failed; its error supersedes the close
		_ = f.Close()
		return err
	}
	// Close is the last step that can lose the appended line — report it.
	return f.Close()
}

// All reads back every captured signal.
func (s *Store) All() ([]Signal, error) {
	f, err := os.Open(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	//craft:ignore swallowed-errors read-only close cannot lose data; scanner errors carry the read failure
	defer func() { _ = f.Close() }()
	var out []Signal
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var sig Signal
		if err := json.Unmarshal(sc.Bytes(), &sig); err != nil {
			return nil, fmt.Errorf("parse signal line: %w", err)
		}
		out = append(out, sig)
	}
	return out, sc.Err()
}
