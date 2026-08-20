// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

import "fmt"

// say writes one piece of the launcher's user-facing output.
//
// The repo's lint baseline forbids fmt.Print* in favour of slog, and it is right
// to everywhere else: structured output is what a log collector reads. This is
// the one place that rule does not fit. A person double-clicks a starter and
// watches a console window; what they need to read is
//
//	Margince is running at  http://127.0.0.1:8800
//
// and not that line wrapped in key=value framing. The structured logs still
// exist and still go through slog — they are the SERVICES' logs, written to
// data/logs/ by startChild, which is a different channel with a different
// reader.
//
// So the output is funnelled through one function and the waiver is stated once,
// here, with its reason. Seventeen //nolint comments across two files would say
// the same thing seventeen times and drift the moment one of them was copied
// somewhere it did not belong.
//
// Failures are not reported: a console write that fails has nowhere left to
// report to, and the caller's next line would fail identically.
func say(format string, args ...any) {
	//nolint:forbidigo // this function IS the user-facing console; see above
	fmt.Printf(format, args...)
}
