// The desktop launcher is deliberately stdlib-only and deliberately OUTSIDE
// go.work: it supervises the shipped binaries as child processes rather than
// importing them, so it shares no dependency graph with the backend module and
// must not perturb the workspace the existing gates resolve against. Build it
// with GOWORK=off.
module github.com/gradionhq/margince/desktop/launcher

go 1.26.5
