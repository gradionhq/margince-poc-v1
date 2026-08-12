// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// Where a mounted extension route's ARGUMENTS come from, and what they must be
// before a unit's handler sees them.
//
// Split from extroutes.go, which owns the mounting and the response. This half
// exists because it is the whole of the argument validation in the system:
// nothing downstream checks a tool's arguments against its declared schema (this
// codebase carries no jsonschema dependency by choice), so a value that leaves
// this file reaches a handler as whatever it happened to parse as.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strconv"

	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/pkg/extension"
)

// The bodyless routes have no cap of their own, and do not need one: their
// arguments are in the request line, which Go bounds with the request headers at
// http.DefaultMaxHeaderBytes (1 MB, since cmd/api sets no MaxHeaderBytes) and
// answers 431 for before a handler runs. Named here rather than left to be
// inferred, because the explicit cap below otherwise reads as the only one.
//
// maxExtensionRequestBody bounds the argument document a mounted route reads.
// A tool's arguments are a small JSON object by construction (the declared
// input schema is one), and the body is fully buffered before the handler runs,
// so an unbounded read would be a per-request memory cost any authenticated
// seat could set.
const maxExtensionRequestBody = 1 << 20 // 1 MiB

// jsonTrue and jsonFalse are the only boolean spellings a query argument may
// take, and also the JSON tokens they become. See encodeQueryValue.
const (
	jsonTrue  = "true"
	jsonFalse = "false"
)

// queryArgs describes a BODYLESS operation's arguments: the JSON type each
// declared query parameter must become, and which of them the caller must send.
//
// Resolved once, at mount, rather than per request. The declaration cannot
// change while the process runs, and re-parsing the schema on every call would
// put a JSON decode of the contract in front of every read.
type queryArgs struct {
	// types maps each declared argument to its declared JSON type. It is the
	// closed set of names this route accepts, so an unknown query key is refused
	// by its absence here rather than by a second list that could disagree.
	types map[string]string
	// required names the arguments a call must carry. Sorted at construction, so
	// the refusal a caller reads names them in a stable order.
	required []string
}

// argumentReader reads ONE request's arguments, whichever side of the request
// its operation declared them on.
//
// The source is resolved to a function at mount rather than decided per request,
// which is what keeps the handler from branching on the declaration on every
// call — and, more to the point, from being able to branch WRONGLY. A route
// cannot read a body it has no schema for, because the only reader it holds is
// the one its own declaration produced.
type argumentReader func(w http.ResponseWriter, r *http.Request) (json.RawMessage, error)

// argumentReaderFor picks the reader an operation's method calls for, and fails
// the mount if a bodyless operation's declaration cannot be described.
func argumentReaderFor(v extension.Verb) (argumentReader, error) {
	if extension.CarriesBody(v.Method) {
		return readBodyArguments, nil
	}
	args, err := queryArgumentsFor(v)
	if err != nil {
		return nil, err
	}
	// A bodyless route does not read a body at all, not even to reject one: the
	// contract says the operation has none, so reading it would make the route's
	// behaviour depend on something no client was told to send.
	return func(_ http.ResponseWriter, r *http.Request) (json.RawMessage, error) {
		// url.ParseQuery, NOT r.URL.Query(). Query() calls the same parser and
		// THROWS THE ERROR AWAY, keeping the pairs that parsed and dropping the
		// ones that did not — so `?payload=p&exact=%zz` arrives as a query with no
		// `exact` in it, and every strict rule below is applied to a value set the
		// caller did not send. That is the silent-argument-loss this decode exists
		// to prevent, one call above the code that prevents it: a dropped
		// `?payload=a&payload=%zz` also becomes single-valued, so the repeated-key
		// refusal is bypassed and the seam picks a winner after all.
		values, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil {
			return nil, httperr.Validation("query", "malformed_query",
				"the query string is not valid urlencoded form data")
		}
		return args.decode(values)
	}, nil
}

// readBodyArguments reads a body-carrying operation's arguments: the request body
// as the tool's arguments document, bounded and checked for well-formedness.
func readBodyArguments(w http.ResponseWriter, r *http.Request) (json.RawMessage, error) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxExtensionRequestBody))
	if err != nil {
		return nil, httperr.Validation("body", "malformed_json", "the request body could not be read")
	}
	if len(body) == 0 {
		// The declared input schema is an object, so an absent body is the empty
		// object rather than a refusal: a tool taking no arguments is callable
		// with no body.
		return json.RawMessage(`{}`), nil
	}
	if !json.Valid(body) {
		return nil, httperr.Validation("body", "malformed_json", "the request body is not valid JSON")
	}
	return body, nil
}

