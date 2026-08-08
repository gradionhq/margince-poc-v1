// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

// The two rules that make "this file is the whole declaration" true, kept
// beside each other because they answer one question: can a second
// declaration of something reach the emitters without being seen?
//
// Duplicate keys are the third member of that question and need no code
// here: yaml.v3's decoder refuses them by default (uniqueKeys), at every
// depth and independently of KnownFields. gen-jobs' rejectDuplicateKinds
// predates that knowledge and is belt-and-braces over the same property
// with a nicer message; adding a second copy of it here would be a third.
// TestAITasksRejectsDuplicateKey pins the behaviour so a yaml.v3 bump that
// changed the default could not land quietly.

import (
	"bytes"
	"errors"
	"fmt"
	"io"

	"gopkg.in/yaml.v3"
)

// decodeMapping reads one mapping node into out with unknown keys refused.
//
// It exists because yaml.Node.Decode builds its own decoder and so does NOT
// carry the KnownFields setting parseContract put on the outer one: every
// type below with a custom UnmarshalYAML is a hole in that setting, and a
// typo inside one is dropped in silence. That is not a missing declaration
// but a DIFFERENT one — `kinds: agent_loop` on a site leaves Kind at the
// one_shot default, the opposite certification posture, and
// `conditionals: true` on a company-context policy leaves the caller never
// asked. Re-encoding the node and reading it back through a strict decoder
// is what restores the setting.
//
// This is gen-jobs' contract.go helper, ported deliberately rather than
// shared: the two generators are separate commands in one module with no
// common package between them, and a shared one would exist for this single
// function. If a third generator needs it, that is the moment to lift it.
//
// The destination is a POINTER by signature rather than by convention: yaml
// takes its own destination as an empty interface and reports a value passed
// by mistake at run time, on a document that may not be the one under test.
func decodeMapping[T any](node *yaml.Node, out *T) error {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	if err := enc.Encode(node); err != nil {
		return fmt.Errorf("re-encoding for a strict read: %w", err)
	}
	if err := enc.Close(); err != nil {
		return fmt.Errorf("re-encoding for a strict read: %w", err)
	}
	dec := yaml.NewDecoder(&buf)
	dec.KnownFields(true)
	return dec.Decode(out)
}

// rejectSecondDocument refuses a `---` and anything after it. Only the FIRST
// document is decoded, but the fingerprint tasks_gen.go carries
// (TaskContractHash) is the sha256 of the whole FILE. So tasks after a
// separator would be hashed as if they governed routing while reaching
// neither the ladder table nor the census — and every gate downstream
// compares one generated half against the other rather than against the
// file, so nothing would ever notice.
func rejectSecondDocument(dec *yaml.Decoder) error {
	var second yaml.Node
	err := dec.Decode(&second)
	switch {
	case errors.Is(err, io.EOF):
		return nil
	case err != nil:
		return fmt.Errorf("parsing contract: reading past the first document: %w", err)
	default:
		return fmt.Errorf("the contract carries more than one YAML document — everything after the first `---` is hashed into the generated fingerprint and compiled into no table; keep every task in one document")
	}
}
