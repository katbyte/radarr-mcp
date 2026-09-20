package tools

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/katbyte/radarr-mcp/lib/radarr"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type calendarRow struct {
	movieRow

	Releases []string `json:"releases" jsonschema:"the releases that fall in the window: in cinemas, digital or physical, each with its date"`
}

func registerCalendarTools(r *registry) {
	client := r.client

	type calendarIn struct {
		DaysBack    int  `json:"days_back,omitempty"   jsonschema:"days before today to include, default 7"`
		DaysAhead   int  `json:"days_ahead,omitempty"  jsonschema:"days after today to include, default 30"`
		Unmonitored bool `json:"unmonitored,omitempty" jsonschema:"include unmonitored films"`
	}
	type calendarOut struct {
		Start  string        `json:"start"`
		End    string        `json:"end"`
		Movies []calendarRow `json:"movies" jsonschema:"soonest first"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "calendar_list",
		Description: "List the library's films released in cinemas, digitally or on disc in a window around today (default the last week and the next month), soonest first: what Radarr is about to start looking for.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in calendarIn) (*mcp.CallToolResult, calendarOut, error) {
		now := time.Now().UTC()
		start := now.AddDate(0, 0, -limitOr(in.DaysBack, 7)).Truncate(24 * time.Hour)
		end := now.AddDate(0, 0, limitOr(in.DaysAhead, 30)).Truncate(24 * time.Hour).Add(24 * time.Hour)
		res, err := client.GetCalendar(ctx, radarr.GetCalendarOperationOptions{
			Start: start.Format(time.RFC3339), End: end.Format(time.RFC3339), Unmonitored: new(in.Unmonitored),
		})
		if err != nil {
			return nil, calendarOut{}, err
		}
		lk, err := loadLookups(ctx, client)
		if err != nil {
			return nil, calendarOut{}, err
		}
		out := calendarOut{Start: start.Format(time.DateOnly), End: end.Format(time.DateOnly)}
		within := func(stamp string) bool {
			t, err := time.Parse(time.RFC3339, stamp)
			return err == nil && !t.Before(start) && t.Before(end)
		}
		for i := range res.Model {
			m := &res.Model[i]
			row := calendarRow{movieRow: rowOf(m, lk)}
			for _, rel := range []struct{ kind, date string }{{"in cinemas", m.InCinemas}, {"digital", m.DigitalRelease}, {"physical", m.PhysicalRelease}} {
				if within(rel.date) {
					row.Releases = append(row.Releases, rel.kind+" "+rel.date[:10])
				}
			}
			out.Movies = append(out.Movies, row)
		}
		slices.SortStableFunc(out.Movies, func(a, b calendarRow) int { return strings.Compare(firstDate(a.Releases), firstDate(b.Releases)) })

		return nil, out, nil
	})
}

// firstDate is the earliest date in a calendar row's releases, for sorting.
func firstDate(releases []string) string {
	first := "9999"
	for _, r := range releases {
		if i := strings.LastIndexByte(r, ' '); i >= 0 && r[i+1:] < first {
			first = r[i+1:]
		}
	}

	return first
}
