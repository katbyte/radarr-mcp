package tools

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/katbyte/radarr-mcp/sdk/radarr"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type profileRow struct {
	ID                    int            `json:"id"`
	Name                  string         `json:"name"`
	Cutoff                string         `json:"cutoff"                             jsonschema:"the quality Radarr stops upgrading at"`
	UpgradeAllowed        bool           `json:"upgrade_allowed"`
	Allowed               []string       `json:"allowed"                            jsonschema:"the qualities it accepts, best first"`
	MinFormatScore        int            `json:"min_format_score,omitempty"         jsonschema:"custom format score a release needs to be grabbed"`
	MinUpgradeFormatScore int            `json:"min_upgrade_format_score,omitempty" jsonschema:"how much higher a release's custom format score must be to replace a file"`
	CutoffFormatScore     int            `json:"cutoff_format_score,omitempty"      jsonschema:"custom format score it stops upgrading at"`
	FormatScores          map[string]int `json:"format_scores,omitempty"            jsonschema:"the custom formats it scores, and their scores"`
	Movies                int            `json:"movies"                             jsonschema:"films on this profile"`
}

// profileQualities flattens a profile's items into the qualities it allows,
// best first (Radarr lists them worst first), and finds the name of its
// cutoff, which may be a single quality or a group.
func profileQualities(p *radarr.QualityProfileResource) (allowed []string, cutoff string) {
	for _, item := range p.Items {
		name := item.Name
		if item.Quality != nil {
			name = item.Quality.Name
		}
		if item.Id == p.Cutoff && item.Quality == nil || item.Quality != nil && item.Quality.Id == p.Cutoff {
			cutoff = name
		}
		if !isTrue(item.Allowed) {
			continue
		}
		if item.Quality != nil {
			allowed = append(allowed, item.Quality.Name)
			continue
		}
		for _, child := range item.Items {
			if child.Quality != nil {
				allowed = append(allowed, child.Quality.Name)
			}
		}
	}
	slices.Reverse(allowed)

	return allowed, cutoff
}

func profileOf(p *radarr.QualityProfileResource, movies int) profileRow {
	allowed, cutoff := profileQualities(p)
	row := profileRow{
		ID: int(p.Id), Name: p.Name, Cutoff: cutoff, UpgradeAllowed: isTrue(p.UpgradeAllowed), Allowed: allowed,
		MinFormatScore: p.MinFormatScore, MinUpgradeFormatScore: p.MinUpgradeFormatScore, CutoffFormatScore: p.CutoffFormatScore, Movies: movies,
	}
	for _, f := range p.FormatItems {
		if f.Score != 0 {
			if row.FormatScores == nil {
				row.FormatScores = map[string]int{}
			}
			row.FormatScores[f.Name] = f.Score
		}
	}

	return row
}

type definitionRow struct {
	ID            int      `json:"id"`
	Quality       string   `json:"quality"`
	Title         string   `json:"title"                    jsonschema:"what Radarr's settings call it"`
	MinSize       float64  `json:"min_size"                 jsonschema:"MB per minute of runtime"`
	PreferredSize *float64 `json:"preferred_size,omitempty" jsonschema:"MB per minute; absent is no preference"`
	MaxSize       *float64 `json:"max_size,omitempty"       jsonschema:"MB per minute; absent is unlimited"`
}

// definitions reads the quality definitions, worst first.
func definitions(ctx context.Context, c *radarr.Client) ([]definitionRow, error) {
	res, err := c.GetQualityDefinition(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]definitionRow, 0, len(res.Answer))
	for _, d := range res.Answer {
		row := definitionRow{ID: d.Id, Title: d.Title, MinSize: val(d.MinSize), PreferredSize: d.PreferredSize, MaxSize: d.MaxSize}
		if d.Quality != nil {
			row.Quality = d.Quality.Name
		}
		out = append(out, row)
	}

	return out, nil
}

