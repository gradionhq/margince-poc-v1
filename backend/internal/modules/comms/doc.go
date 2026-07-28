// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package comms owns outbound message delivery: the durable record of what was
// staged for transmission, the rules that decide whether it may go now, and the
// dispatcher that hands it to a provider.
//
// It owns delivery MACHINERY, not the message. The user-visible fact of an
// outbound email is the activity row, which the activities module writes and
// owns; comms holds only the state needed to get that message out and to say
// honestly why it has not. Provider I/O lives in whichever connector implements
// ports/connector.Sender — comms never speaks to Google.
//
// Tables owned: comms_outbound.
package comms
