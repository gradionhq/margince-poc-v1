// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package aicert

// Rendering a scenario back OUT as corpus YAML — the scaffold half of the debug
// lane. The contract is a round trip: whatever this writes, LoadScenarioFile
// (scenario.go) must read back unchanged, which is why a number the format
// cannot carry is refused here rather than quietly approximated.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// scenarioForRender mirrors Scenario for OUTPUT only. It exists because
// Scenario carries its fixture as JSONValue — a []byte, which yaml.Marshal
// renders as a list of byte values rather than as the mapping it holds.
// Fixture and Answer are `any` here so the JSON is decoded back into a plain
// value and emits as the YAML a human edits and LoadScenarioFile reads back.
type scenarioForRender struct {
	Name        string          `yaml:"name"`
	Task        string          `yaml:"task"`
	Site        string          `yaml:"site"`
	Source      string          `yaml:"source"`
	SanitizedBy string          `yaml:"sanitized_by"`
	Fixture     any             `yaml:"fixture"`
	Expect      expectForRender `yaml:"expect"`
}

type expectForRender struct {
	Outcome string `yaml:"outcome"`
	Answer  any    `yaml:"answer,omitempty"`
	Rubric  string `yaml:"rubric,omitempty"`
	Bands   Bands  `yaml:"bands"`
	Caps    Caps   `yaml:"caps,omitempty"`
}

// RenderScenario emits a scenario as the YAML the corpus format uses, suitable
// as a starting point an operator edits. The round trip is the contract:
// whatever this writes, LoadScenarioFile must read back.
func RenderScenario(sc Scenario) ([]byte, error) {
	out := scenarioForRender{
		Name: sc.Name, Task: sc.Task, Site: sc.Site,
		Source: sc.Source, SanitizedBy: sc.SanitizedBy,
		Expect: expectForRender{
			Outcome: sc.Expect.Outcome, Rubric: sc.Expect.Rubric,
			Bands: sc.Expect.Bands, Caps: sc.Expect.Caps,
		},
	}
	fixture, err := decodePreservingNumbers(sc.Fixture)
	if err != nil {
		return nil, fmt.Errorf("aicert: the fixture of %s is not decodable JSON: %w", sc.Name, err)
	}
	out.Fixture = fixture
	if len(sc.Expect.Answer) > 0 {
		answer, err := decodePreservingNumbers(sc.Expect.Answer)
		if err != nil {
			return nil, fmt.Errorf("aicert: the expected answer of %s is not decodable JSON: %w", sc.Name, err)
		}
		out.Expect.Answer = answer
	}
	// Refuse BEFORE marshaling: a scenario that cannot load back unchanged is
	// not a starting point, and emitting one silently is the failure this whole
	// path exists to prevent.
	if err := representableNumbers(out.Fixture, "fixture"); err != nil {
		return nil, err
	}
	if err := representableNumbers(out.Expect.Answer, "expect.answer"); err != nil {
		return nil, err
	}
	body, err := yaml.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("aicert: rendering %s: %w", sc.Name, err)
	}
	return body, nil
}

// decodePreservingNumbers decodes JSON into plain values WITHOUT routing every
// number through float64.
//
// The default decoding would render a 19-digit id as 1.234567890123457e+18 and
// an exact literal as its nearest double — silently changing the fixture on the
// way to YAML, which is the one thing a round trip must not do.
//
//craft:ignore naked-any the return is a whole decoded JSON document — a fixture is free to be an object, an array or a bare scalar, so the shape is only known by inspecting it
func decodePreservingNumbers(raw []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var decoded any
	if err := dec.Decode(&decoded); err != nil {
		return nil, err
	}
	// A decoder stops at the end of the FIRST value, so anything after it would
	// be dropped in silence — the fixture rendered would not be the fixture
	// supplied. json.Unmarshal refuses trailing data; this has to say so too.
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("trailing data after the JSON value")
	}
	return exactNumbers(decoded), nil
}

