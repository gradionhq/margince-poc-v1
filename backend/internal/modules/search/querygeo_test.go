// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package search

import (
	"context"
	"encoding/json"
	"math"
	"strings"
	"testing"
)

// The bounding box must never exclude a row the circle would include.
//
// It is a pre-filter whose only job is to let the index narrow the candidates
// before the exact distance runs. Too wide costs a few extra comparisons; too
// narrow silently drops companies that ARE within the radius, and the answer
// looks complete.
func TestTheBoundingBoxNeverCutsInsideTheCircle(t *testing.T) {
	stuttgart := Point{Lat: 48.7758, Lon: 9.1829}
	for _, radiusKM := range []float64{1, 25, 50, 200, 1000} {
		minLat, maxLat, minLon, maxLon := boundingBox(stuttgart, radiusKM)
		// Sample the circle's edge all the way round. Every point exactly at
		// the radius must fall inside the box.
		for degrees := 0; degrees < 360; degrees += 5 {
			edge := destination(stuttgart, radiusKM, float64(degrees))
			if edge.Lat < minLat || edge.Lat > maxLat {
				t.Errorf("radius %vkm bearing %d°: latitude %v outside box [%v, %v]",
					radiusKM, degrees, edge.Lat, minLat, maxLat)
			}
			if edge.Lon < minLon || edge.Lon > maxLon {
				t.Errorf("radius %vkm bearing %d°: longitude %v outside box [%v, %v]",
					radiusKM, degrees, edge.Lon, minLon, maxLon)
			}
		}
	}
}

// Near a pole the longitude box widens without bound, so it is clamped to the
// whole range rather than producing nonsense. Rare, and wrong is worse than
// slow.
func TestTheBoundingBoxSurvivesThePoles(t *testing.T) {
	for _, lat := range []float64{89.9, -89.9, 90, -90} {
		_, _, minLon, maxLon := boundingBox(Point{Lat: lat, Lon: 0}, 100)
		if math.IsNaN(minLon) || math.IsInf(minLon, 0) || math.IsNaN(maxLon) || math.IsInf(maxLon, 0) {
			t.Errorf("latitude %v produced a longitude box [%v, %v]", lat, minLon, maxLon)
		}
		if maxLon-minLon < 359 {
			t.Errorf("latitude %v did not widen to the whole range: [%v, %v]", lat, minLon, maxLon)
		}
	}
}

// A record type that is not somewhere cannot be asked about by distance, and
// the answer says so rather than pretending.
func TestARecordTypeWithNoPlaceAnswersUnavailable(t *testing.T) {
	km := 50.0
	for _, target := range []string{"deal", "person", "lead", "project"} {
		bound, note, err := bindGeo(context.Background(), stubPlaces{},
			target, "address", radiusOperand{Center: "Stuttgart", RadiusKM: &km})
		if err != nil {
			t.Fatalf("%s: %v", target, err)
		}
		if bound != nil {
			t.Errorf("%s bound a radius predicate; only a company is somewhere", target)
		}
		if note == nil || note.Code != CodeDistanceRankingUnavailable {
			t.Errorf("%s did not answer the honest unavailable note: %+v", target, note)
		}
	}
}

// A place nothing has looked up answers a note, NOT an outbound request.
// query_workspace is declared workspace-local and the cap is derived from that
// declaration; reaching a geocoder here would make the declaration a lie.
func TestAnUnknownPlaceAnswersANoteRatherThanLeavingTheWorkspace(t *testing.T) {
	km := 50.0
	bound, note, err := bindGeo(context.Background(), stubPlaces{},
		"organization", "address", radiusOperand{Center: "Atlantis", RadiusKM: &km})
	if err != nil {
		t.Fatalf("binding: %v", err)
	}
	if bound != nil {
		t.Error("a place the cache does not hold was bound anyway")
	}
	if note == nil {
		t.Error("an unresolvable place produced no note, so the caller is told nothing")
	}
}