// queryArgumentsFor reads a bodyless operation's argument description out of its
// declared input schema.
//
// It returns an error rather than tolerating a schema it cannot read. Every shape
// it refuses, Verb.Validate refuses first — a non-object root, a property whose
// type is not query-encodable, a `required` naming an argument the properties do
// not declare — so reaching one of these means the served declaration did not
// come through Validate. The check stays anyway, at the cost of two branches no
// generated composition reaches: mounting PANICS on a refusal (see
// extensionEdge), so this is the difference between a boot that stops naming the
// operation and a route that serves every call while quietly taking no
// arguments.
func queryArgumentsFor(v extension.Verb) (queryArgs, error) {
	args := queryArgs{types: map[string]string{}}
	if v.InputSchema == nil {
		return args, nil
	}
	var doc struct {
		Properties map[string]struct {
			Type string `json:"type"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(v.InputSchema, &doc); err != nil {
		return queryArgs{}, fmt.Errorf("operation %s declares an input schema this route cannot read: %w", v.OperationID, err)
	}
	for name, prop := range doc.Properties {
		args.types[name] = prop.Type
	}
	for _, name := range doc.Required {
		if _, declared := args.types[name]; !declared {
			return queryArgs{}, fmt.Errorf("operation %s requires the argument %q, which its input schema does not declare — nothing could ever satisfy this route", v.OperationID, name)
		}
	}
	args.required = slices.Clone(doc.Required)
	slices.Sort(args.required)
	return args, nil
}

// decode turns a request's query string into the tool's arguments document.
//
// It is STRICT, and it is the only thing that is: nothing downstream validates a
// tool's arguments against its declared schema (this codebase carries no
// jsonschema dependency by choice), so a value this function lets through
// reaches the handler as whatever it happened to parse as. Hence an unknown key
// is refused rather than dropped, a repeated key is refused rather than
// resolved to one of its values, and a value that is not of its declared type is
// refused rather than passed along as a string for the handler to re-parse.
//
// The refusals go through httperr.Validation so an extension route's bad-input
// answer has the same shape as the core route beside it.
func (q queryArgs) decode(values url.Values) (json.RawMessage, error) {
	args := make(map[string]json.RawMessage, len(values))
	// Sorted, because the loop below returns on the FIRST bad argument and a Go
	// map's order is random: given two bad arguments, an unsorted walk names one
	// of them on this call and the other on the next. A client — or a client's
	// test — cannot be written against a refusal that moves.
	for _, name := range slices.Sorted(maps.Keys(values)) {
		vals := values[name]
		declared, ok := q.types[name]
		if !ok {
			return nil, httperr.Validation(name, "unknown_parameter",
				"this operation declares no argument by that name")
		}
		if len(vals) > 1 {
			// A repeated key has no meaning in a flat object: the schema says this
			// argument is one primitive, so picking the first or the last would be
			// this seam inventing a rule the published contract does not state.
			return nil, httperr.Validation(name, "repeated_parameter",
				"this argument was given more than once, and it takes a single value")
		}
		encoded, err := encodeQueryValue(declared, vals[0])
		if err != nil {
			return nil, httperr.Validation(name, "invalid_type", err.Error())
		}
		args[name] = encoded
	}
	for _, name := range q.required {
		if _, sent := args[name]; !sent {
			return nil, httperr.Validation(name, "missing_parameter",
				"this operation requires the argument")
		}
	}
	// Marshalled from a map, so the emitted object's keys are sorted and one
	// call's arguments cannot differ from another's by query order alone.
	return json.Marshal(args)
}

// encodeQueryValue turns one query value — always text — into the JSON type its
// declaration promised a handler it would be.
//
// The parsed value is re-marshalled rather than the raw text passed through.
// Text that parses is not necessarily text JSON accepts in that position: a
// declared integer given "007" or "+7" parses to 7, and emitting the original
// would put a token in the arguments document that no JSON reader would accept
// as a number.
func encodeQueryValue(declared, text string) (json.RawMessage, error) {
	switch declared {
	case "string":
		return json.Marshal(text)
	case "boolean":
		// The JSON spellings only. The looser ones ("1", "yes", "on", "True") are
		// each a convention some client uses and none the contract states, and
		// guessing which one a caller meant is how a flag silently reads false.
		//
		// The two literals are named because the accepted query spelling and the
		// emitted JSON token are THE SAME STRING here, and that is a coincidence of
		// JSON rather than a rule: writing it once makes the pass-through obvious
		// and keeps a future edit from changing one side only.
		switch text {
		case jsonTrue, jsonFalse:
			return json.RawMessage(text), nil
		}
		return nil, fmt.Errorf("expected a boolean, spelled %q or %q", jsonTrue, jsonFalse)
	case "integer":
		n, err := strconv.ParseInt(text, 10, 64)
		if err != nil {
			return nil, errors.New("expected an integer")
		}
		return json.Marshal(n)
	case "number":
		// decimalNumeral BEFORE ParseFloat, because ParseFloat is far more
		// permissive than JSON: it accepts "NaN", "Inf", "Infinity", hex floats
		// ("0x1p10" → 1024) and underscored digits ("1_0" → 10). Every one of those
		// is a spelling the published `number` contract does not admit and no
		// generated client would produce, and the first three cannot be marshalled
		// as JSON at all — so without this the refusal came from json.Marshal
		// failing, and its raw error ("json: unsupported value: NaN") was handed to
		// the caller as the validation detail.
		//
		// It also keeps `number` and `integer` on the same grammar. ParseInt in base
		// 10 already refuses underscores and hex, so the two types would otherwise
		// disagree about what a numeral is.
		if !decimalNumeral(text) {
			return nil, errors.New("expected a number")
		}
		n, err := strconv.ParseFloat(text, 64)
		if err != nil {
			return nil, errors.New("expected a number")
		}
		return json.Marshal(n)
	}
	// Unreachable through a declaration Verb.Validate admitted, which refuses any
	// other type on a bodyless method. Named rather than defaulted to string,
	// because defaulting would serve an argument shape the contract never
	// published.
	return nil, fmt.Errorf("declares the unsupported query type %q", declared)
}

// decimalNumeral reports whether text is written with the characters a JSON
// number is written with. It is a CHARACTER-SET test and not a grammar: the
// structure is strconv's to check, and the two together are what pin the accepted
// spelling to the one the contract published. See encodeQueryValue's number case
// for what it exists to keep out.
func decimalNumeral(text string) bool {
	if text == "" {
		return false
	}
	for _, r := range text {
		switch {
		case r >= '0' && r <= '9', r == '+', r == '-', r == '.', r == 'e', r == 'E':
			continue
		}
		return false
	}
	return true
}