// exactNumbers replaces every json.Number with a yamlNumber, so the LEXEME the
// scenario was written with is what reaches the YAML.
//
// Converting to int64/float64 first would be lossless only for values those
// types hold exactly; anything wider — a 20-digit id, a high-precision
// decimal — would be silently approximated, which is the failure this whole
// path exists to prevent.
//
//craft:ignore naked-any v is one node of a decoded JSON tree — mapping, sequence or scalar — and the type switch below IS what decides which; the rewritten node goes back into the same untyped slot it came from
func exactNumbers(v any) any {
	switch typed := v.(type) {
	case map[string]any:
		for k, inner := range typed {
			typed[k] = exactNumbers(inner)
		}
		return typed
	case []any:
		for i, inner := range typed {
			typed[i] = exactNumbers(inner)
		}
		return typed
	case json.Number:
		return yamlNumber(typed.String())
	default:
		return v
	}
}

// representableNumbers refuses a value the corpus YAML format cannot carry back
// unchanged.
//
// yaml.v3 decodes an integer wider than int64 as a float and then refuses it as
// an !!int, and a decimal finer than float64 comes back rounded. Either way the
// scenario that loads is not the scenario that was written — so RenderScenario
// says so instead of emitting one that quietly differs.
//
//craft:ignore naked-any v walks the same decoded JSON tree exactNumbers produced, where every node is untyped until the switch reaches the numbers this check is about
func representableNumbers(v any, path string) error {
	switch typed := v.(type) {
	case map[string]any:
		for k, inner := range typed {
			if err := representableNumbers(inner, path+"."+k); err != nil {
				return err
			}
		}
	case []any:
		for i, inner := range typed {
			if err := representableNumbers(inner, fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
	case yamlNumber:
		return typed.representable(path)
	}
	return nil
}

// representable reports whether this literal survives a YAML round trip.
func (n yamlNumber) representable(path string) error {
	text := string(n)
	if !strings.ContainsAny(text, ".eE") {
		if _, err := strconv.ParseInt(text, 10, 64); err != nil {
			return fmt.Errorf(
				"aicert: %s is the integer %s, which is wider than the corpus format can carry back unchanged — quote it as a string if the scenario needs it verbatim",
				strings.TrimPrefix(path, "."), text)
		}
		return nil
	}
	f, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return fmt.Errorf("aicert: %s is the number %s, which the corpus format cannot carry back unchanged", strings.TrimPrefix(path, "."), text)
	}
	// Compared as VALUES, not as spellings: 1e-6 and 1e-06 are the same number,
	// and so are 1.50 and 1.5. A string comparison would refuse a perfectly
	// representable fixture over how it happens to be written.
	loaded := strconv.FormatFloat(f, 'g', -1, 64)
	written, wroteOK := new(big.Rat).SetString(text)
	readBack, readOK := new(big.Rat).SetString(loaded)
	if !wroteOK || !readOK {
		return fmt.Errorf("aicert: %s is the number %s, which the corpus format cannot carry back unchanged", strings.TrimPrefix(path, "."), text)
	}
	// Equal values mean the literal survives float64; unequal means it would
	// load as something else, which is the failure this check exists to name.
	if written.Cmp(readBack) != 0 {
		return fmt.Errorf(
			"aicert: %s is the number %s, whose precision exceeds what the corpus format carries back (it would load as %s) — quote it as a string if the scenario needs it verbatim",
			strings.TrimPrefix(path, "."), text, loaded)
	}
	return nil
}

// yamlNumber emits a JSON number's own text as a YAML scalar, unquoted, so it
// round-trips as the number it was rather than as a string or an approximation.
type yamlNumber string

// MarshalYAML tags the scalar so a reader parses it back as a number. The tag is
// chosen from the lexeme rather than by converting: an integer literal too wide
// for int64 is still an integer, and saying so costs nothing.
//
//craft:ignore naked-any the signature is fixed by yaml.v3's Marshaler interface; naming *yaml.Node here would stop implementing it
func (n yamlNumber) MarshalYAML() (any, error) {
	tag := "!!int"
	if strings.ContainsAny(string(n), ".eE") {
		tag = "!!float"
	}
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: tag, Value: string(n)}, nil
}
