// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package search

// Answering "which customers are within 50km of Stuttgart".
//
// The predicate has been in the grammar the whole time and always came back
// `distance_ranking_unavailable`, because no row had a point to compare. Rows
// have points now, so the question is whether THIS deployment can answer it —
// and that is a capability, decided against the schema, not a constant.
//
// WHY THIS IS A BINDING RATHER THAN A DELETION. The validator used to append
// the unavailable note unconditionally, and Execute returned on any note before
// it ever consulted the schema reader. So "can we do distance" was answered
// twice, in two places, by neither of them looking. It is answered once here
// now: bindGeo asks the storage whether the target carries a coordinate pair,
// and the note it returns when the answer is no is the SAME honest note as
// before. A deployment that has not geocoded anything is not worse off; it is
// told the same thing, for a reason that is now true.
//
// NO EGRESS FROM HERE. A center given as a place NAME is resolved against the
// installation's place cache and nothing else. query_workspace is declared
// workspace-local and Scope.Egresses() is derived from that declaration
// precisely so a tool cannot quietly reach the internet; asking a geocoder here
// would make the declaration a lie. A name the cache does not hold answers a
// note that names the place it could not resolve, and the caller who wants it
// resolved uses the enrich-scoped door.

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
)

// geoColumns is the coordinate pair a radius predicate measures from, plus the
// status column that says whether the pair means anything.
//
// THE STATUS IS NOT OPTIONAL. A row's coordinates belong to whatever address it
// held when the worker last ran, and an address can change at any time. Reading
// lat/lon without geocode_status would answer distances from where a company
// USED to be, reporting success — which is the whole defect the status column
// was added to prevent (see people/geocode.go and the staleness trigger).
type geoColumns struct {
	Lat, Lon, Status string
}

// geoCapableTargets maps a record type to the columns that make it locatable.
//
// Only `organization` today, and that is a product fact rather than a
// limitation of this code: a company has an address that means a place on the
// earth. A person's address is where somebody lives, which this product does
// not geocode and should think hard about before it does.
var geoCapableTargets = map[string]geoColumns{
	"organization": {Lat: "geocode_lat", Lon: "geocode_lon", Status: "geocode_status"},
}

// locatableTarget reports whether this record type can be somewhere at all.
//
// Separate from bindGeo because the two questions settle at different times: a
// deal is never anywhere, which validation can say for the whole plan, while
// whether "Stuttgart" resolves depends on what this workspace has looked up
// and can only be answered per call.
func locatableTarget(target string) bool {
	_, ok := geoCapableTargets[target]
	return ok
}

// geoResolvedStatus is the one status a radius query may read. It mirrors
// people.GeocodeOK, spelled here because search may not import people.
const geoResolvedStatus = "ok"

// geoBinding is a radius predicate this deployment CAN answer: where to measure
// from, how far, and which columns hold the point.
type geoBinding struct {
	Center   Point
	RadiusKM float64
	Columns  geoColumns
	// Field is the plan's own field name, so a note or an explain line can name
	// what the caller asked about rather than a column.
	Field string
}

// Point is a place on the earth.
type Point struct{ Lat, Lon float64 }

// PlaceResolver answers where a named place is, from what this INSTALLATION has
// already looked up.
//
// Installation-wide rather than per workspace, and deliberately: a place is a
// place whoever asks, so keying it by tenant would multiply every lookup by the
// number of them against a provider that holds this installation to four
// requests a minute. It is safe here because one active workspace per
// installation is an enforced invariant (identity.ErrMultipleWorkspaces) — a
// deployment that ever relaxed that would have to revisit this, since a cache
// hit and a miss are distinguishable from the outside.
//
// LOOKUP ONLY — there is deliberately no method that would resolve a name the
// cache does not hold. The seam has no door to the internet because the tool
// that reaches it has no cap to go there, and a resolver that could fetch would
// make that cap unenforceable by construction rather than by discipline.
type PlaceResolver interface {
	LookupPlace(ctx context.Context, query string) (Point, bool, error)
}

