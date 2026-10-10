//go:build integration

package acceptance

import (
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	acc "github.com/katbyte/go-kt/mcp/acctest"
)

// libraryCollection is one collection_list row by title.
func libraryCollection(t *testing.T, args map[string]any) map[string]map[string]any {
	t.Helper()

	out := map[string]map[string]any{}
	for _, c := range acc.Rows(t, suite.Call(t, "collection_list", args)["collections"], "collections") {
		out[acc.Str(c["title"])] = c
	}

	return out
}

// libraryMissing is a collection row's missing films as "Title (Year)".
func libraryMissing(t *testing.T, c map[string]any) []string {
	t.Helper()

	var out []string
	for _, m := range acc.Rows(t, c["missing"], "missing") {
		out = append(out, titleYear(acc.Str(m["title"]), acc.NumOr0(m["year"])))
	}

	return out
}

// The collections are what TMDB says the fixture films belong to: each with
// the films the library holds, in release order, and the released ones it
// does not; the unreleased ones only when asked for, and missing_only
// narrows the list to the collections with gaps.
func TestCollections(t *testing.T) {
	cols := libraryCollection(t, nil)
	var titles []string
	for title := range cols {
		titles = append(titles, title)
	}
	slices.Sort(titles)
	if !slices.Equal(titles, []string{"Alien Collection", "Blade Runner Collection", "Dune Collection", "Heat Collection", "The Matrix Collection"}) {
		t.Fatalf("collections = %v", titles)
	}

	for title, want := range map[string]struct {
		held, missing []string
		root          string
	}{
		"Alien Collection":        {[]string{"Alien (1979)", "Aliens (1986)", "Alien³ (1992)", "Alien Resurrection (1997)"}, nil, moviesRoot},
		"Blade Runner Collection": {[]string{"Blade Runner (1982)", "Blade Runner 2049 (2017)"}, nil, moviesRoot},
		"Dune Collection":         {[]string{"Dune (2021)", "Dune: Part Two (2024)"}, nil, moviesRoot},
		"Heat Collection":         {[]string{"Heat (1995)"}, nil, messyRoot},
		"The Matrix Collection":   {[]string{"The Matrix (1999)"}, []string{"The Matrix Reloaded (2003)", "The Matrix Revolutions (2003)", "The Matrix Resurrections (2021)"}, messyRoot},
	} {
		c := cols[title]
		if got := acc.Strs(t, acc.OrEmptyList(c["held"]), "held"); !slices.Equal(got, want.held) {
			t.Errorf("%s holds %v, want %v", title, got, want.held)
		}
		if got := libraryMissing(t, c); !slices.Equal(got, want.missing) {
			t.Errorf("%s is missing %v, want %v", title, got, want.missing)
		}
		// a collection takes its settings from the first film added to it
		if c["monitored"] != false || c["search_on_add"] != false || acc.Str(c["quality_profile"]) != profileHD || acc.Str(c["root_folder"]) != want.root || acc.NumOr0(c["tmdb_id"]) == 0 {
			t.Errorf("%s = %v", title, c)
		}
	}
	for _, m := range acc.Rows(t, cols["The Matrix Collection"]["missing"], "missing") {
		if acc.Str(m["status"]) != "released" || acc.NumOr0(m["tmdb_id"]) == 0 || m["excluded"] != nil {
			t.Errorf("a missing Matrix film = %v", m)
		}
	}

	// the announced films, when asked for, last in their collection
	all := libraryCollection(t, map[string]any{"include_unreleased": true})
	if got := libraryMissing(t, all["Dune Collection"]); !slices.Equal(got, []string{"Dune: Part Three (2026)"}) {
		t.Errorf("Dune's unreleased = %v", got)
	}
	if got := libraryMissing(t, all["Heat Collection"]); !slices.Equal(got, []string{"Heat 2"}) {
		t.Errorf("Heat's unreleased = %v", got)
	}
	if m := acc.Rows(t, all["Heat Collection"]["missing"], "missing"); acc.Str(m[0]["status"]) != "announced" {
		t.Errorf("Heat 2 = %v, want announced", m[0])
	}

	for args, want := range map[string][]string{
		"released":   {"The Matrix Collection"},
		"unreleased": {"Dune Collection", "Heat Collection", "The Matrix Collection"},
	} {
		var got []string
		for title := range libraryCollection(t, map[string]any{"missing_only": true, "include_unreleased": args == "unreleased"}) {
			got = append(got, title)
		}
		slices.Sort(got)
		if !slices.Equal(got, want) {
			t.Errorf("missing_only (%s) = %v, want %v", args, got, want)
		}
	}
}

