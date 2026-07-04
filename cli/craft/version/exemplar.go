package version

import (
	_ "embed"
	"encoding/json"
	"fmt"
)

//go:embed exemplars.json
var exemplarsRaw []byte

// Exemplar is one curated few-shot example the reviewer retrieves at review time
// (Reflexion-style memory, architecture/17 §3). It carries its source so every
// exemplar is traceable to the adjudication or audit it came from.
type Exemplar struct {
	ID         string `json:"id"`
	Polarity   string `json:"polarity"` // positive (real slop) | negative (false positive)
	Category   string `json:"category"`
	Code       string `json:"code"`
	Rationale  string `json:"rationale"`
	Provenance string `json:"provenance"`
}

// ExemplarSet is the versioned, curated memory. It improves the gate without
// retraining: a better set is a better gate. The version is part of the identity tuple.
type ExemplarSet struct {
	Version   string     `json:"version"`
	Exemplars []Exemplar `json:"exemplars"`
}

// LoadExemplars returns the embedded set.
func LoadExemplars() (ExemplarSet, error) {
	var s ExemplarSet
	if err := json.Unmarshal(exemplarsRaw, &s); err != nil {
		return ExemplarSet{}, fmt.Errorf("parse exemplars.json: %w", err)
	}
	return s, nil
}

// Curate adds a candidate exemplar, enforcing that the memory is curated, not
// blindly appended: a candidate must carry provenance and must not duplicate an
// existing id (a noisy set degrades the gate, architecture/17 §3). It returns the
// new set; the caller bumps the version as an explicit promotion (B-EP11.8b).
func Curate(set ExemplarSet, candidate Exemplar) (ExemplarSet, error) {
	if candidate.ID == "" {
		return set, fmt.Errorf("exemplar has no id")
	}
	if candidate.Provenance == "" {
		return set, fmt.Errorf("exemplar %s has no provenance — untraceable exemplars degrade the gate", candidate.ID)
	}
	for _, e := range set.Exemplars {
		if e.ID == candidate.ID {
			return set, fmt.Errorf("exemplar %s already present — curated, not blindly appended", candidate.ID)
		}
	}
	set.Exemplars = append(set.Exemplars, candidate)
	return set, nil
}