// bindGeo decides whether a radius predicate can be answered, and how.
//
// Three outcomes, and they are genuinely different things:
//   - bound: this deployment carries coordinates for the target and the center
//     resolved. The statement can measure.
//   - a NOTE: the deployment cannot answer this one — no coordinate columns, or
//     a place name nothing has looked up. Honest, and the answer says which.
//   - an error: something is broken.
func bindGeo(
	ctx context.Context, places PlaceResolver, target string, field string, operand radiusOperand,
) (*geoBinding, *Unavailable, error) {
	columns, locatable := geoCapableTargets[target]
	if !locatable {
		// The record type has no place to be. Not a deployment gap — a
		// deal is not somewhere.
		return nil, &Unavailable{Path: field, Code: CodeDistanceRankingUnavailable}, nil
	}
	center, resolved, err := resolveCenter(ctx, places, operand)
	if err != nil {
		return nil, nil, err
	}
	if !resolved {
		return nil, &Unavailable{Path: field, Code: CodeDistanceRankingUnavailable}, nil
	}
	return &geoBinding{
		Center:   center,
		RadiusKM: *operand.RadiusKM,
		Columns:  columns,
		Field:    field,
	}, nil, nil
}

// resolveCenter answers where to measure from.
//
// Coordinates given directly are used as given — a caller who already holds a
// point has done the resolving, and second-guessing them would mean asking
// somebody about a place they can already name exactly.
func resolveCenter(ctx context.Context, places PlaceResolver, operand radiusOperand) (Point, bool, error) {
	if operand.Lat != nil && operand.Lon != nil {
		return Point{Lat: *operand.Lat, Lon: *operand.Lon}, true, nil
	}
	if places == nil {
		// No cache wired: this composition cannot turn a name into a point,
		// which is a deployment fact and not a fault.
		return Point{}, false, nil
	}
	return places.LookupPlace(ctx, operand.Center)
}

// earthRadiusKM is the mean radius, which is what a haversine over a sphere
// takes. The earth is not a sphere and this is not a survey: over the
// distances a sales team asks about, the error is well under the error in
// "which office is this company actually at".
const earthRadiusKM = 6371.0

// boundingBox is the cheap pre-filter a radius query runs before any
// trigonometry.
//
// It exists so the index does the work. The haversine below is exact and
// unindexable — Postgres cannot use a btree on (lat, lon) to evaluate it — so
// the box narrows the candidate rows to something small enough for the exact
// distance to be cheap. The box is deliberately WIDER than the circle: it must
// never exclude a row the circle would have included, and including a few
// extra that the haversine then rejects costs nothing but a comparison.
func boundingBox(center Point, radiusKM float64) (minLat, maxLat, minLon, maxLon float64) {
	latDelta := radiusKM / earthRadiusKM * 180 / math.Pi
	minLat, maxLat = center.Lat-latDelta, center.Lat+latDelta

	// THE COSINE IS TAKEN AT THE WIDEST LATITUDE THE CIRCLE REACHES, not at
	// the center. Longitude degrees narrow toward the poles, so a circle
	// reaching further from the equator than its center bulges further east
	// and west than the center's own cosine predicts — and a box computed from
	// the center would cut inside the circle at those latitudes, silently
	// dropping companies that ARE within the radius. The first version of this
	// did exactly that; the test walking the circle's edge caught it.
	//
	// The widest latitude is whichever of the box's two edges sits further
	// from the equator, since |cos| shrinks as |latitude| grows.
	widest := math.Max(math.Abs(minLat), math.Abs(maxLat))
	// At the poles cos() approaches zero and the box would widen without
	// bound, so it is clamped to the whole range — a radius query near a pole
	// falls back to the haversine alone, which is correct and rare.
	cos := math.Cos(math.Min(widest, 90) * math.Pi / 180)
	lonDelta := 180.0
	if cos > 0.01 {
		lonDelta = radiusKM / (earthRadiusKM * cos) * 180 / math.Pi
	}

	// A hair of slack, because the box is a PRE-FILTER and the exact haversine
	// runs after it. A row on the boundary lost to a floating-point tie is a
	// company missing from the answer; a few extra candidates cost one
	// comparison each.
	const slack = 1e-9
	minLon, maxLon = center.Lon-lonDelta-slack, center.Lon+lonDelta+slack

	// THE BOX MUST NOT WRAP. A circle near the antimeridian spans longitudes on
	// both sides of ±180 — centre 179.9 with a 50km radius reaches -179.9 — and
	// a plain `BETWEEN 179.45 AND 180.35` excludes every one of them. The same
	// happens near a pole, where lonDelta is clamped to 180 and the box runs
	// from -80 to 280: valid rows below -80 fall outside it.
	//
	// Rather than emit a wrapped OR-pair for a case this product may never see,
	// the box widens to the whole globe whenever it would cross the edge. The
	// haversine still decides membership exactly; all that is lost is the
	// index's help, on the few queries that ask about the ends of the earth.
	if minLon < -180 || maxLon > 180 {
		minLon, maxLon = -180, 180
	}
	return minLat - slack, maxLat + slack, minLon, maxLon
}