// collection_edit changes a collection's settings and nothing else - no film
// is added - and names it by title, "title Collection", tmdb:<id> or id. It
// is not asked to monitor: a monitored collection has Radarr add its missing
// films, which is Radarr's job to test, not a fixture to disturb.
func TestCollectionEdit(t *testing.T) {
	matrix := libraryCollection(t, nil)["The Matrix Collection"]
	id := acc.Num(t, matrix["id"], "id")
	path := "/api/v3/collection/" + strconv.Itoa(id)
	before := settingsRaw(t, path)
	films := acc.Num(t, suite.Call(t, "movie_list", map[string]any{"limit": 1})["total"], "total")
	t.Cleanup(func() {
		suite.Call(t, "collection_edit", map[string]any{"collection": "tmdb:2344", "quality_profile": profileHD, "root_folder": messyRoot, "minimum_availability": "released"})
		settingsUnchanged(t, path, before)
	})

	out := suite.Call(t, "collection_edit", map[string]any{
		"collection": "The Matrix", "quality_profile": profileUHD, "root_folder": moviesRoot, "minimum_availability": "announced", "search_on_add": false, "monitored": false,
	})
	if acc.Str(out["title"]) != "The Matrix Collection" || acc.Str(out["quality_profile"]) != profileUHD || acc.Str(out["root_folder"]) != moviesRoot ||
		out["monitored"] != false || out["search_on_add"] != false || len(acc.Strs(t, acc.OrEmptyList(out["held"]), "held")) != 1 {
		t.Errorf("collection_edit = %v", out)
	}
	raw := acc.Object(t, settingsRaw(t, path), "collection")
	if acc.NumOr0(raw["qualityProfileId"]) != 5 || acc.Str(raw["minimumAvailability"]) != "announced" || acc.Str(raw["rootFolderPath"]) != moviesRoot {
		t.Errorf("Radarr holds %v", raw)
	}
	if got := acc.Num(t, suite.Call(t, "movie_list", map[string]any{"limit": 1})["total"], "total"); got != films {
		t.Errorf("the library went from %d films to %d on a collection edit", films, got)
	}

	// every way of naming it reaches the same collection
	for _, ref := range []string{strconv.Itoa(id), "tmdb:2344", "the matrix collection"} {
		if got := suite.Call(t, "collection_edit", map[string]any{"collection": ref, "search_on_add": false}); acc.Num(t, got["id"], "id") != id {
			t.Errorf("%q named collection %v", ref, got["id"])
		}
	}

	if msg := suite.CallErr(t, "collection_edit", map[string]any{"collection": "nope", "monitored": false}); !strings.Contains(msg, `no collection "nope"; the collections are Alien Collection, Blade Runner Collection`) {
		t.Errorf("an unknown collection = %s", msg)
	}
	if msg := suite.CallErr(t, "collection_edit", map[string]any{"collection": "The Matrix", "minimum_availability": "soon"}); !strings.Contains(msg, `minimum availability "soon": want announced, inCinemas or released`) {
		t.Errorf("an unknown availability = %s", msg)
	}
	if msg := suite.CallErr(t, "collection_edit", map[string]any{"collection": "The Matrix", "root_folder": "/media/nope"}); !strings.Contains(msg, `no root folder "/media/nope"`) {
		t.Errorf("an unknown root folder = %s", msg)
	}
}

