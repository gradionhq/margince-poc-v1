// The committed vanilla composition stub (ADR-0069): bare go builds
// resolve this module through backend/go.mod's replace; make lanes
// override it with the generated module under build/composition/. The
// sibling go.sum is EMPTY by construction — the single require is a
// directory replace (no remote hashes exist to lock) and the stub
// imports only the stdlib-only published surface. It is committed so
// module-mode builds verify hashes the moment a real dependency ever
// appears, instead of silently resolving unverified.
module github.com/gradionhq/margince/composition

go 1.26.5

require github.com/gradionhq/margince/backend v0.0.0

replace github.com/gradionhq/margince/backend => ../backend