// distanceSQL renders the great-circle distance in kilometres between a row's
// point and the center, as a SQL expression.
//
// Haversine rather than the law of cosines: the latter loses precision for
// small distances, and small distances — two companies in one city — are
// exactly what this feature is asked about.
func distanceSQL(alias string, columns geoColumns, centerLat, centerLon string) string {
	return fmt.Sprintf(`(%f * 2 * asin(sqrt(
		power(sin(radians(%s.%s - %s) / 2), 2)
		+ cos(radians(%s)) * cos(radians(%s.%s))
		* power(sin(radians(%s.%s - %s) / 2), 2))))`,
		earthRadiusKM,
		alias, columns.Lat, centerLat,
		centerLat, alias, columns.Lat,
		alias, columns.Lon, centerLon)
}

// A RADIUS BEATS SIMILARITY FOR ORDERING (Lars, 2026-08-21).
//
// "Companies like Kugellager, within 50km of Stuttgart" comes back NEAREST
// FIRST. Asking "within 50km" is asking about nearness, so distance orders the
// answer and similarity decides who qualifies for it — the reverse of the
// exact/similarity split everywhere else in this compiler, and deliberately so.
//
// That makes a THIRD sort lane. The two existing ones are: the exact lane,
// which orders in SQL by t.id, and the similarity lane, which emits no SQL
// ORDER BY at all because the retriever's ranking is applied in Go afterwards.
// A radius plan orders by the distance expression, in SQL, in both lanes —
// which is why orderByRank has to stop re-sorting when a distance is bound.
//
// STILL NOT DONE: ordering by a HOP's distance. The hop's LATERAL is
// `ORDER BY h.id LIMIT 1`, chosen so membership never depends on which related
// row came back; returning the nearest instead would change what a piece of
// evidence means. Filtering on a hop's radius is fine and works today — the
// alias is a parameter throughout — it is only the ordering that waits.
//
// The published refusal code is `distance_ranking_unavailable`, and it now
// means what it says: a deployment that cannot rank by distance says so, and
// one that can does not.