// An exclusion's life: added by tmdb id for a film the library does not
// hold (looked up on TMDB) or by the title of one it does, listed, marked on
// the collection it belongs to, refused twice, and removed.
func TestExclusions(t *testing.T) {
	exclusions := func() map[int]map[string]any {
		out := map[int]map[string]any{}
		for _, e := range acc.Rows(t, suite.Call(t, "exclusion_list", nil)["exclusions"], "exclusions") {
			out[acc.Num(t, e["tmdb_id"], "tmdb_id")] = e
		}
		return out
	}
	ours := []int{17431, 604, 782}
	for _, tmdb := range ours {
		if exclusions()[tmdb] != nil {
			t.Fatalf("tmdb %d is excluded before the test", tmdb)
		}
	}
	t.Cleanup(func() {
		for _, tmdb := range ours {
			if e := exclusions()[tmdb]; e != nil {
				_, _ = sdk.DeleteExclusionsById(ctx, acc.Num(t, e["id"], "id"))
			}
		}
	})

	moon := suite.Call(t, "exclusion_add", map[string]any{"movie": "tmdb:17431"})
	if acc.Num(t, moon["tmdb_id"], "tmdb_id") != 17431 || acc.Str(moon["title"]) != "Moon" || acc.Num(t, moon["year"], "year") != 2009 || acc.NumOr0(moon["id"]) == 0 {
		t.Errorf("exclusion_add Moon = %v", moon)
	}
	msg := suite.CallErr(t, "exclusion_add", map[string]any{"movie": "tmdb:17431"})
	if !strings.Contains(msg, "Moon (2009) is already excluded (id "+strconv.Itoa(acc.Num(t, moon["id"], "id"))+")") {
		t.Errorf("excluding Moon twice = %s", msg)
	}
	reloaded := suite.Call(t, "exclusion_add", map[string]any{"movie": "tmdb:604"})
	gattaca := suite.Call(t, "exclusion_add", map[string]any{"movie": messyGattaca})
	if acc.Str(reloaded["title"]) != "The Matrix Reloaded" || acc.Num(t, gattaca["tmdb_id"], "tmdb_id") != 782 {
		t.Errorf("exclusion_add = %v and %v", reloaded, gattaca)
	}

	list := acc.Rows(t, suite.Call(t, "exclusion_list", nil)["exclusions"], "exclusions")
	var titles []string
	for _, e := range list {
		titles = append(titles, acc.Str(e["title"]))
	}
	if !slices.IsSorted(titles) || !slices.Contains(titles, "Moon") || !slices.Contains(titles, "The Matrix Reloaded") || !slices.Contains(titles, "Gattaca") {
		t.Errorf("exclusion_list = %v, want the three, sorted", titles)
	}

	// a collection's missing film on the exclusions is marked so, and still
	// listed
	for _, m := range acc.Rows(t, libraryCollection(t, nil)["The Matrix Collection"]["missing"], "missing") {
		if (acc.Str(m["title"]) == "The Matrix Reloaded") != (m["excluded"] == true) {
			t.Errorf("a missing Matrix film = %v", m)
		}
	}

	ids := []any{moon["id"], reloaded["id"], gattaca["id"]}
	removed := acc.Rows(t, suite.Call(t, "exclusion_remove", map[string]any{"ids": ids})["removed"], "removed")
	var gone []string
	for _, e := range removed {
		gone = append(gone, acc.Str(e["title"]))
	}
	if !slices.Equal(gone, []string{"Moon", "The Matrix Reloaded", "Gattaca"}) {
		t.Errorf("exclusion_remove removed %v", gone)
	}
	left := exclusions()
	for _, tmdb := range ours {
		if left[tmdb] != nil {
			t.Errorf("tmdb %d is still excluded", tmdb)
		}
	}

	if msg := suite.CallErr(t, "exclusion_remove", map[string]any{"ids": []any{}}); !strings.Contains(msg, "no exclusion ids") {
		t.Errorf("removing nothing = %s", msg)
	}
	// Radarr answers a delete of an exclusion that is not there with a 200,
	// so the tool checks, rather than reporting it removed
	if msg := suite.CallErr(t, "exclusion_remove", map[string]any{"ids": []any{999999}}); !strings.Contains(msg, "no exclusion 999999; exclusion_list lists them") {
		t.Errorf("removing an exclusion that is not there = %s", msg)
	}
}