// Coordinates given directly need no cache at all: a caller holding a point has
// already done the resolving.
func TestAPointGivenDirectlyNeedsNoCache(t *testing.T) {
	km, lat, lon := 50.0, 48.7758, 9.1829
	bound, note, err := bindGeo(context.Background(), nil,
		"organization", "address", radiusOperand{Lat: &lat, Lon: &lon, RadiusKM: &km})
	if err != nil {
		t.Fatalf("binding: %v", err)
	}
	if note != nil {
		t.Errorf("an explicit point produced a note: %+v", note)
	}
	if bound == nil {
		t.Fatal("an explicit point did not bind")
	}
	if bound.Center.Lat != lat || bound.Center.Lon != lon {
		t.Errorf("center = %+v, want the point the caller gave", bound.Center)
	}
}

// The operand says WHERE exactly once. A name and a point together have two
// answers and no way to say which was meant; half a point is not a point.
func TestTheOperandNamesACenterExactlyOnce(t *testing.T) {
	lat, lon := 48.7758, 9.1829
	for name, operand := range map[string]radiusOperand{
		"neither":             {},
		"both name and point": {Center: "Stuttgart", Lat: &lat, Lon: &lon},
		"latitude alone":      {Lat: &lat},
		"longitude alone":     {Lon: &lon},
	} {
		if operand.namesACenter() {
			t.Errorf("%s was accepted as a center", name)
		}
	}
	if !(radiusOperand{Center: "Stuttgart"}).namesACenter() {
		t.Error("a place name alone was not accepted as a center")
	}
	if !(radiusOperand{Lat: &lat, Lon: &lon}).namesACenter() {
		t.Error("a point alone was not accepted as a center")
	}
}

// A center off the earth is refused: it is where a transposed lat/lon pair gets
// caught, and a transposition produces confidently wrong distances rather than
// an error anyone notices.
func TestACenterOffTheEarthIsRefused(t *testing.T) {
	bad, fine := 181.0, 9.1829
	if (radiusOperand{Lat: &bad, Lon: &fine}).plausibleCenter() {
		t.Error("a latitude of 181 was accepted")
	}
	lat := 48.7758
	if !(radiusOperand{Lat: &lat, Lon: &fine}).plausibleCenter() {
		t.Error("Stuttgart was rejected as implausible")
	}
}

// stubPlaces is a cache holding one city, so an unknown name is genuinely
// unknown rather than merely unwired.
type stubPlaces struct{}

func (stubPlaces) LookupPlace(_ context.Context, query string) (Point, bool, error) {
	if query == "Stuttgart" {
		return Point{Lat: 48.7758, Lon: 9.1829}, true, nil
	}
	return Point{}, false, nil
}

// destination walks radiusKM from a point along a bearing, so the box test has
// real circle-edge points to check rather than an approximation of them.
func destination(from Point, distanceKM, bearingDegrees float64) Point {
	angular := distanceKM / earthRadiusKM
	bearing := bearingDegrees * math.Pi / 180
	lat1 := from.Lat * math.Pi / 180
	lon1 := from.Lon * math.Pi / 180

	lat2 := math.Asin(math.Sin(lat1)*math.Cos(angular) +
		math.Cos(lat1)*math.Sin(angular)*math.Cos(bearing))
	lon2 := lon1 + math.Atan2(
		math.Sin(bearing)*math.Sin(angular)*math.Cos(lat1),
		math.Cos(angular)-math.Sin(lat1)*math.Sin(lat2))
	return Point{Lat: lat2 * 180 / math.Pi, Lon: lon2 * 180 / math.Pi}
}

