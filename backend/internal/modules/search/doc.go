// Package search owns cross-object retrieval (B-EP05.15+): ranked
// full-text search over the generated search_tsv columns today, the
// pgvector/RRF hybrid and the context graph as they land. Every branch
// of every query carries the caller's row-scope predicate — a search
// hit IS a read, so existence-hiding holds here exactly as on the
// per-entity lists.
//
// Tables owned: none — search reads the domain modules' tables through
// their declared indexes (embedding rows join it in the retrieval
// slice). Imports shared + platform only; never a sibling module.
package search