// bindGeoPredicate finds the plan's radius predicate, if it has one, and binds
// it against this deployment.
//
// ONE radius per plan, and a SECOND IS REFUSED rather than ignored.
//
// Two circles would mean an intersection, which the ordering cannot express —
// "nearest first" has no meaning when there are two centres. The first cut
// bound the first clause and let the compiler skip the rest, so a plan asking
// for companies near Stuttgart AND near Munich answered with everything near
// Stuttgart, in the shape of a correct answer. A wider answer that looks right
// is the worst failure available here, so the second clause refuses.
//
// A radius inside a TRAVERSAL is deliberately not bound here: filtering a hop
// by distance is a WHERE term the compiler already handles through the shared
// predicate path, and ordering by a hop's distance is the open question named
// at the top of this file.
func (e *QueryExecutor) bindGeoPredicate(
	ctx context.Context, plan ValidatedPlan,
) (*geoBinding, *Unavailable, error) {
	if at, found := secondRadius(plan.Plan.Where); found {
		return nil, &Unavailable{Path: at, Code: CodeDistanceRankingUnavailable}, nil
	}
	for i, clause := range plan.Plan.Where {
		if clause.Op != OpWithinRadius {
			continue
		}
		operand, ok := decodeRadiusOperand(clause.Value)
		if !ok {
			// Unreachable through Execute: the validator checked the operand's
			// shape against this same grammar. Reaching it means a plan that
			// never passed validation, which is a wiring fault rather than a
			// caller to explain anything to.
			return nil, nil, fmt.Errorf("search: where[%d] carries a radius operand that did not validate", i)
		}
		return bindGeo(ctx, e.places, plan.Target.Target, clause.Field, operand)
	}
	return nil, nil, nil
}

// decodeRadiusOperand reads the operand the validator already accepted.
func decodeRadiusOperand(raw json.RawMessage) (radiusOperand, bool) {
	var operand radiusOperand
	if err := json.Unmarshal(raw, &operand); err != nil {
		return radiusOperand{}, false
	}
	return operand, operand.namesACenter() && operand.RadiusKM != nil && *operand.RadiusKM > 0
}

// radius renders the SQL a bound radius predicate needs: the distance
// expression for the projection, and the WHERE terms that narrow to it.
//
// FOUR terms, and each earns its place:
//
//  1. geocode_status = 'ok' — the coordinates match the address the row holds
//     RIGHT NOW. A stale or failed row has a point belonging to an address the
//     company has moved from, and reading it would answer a distance from where
//     they used to be while reporting success. This is the term the whole
//     staleness design exists to make possible.
//  2. the bounding box — a cheap, INDEXED pre-filter. The haversine is exact
//     and unindexable, so without the box every radius query is a full scan.
//     The box is deliberately wider than the circle; term 4 does the trimming.
//  3. NOT NULL on both coordinates — belt and braces beside the status check,
//     because a NULL in the haversine yields NULL rather than an error, and a
//     NULL comparison is silently false.
//  4. the haversine itself, which is what actually decides membership.
func (c *planCompiler) radius(alias string, geo geoBinding) (distance string, where []string) {
	minLat, maxLat, minLon, maxLon := boundingBox(geo.Center, geo.RadiusKM)
	lat := c.arg(geo.Center.Lat)
	lon := c.arg(geo.Center.Lon)

	distance = distanceSQL(alias, geo.Columns,
		fmt.Sprintf("$%d", lat), fmt.Sprintf("$%d", lon))

	return distance, []string{
		fmt.Sprintf("%s.%s = $%d", alias, geo.Columns.Status, c.arg(geoResolvedStatus)),
		fmt.Sprintf("%s.%s IS NOT NULL AND %s.%s IS NOT NULL",
			alias, geo.Columns.Lat, alias, geo.Columns.Lon),
		fmt.Sprintf("%s.%s BETWEEN $%d AND $%d", alias, geo.Columns.Lat,
			c.arg(minLat), c.arg(maxLat)),
		fmt.Sprintf("%s.%s BETWEEN $%d AND $%d", alias, geo.Columns.Lon,
			c.arg(minLon), c.arg(maxLon)),
		fmt.Sprintf("%s <= $%d", distance, c.arg(geo.RadiusKM)),
	}
}

// secondRadius finds a second radius predicate, and where it is.
//
// Reported rather than silently dropped: the clauses are ANDed, so a dropped
// one widens the answer without changing its shape — every company near
// Stuttgart returned for a question that asked for those ALSO near Munich, and
// nothing in the response saying so.
func secondRadius(clauses []Predicate) (string, bool) {
	seen := false
	for i, clause := range clauses {
		if clause.Op != OpWithinRadius {
			continue
		}
		if seen {
			return "where[" + strconv.Itoa(i) + "]", true
		}
		seen = true
	}
	return "", false
}