// The SQL a radius compiles to must read geocode_status, not the coordinates
// alone.
//
// This is the whole staleness design showing up in one predicate. A company's
// coordinates belong to whatever address it held when the worker last ran, and
// an address can change at any time — so a query reading lat/lon without the
// status would answer a distance from where the company USED to be, and report
// success while doing it.
func TestARadiusOnlyReadsCoordinatesThatMatchTheAddress(t *testing.T) {
	c := &planCompiler{}
	distance, where := c.radius("t", geoBinding{
		Center:   Point{Lat: 48.7758, Lon: 9.1829},
		RadiusKM: 50,
		Columns:  geoCapableTargets["organization"],
		Field:    "address",
	})
	joined := strings.Join(where, " AND ")

	if !strings.Contains(joined, "geocode_status") {
		t.Errorf("the radius does not check geocode_status, so a moved company answers from its old "+
			"address:\n%s", joined)
	}
	if !strings.Contains(joined, "IS NOT NULL") {
		t.Errorf("the radius does not exclude null coordinates; a NULL in the haversine is silently "+
			"false rather than an error:\n%s", joined)
	}
	if !strings.Contains(joined, "BETWEEN") {
		t.Errorf("the radius has no bounding box, so every query is a full scan — the haversine "+
			"cannot use an index:\n%s", joined)
	}
	if distance == "" {
		t.Error("the radius produced no distance expression, so the answer cannot say how far")
	}
	if !strings.Contains(joined, distance) {
		t.Error("the distance expression is not used to decide membership; the box alone is wider " +
			"than the circle and would admit companies outside the radius")
	}
}

// Every value reaches SQL as a bound parameter. A centre is caller-supplied
// text, and a radius that interpolated it would be an injection door on a
// read tool.
func TestARadiusBindsEveryValueRatherThanInterpolatingIt(t *testing.T) {
	c := &planCompiler{}
	_, where := c.radius("t", geoBinding{
		Center:   Point{Lat: 48.7758, Lon: 9.1829},
		RadiusKM: 50,
		Columns:  geoCapableTargets["organization"],
	})
	joined := strings.Join(where, " AND ")
	if strings.Contains(joined, "48.7758") || strings.Contains(joined, "9.1829") {
		t.Errorf("a coordinate was interpolated into the SQL rather than bound:\n%s", joined)
	}
	if len(c.args) == 0 {
		t.Error("the radius bound no parameters at all")
	}
}

// A radius inside a TRAVERSAL refuses rather than being dropped.
//
// The root radius is rendered by planCompiler.radius and skipped by the
// ordinary clause loop. That same loop compiles a traversal's predicates, and
// there nothing binds a radius — so skipping it there would drop the predicate
// silently and return EVERY related record. A wider answer in the shape of the
// right one is the worst outcome available, and it is what this refusal
// prevents until hop binding exists.
func TestARadiusInsideATraversalRefusesRatherThanBeingDropped(t *testing.T) {
	c := &planCompiler{}
	km := 50.0
	operand, err := json.Marshal(radiusOperand{Center: "Stuttgart", RadiusKM: &km})
	if err != nil {
		t.Fatalf("building the operand: %v", err)
	}
	clauses := []Predicate{{Field: "address", Op: OpWithinRadius, Value: operand}}

	fragments, refusals := c.predicates("h", unfilteredStorage(),
		TargetVocabulary{Target: "organization"}, "traverse.where", clauses, false)
	if len(fragments) != 0 {
		t.Errorf("a hop radius compiled to %v; nothing binds it there", fragments)
	}
	if len(refusals) != 1 {
		t.Fatalf("a hop radius produced %d refusals; it must refuse exactly once rather than "+
			"pass silently and widen the answer", len(refusals))
	}
	if refusals[0].Code != CodeDistanceRankingUnavailable {
		t.Errorf("the refusal answers %q, want %q", refusals[0].Code, CodeDistanceRankingUnavailable)
	}

	// The ROOT is the opposite: skipped here on purpose, because radius()
	// renders it. Neither a fragment nor a refusal.
	fragments, refusals = c.predicates("t", unfilteredStorage(),
		TargetVocabulary{Target: "organization"}, "where", clauses, true)
	if len(fragments) != 0 || len(refusals) != 0 {
		t.Errorf("the root radius produced fragments %v and refusals %v; radius() renders it",
			fragments, refusals)
	}
}
