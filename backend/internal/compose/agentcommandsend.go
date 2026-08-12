// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The REST door's half of the four outbound commands
// (gradionhq/margince-poc-v1#928 task 7): a mail reply, a channel reply, an
// account-started send and a booking. Unlike every family before them, each of
// these has a real tool-door twin resolving the SAME command — so a refusal
// the tool made and this door skipped (an empty `to`, an empty `links`, a link
// over the cap, a link held in another system of record, a booking whose end
// precedes its start, an anchor that is not a channel conversation) is now one
// refusal both doors reach.
//
// These are also the first decoders that read the request BODY for operands
// rather than only for a create's fields: a send names its addressees there,
// and a booking names its slot and its records. body is the same buffered copy
// stageRefusal already hashed into canonicalRESTCall — a stream has one honest
// reading, and the gate has already taken it.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"time"

	"github.com/gradionhq/margince/backend/internal/modules/agents"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// commandBody decodes the buffered body into one decoder's view of it.
//
// An ABSENT body answers the zero value rather than an error. Three of this
// task's routes declare their body optional in crm.yaml, and for the rest a
// missing required field is the HANDLER's 422 to write: this decoder's job is
// to say what an approval would bind to, and a governance layer that refused
// the request first would answer one mistake with two different machine codes
// depending on which credential the caller presented.
//
// Unreadable JSON is different, and is refused with the code httperr.Decode
// answers on the session half of the same route — the rule
// advanceDealTierInput (agentgate.go) already follows for the same reason.
func commandBody[T any](body []byte) (T, error) {
	var into T
	if len(bytes.TrimSpace(body)) == 0 {
		return into, nil
	}
	if err := json.Unmarshal(body, &into); err != nil {
		return into, httperr.Validation("body", "malformed_json", "the request body is not readable JSON")
	}
	return into, nil
}

// sendEmailCommand decodes POST /v1/activities/{id}/send-email. The routed
// {id} is the ANCHOR — the thread being replied to — and the addressees come
// from the body, which is where crm.yaml's SendEmailRequest puts them.
//
//nolint:ireturn // a decoder's whole product is the erased command-and-resolver pair restCommands is typed by
func sendEmailCommand(_ agentPolicy, deps restCommandDeps, r *http.Request, body []byte) (agents.GovernedCall, error) {
	id, err := routedID(r)
	if err != nil {
		return nil, err
	}
	in, err := commandBody[struct {
		To      []string `json:"to"`
		Cc      []string `json:"cc"`
		Subject string   `json:"subject"`
	}](body)
	if err != nil {
		return nil, err
	}
	return agents.NewSendEmailCall(deps.records, agents.SendEmailCommand{
		ActivityID: id,
		To:         in.To,
		Cc:         in.Cc,
		Subject:    in.Subject,
	}), nil
}

// sendMessageCommand decodes POST /v1/activities/{id}/send-message. The body
// carries the message text and nothing else the resolver reads: a channel
// reply names no addressee, because the conversation does.
//
//nolint:ireturn // a decoder's whole product is the erased command-and-resolver pair restCommands is typed by
func sendMessageCommand(_ agentPolicy, deps restCommandDeps, r *http.Request, body []byte) (agents.GovernedCall, error) {
	id, err := routedID(r)
	if err != nil {
		return nil, err
	}
	in, err := commandBody[struct {
		Body string `json:"body"`
	}](body)
	if err != nil {
		return nil, err
	}
	return agents.NewSendMessageCall(deps.records, deps.channels, agents.SendMessageCommand{
		ActivityID: id,
		Body:       in.Body,
	}), nil
}

// sendAccountEmailCommand decodes POST /v1/emails. There is no routed id to
// read: this send starts a conversation rather than answering one, so the
// records it belongs to are NAMED in the body — which is exactly why the route
// walk this replaces could offer no target for it at all.
//
//nolint:ireturn // a decoder's whole product is the erased command-and-resolver pair restCommands is typed by
func sendAccountEmailCommand(_ agentPolicy, deps restCommandDeps, _ *http.Request, body []byte) (agents.GovernedCall, error) {
	in, err := commandBody[struct {
		To      []string            `json:"to"`
		Cc      []string            `json:"cc"`
		Subject string              `json:"subject"`
		Links   []agents.RecordLink `json:"links"`
	}](body)
	if err != nil {
		return nil, err
	}
	return agents.NewSendAccountEmailCall(deps.records, agents.SendAccountEmailCommand{
		To:      in.To,
		Cc:      in.Cc,
		Subject: in.Subject,
		Links:   in.Links,
	}), nil
}

// bookMeetingCommand decodes POST /v1/bookings. It has no routed id either —
// a booking anchors on no existing row — and every operand the resolver reads
// is in the body.
//
//nolint:ireturn // a decoder's whole product is the erased command-and-resolver pair restCommands is typed by
func bookMeetingCommand(_ agentPolicy, deps restCommandDeps, _ *http.Request, body []byte) (agents.GovernedCall, error) {
	in, err := commandBody[struct {
		HostUserID *ids.UUID           `json:"host_user_id"`
		Start      time.Time           `json:"start"`
		End        time.Time           `json:"end"`
		Subject    string              `json:"subject"`
		Links      []agents.RecordLink `json:"links"`
	}](body)
	if err != nil {
		return nil, err
	}
	return agents.NewBookMeetingCall(deps.records, agents.BookMeetingCommand{
		HostUserID: in.HostUserID,
		Start:      in.Start,
		End:        in.End,
		Subject:    in.Subject,
		Links:      in.Links,
	}), nil
}
