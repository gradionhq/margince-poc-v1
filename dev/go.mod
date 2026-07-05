// Dev-only tooling module — kept OUT of the product module (backend/) so
// the four process-role binaries stay clean and this never ships. Runs the
// local HTTPS front door that gives the SPA a single Secure-cookie origin.
module margince-dev

go 1.26