func registerQualityTools(r *registry) {
	client := r.client

	type profileListOut struct {
		Profiles []profileRow `json:"profiles"`
		pageOut
	}
	add(r, readTool, &mcp.Tool{
		Name:        "qualityprofile_list",
		Description: "List the quality profiles films are held to: the qualities each allows (best first), the cutoff it stops upgrading at, whether it upgrades at all, the custom formats it scores, and how many films are on it.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in pageIn) (*mcp.CallToolResult, profileListOut, error) {
		res, err := client.GetQualityProfile(ctx)
		if err != nil {
			return nil, profileListOut{}, err
		}
		movies, err := allMovies(ctx, client)
		if err != nil {
			return nil, profileListOut{}, err
		}
		counts := map[radarr.QualityProfileID]int{}
		for i := range movies {
			m := &movies[i]
			counts[m.QualityProfileId]++
		}
		page, paged := pageRows(res.Answer, in)
		out := profileListOut{Profiles: make([]profileRow, 0, len(page)), pageOut: paged}
		for i := range page {
			out.Profiles = append(out.Profiles, profileOf(&page[i], counts[page[i].Id]))
		}

		return nil, out, nil
	})

	type profileEditIn struct {
		Profile string `json:"profile"            jsonschema:"the quality profile: its name or id"`
		NewName string `json:"new_name,omitempty" jsonschema:"rename it"`
		profileChanges
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "qualityprofile_edit",
		Description: "Change a quality profile: its name, whether it upgrades, the qualities it accepts, the quality it stops upgrading at, the score it gives each custom format, and the custom format scores a release needs to be grabbed, to replace a file and to stop upgrading. Every film on the profile is held to the change.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in profileEditIn) (*mcp.CallToolResult, profileRow, error) {
		p, err := resolveProfile(ctx, client, in.Profile)
		if err != nil {
			return nil, profileRow{}, err
		}
		if in.NewName == "" && in.none() {
			return nil, profileRow{}, errors.New("nothing to change: give new_name, upgrade_allowed, allow, disallow, cutoff, format_scores, min_format_score, min_upgrade_format_score or cutoff_format_score")
		}

		// the profile is sent back whole, so it is read again once it is held: another edit of it in this turn may have changed it since it was found
		unlock := r.locking().By(profileLock(p.Id))
		defer unlock()
		held, err := client.GetQualityProfileById(ctx, p.Id)
		if err != nil {
			return nil, profileRow{}, err
		}
		if held.Answer == nil {
			return nil, profileRow{}, fmt.Errorf("no answer from Radarr when asked for quality profile %d", p.Id)
		}
		p = held.Answer

		edit := *p
		if name := strings.TrimSpace(in.NewName); name != "" {
			edit.Name = name
		}
		if err := in.apply(&edit); err != nil {
			return nil, profileRow{}, err
		}
		res, err := client.PutQualityProfileById(ctx, p.Id, edit)
		if err != nil {
			return nil, profileRow{}, err
		}
		movies, err := allMovies(ctx, client)
		if err != nil {
			return nil, profileRow{}, err
		}
		n := 0
		for i := range movies {
			m := &movies[i]
			if m.QualityProfileId == p.Id {
				n++
			}
		}

		return nil, profileOf(res.Answer, n), nil
	})

	type definitionListOut struct {
		Definitions []definitionRow `json:"definitions" jsonschema:"worst quality first"`
		pageOut
	}
	add(r, readTool, &mcp.Tool{
		Name:        "qualitydefinition_list",
		Description: "List the qualities Radarr knows, worst first, with the size limits each allows in MB per minute of runtime: a release outside them is not grabbed, and audit_size finds files outside them.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in pageIn) (*mcp.CallToolResult, definitionListOut, error) {
		rows, err := definitions(ctx, client)
		if err != nil {
			return nil, definitionListOut{}, err
		}

		out := definitionListOut{}
		out.Definitions, out.pageOut = pageRows(rows, in)

		return nil, out, nil
	})

	type definitionEditIn struct {
		Quality       string   `json:"quality"                  jsonschema:"the quality, as qualitydefinition_list names it"`
		MinSize       *float64 `json:"min_size,omitempty"       jsonschema:"MB per minute"`
		PreferredSize *float64 `json:"preferred_size,omitempty" jsonschema:"MB per minute"`
		MaxSize       *float64 `json:"max_size,omitempty"       jsonschema:"MB per minute; 0 is unlimited"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "qualitydefinition_edit",
		Description: "Change the size limits of a quality, in MB per minute of runtime: raise the minimum to stop Radarr grabbing tiny fakes of it, lower the maximum to stop it grabbing bloated ones. Files already in the library are not touched.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in definitionEditIn) (*mcp.CallToolResult, definitionRow, error) {
		if in.MinSize == nil && in.PreferredSize == nil && in.MaxSize == nil {
			return nil, definitionRow{}, errors.New("nothing to change: give min_size, preferred_size or max_size")
		}
		rows, err := definitions(ctx, client)
		if err != nil {
			return nil, definitionRow{}, err
		}
		i := slices.IndexFunc(rows, func(d definitionRow) bool { return strings.EqualFold(d.Quality, strings.TrimSpace(in.Quality)) })
		if i < 0 {
			names := make([]string, 0, len(rows))
			for _, d := range rows {
				names = append(names, d.Quality)
			}
			return nil, definitionRow{}, fmt.Errorf("no quality %q; the qualities are %s", in.Quality, strings.Join(names, ", "))
		}
		d := rows[i]

		// the limits are sent back whole, so they are read once the quality is held
		unlock := r.locking().By(definitionLock(d.ID))
		defer unlock()
		current, err := client.GetQualityDefinitionById(ctx, d.ID)
		if err != nil {
			return nil, definitionRow{}, err
		}
		if current.Answer == nil {
			return nil, definitionRow{}, fmt.Errorf("no answer from Radarr when asked for quality definition %d", d.ID)
		}

		// a nil maximum or preference is Radarr's unlimited and none: the
		// generated model leaves it out, which Radarr reads as null
		edit := *current.Answer
		if in.MinSize != nil {
			edit.MinSize = in.MinSize
		}
		if in.PreferredSize != nil {
			edit.PreferredSize = in.PreferredSize
		}
		if in.MaxSize != nil {
			edit.MaxSize = in.MaxSize
			if *in.MaxSize <= 0 {
				edit.MaxSize = nil
			}
		}
		if _, err := client.PutQualityDefinitionById(ctx, d.ID, edit); err != nil {
			return nil, definitionRow{}, err
		}
		after, err := definitions(ctx, client)
		if err != nil {
			return nil, definitionRow{}, err
		}

		return nil, after[i], nil
	})

	type formatRow struct {
		ID             int            `json:"id"`
		Name           string         `json:"name"`
		Specifications []string       `json:"specifications"   jsonschema:"what a release must (or must not) match, one line each"`
		Scores         map[string]int `json:"scores,omitempty" jsonschema:"the score each quality profile gives it"`
	}
	type formatListOut struct {
		Formats []formatRow `json:"formats"`
		pageOut
	}
	add(r, readTool, &mcp.Tool{
		Name:        "customformat_list",
		Description: "List the custom formats Radarr scores releases and files by (HDR, a release group, a codec, an edition), what each matches, and the score each quality profile gives it.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in pageIn) (*mcp.CallToolResult, formatListOut, error) {
		formats, err := client.GetCustomFormat(ctx)
		if err != nil {
			return nil, formatListOut{}, err
		}
		profiles, err := client.GetQualityProfile(ctx)
		if err != nil {
			return nil, formatListOut{}, err
		}
		page, paged := pageRows(formats.Answer, in)
		out := formatListOut{Formats: make([]formatRow, 0, len(page)), pageOut: paged}
		for _, f := range page {
			row := formatRow{ID: f.Id, Name: f.Name}
			for _, s := range f.Specifications {
				line := s.Implementation + " " + strconv.Quote(s.Name)
				for _, field := range s.Fields {
					if field.Name == "value" && field.Value != nil {
						line = fmt.Sprintf("%s = %v", line, field.Value)
					}
				}
				if isTrue(s.Negate) {
					line = "not " + line
				}
				if isTrue(s.Required) {
					line += " (required)"
				}
				row.Specifications = append(row.Specifications, line)
			}
			for _, p := range profiles.Answer {
				for _, fi := range p.FormatItems {
					if fi.Format == f.Id && fi.Score != 0 {
						if row.Scores == nil {
							row.Scores = map[string]int{}
						}
						row.Scores[p.Name] = fi.Score
					}
				}
			}
			out.Formats = append(out.Formats, row)
		}

		return nil, out, nil
	})
}

// cutoffID finds the id of a quality or quality group a profile allows, by
// name.
func cutoffID(p *radarr.QualityProfileResource, name string) (int, error) {
	var names []string
	for _, item := range p.Items {
		if !isTrue(item.Allowed) {
			continue
		}
		if item.Quality != nil {
			if strings.EqualFold(item.Quality.Name, name) {
				return item.Quality.Id, nil
			}
			names = append(names, item.Quality.Name)
			continue
		}
		if strings.EqualFold(item.Name, name) {
			return item.Id, nil
		}
		names = append(names, item.Name)
	}

	return 0, fmt.Errorf("%s does not allow %q, so it cannot be the cutoff; it allows %s", p.Name, name, strings.Join(names, ", "))
}