// libraryDaysSince is how many days back calendar_list must reach for its
// window to start on a date.
func libraryDaysSince(date string) int {
	d, err := time.Parse(time.DateOnly, date)
	if err != nil {
		panic(err)
	}

	return int(time.Since(d).Hours() / 24)
}

// libraryCalendar is calendar_list's films, title to releases, in its order.
func libraryCalendar(t *testing.T, args map[string]any) ([]string, map[string][]string, map[string]any) {
	t.Helper()

	out := suite.Call(t, "calendar_list", args)
	var order []string
	releases := map[string][]string{}
	for _, m := range acc.Rows(t, out["movies"], "movies") {
		title := titleYear(acc.Str(m["title"]), acc.NumOr0(m["year"]))
		order = append(order, title)
		releases[title] = acc.Strs(t, acc.OrEmptyList(m["releases"]), "releases")
	}

	return order, releases, out
}

// calendar_list lists the films released in its window, with the releases
// that fall in it, soonest first: nothing in the days around today (the
// fixture films are years old), exactly the films whose dates fall in a
// window reaching back to them, and the unmonitored ones only when asked.
func TestCalendar(t *testing.T) {
	now, _, out := libraryCalendar(t, nil)
	if len(now) != 0 {
		t.Errorf("the week around today holds %v; the fixture films are years old", now)
	}
	if acc.Str(out["start"]) != time.Now().UTC().AddDate(0, 0, -7).Format(time.DateOnly) || acc.Str(out["end"]) == "" {
		t.Errorf("the default window = %v to %v", out["start"], out["end"])
	}

	order, releases, out := libraryCalendar(t, map[string]any{"days_back": libraryDaysSince("2024-01-01")})
	if acc.Str(out["start"]) != "2024-01-01" || !slices.Equal(order, []string{"Dune: Part Two (2024)"}) {
		t.Fatalf("since 2024 = %v from %v", order, out["start"])
	}
	if got := releases["Dune: Part Two (2024)"]; !slices.Equal(got, []string{"in cinemas 2024-02-27", "digital 2024-02-06", "physical 2024-05-14"}) {
		t.Errorf("Dune: Part Two's releases = %v", got)
	}

	// reaching back to September 2022 takes in The Thirteenth Floor's digital
	// release, and orders the films by their first date in the window
	order, releases, _ = libraryCalendar(t, map[string]any{"days_back": libraryDaysSince("2022-09-01"), "days_ahead": 1})
	if !slices.Equal(order, []string{"The Thirteenth Floor (1999)", "Dune: Part Two (2024)"}) || !slices.Equal(releases["The Thirteenth Floor (1999)"], []string{"digital 2022-09-10"}) {
		t.Errorf("since September 2022 = %v, %v", order, releases)
	}

	// Alien Resurrection is the one unmonitored film, and the only difference
	back := libraryDaysSince("1997-11-01")
	monitored, _, _ := libraryCalendar(t, map[string]any{"days_back": back})
	everything, releases, _ := libraryCalendar(t, map[string]any{"days_back": back, "unmonitored": true})
	var extra []string
	for _, title := range everything {
		if !slices.Contains(monitored, title) {
			extra = append(extra, title)
		}
	}
	if !slices.Equal(extra, []string{"Alien Resurrection (1997)"}) || len(everything) != len(monitored)+1 || !slices.Contains(releases["Alien Resurrection (1997)"], "in cinemas 1997-11-12") {
		t.Errorf("unmonitored added %v (%d against %d), releases %v", extra, len(everything), len(monitored), releases["Alien Resurrection (1997)"])
	}
}
