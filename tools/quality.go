package tools

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/katbyte/radarr-mcp/lib/radarr"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type profileRow struct {
	ID                int            `json:"id"`
	Name              string         `json:"name"`
	Cutoff            string         `json:"cutoff"                        jsonschema:"the quality Radarr stops upgrading at"`
	UpgradeAllowed    bool           `json:"upgrade_allowed"`
	Allowed           []string       `json:"allowed"                       jsonschema:"the qualities it accepts, best first"`
	MinFormatScore    int            `json:"min_format_score,omitempty"    jsonschema:"custom format score a release needs to be grabbed"`
	CutoffFormatScore int            `json:"cutoff_format_score,omitempty" jsonschema:"custom format score it stops upgrading at"`
	FormatScores      map[string]int `json:"format_scores,omitempty"       jsonschema:"the custom formats it scores, and their scores"`
	Movies            int            `json:"movies"                        jsonschema:"films on this profile"`
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
		ID: p.Id, Name: p.Name, Cutoff: cutoff, UpgradeAllowed: isTrue(p.UpgradeAllowed), Allowed: allowed,
		MinFormatScore: p.MinFormatScore, CutoffFormatScore: p.CutoffFormatScore, Movies: movies,
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
	res, err := c.GetQualitydefinition(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]definitionRow, 0, len(res.Model))
	for _, d := range res.Model {
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
	}
	add(r, readTool, &mcp.Tool{
		Name:        "qualityprofile_list",
		Description: "List the quality profiles films are held to: the qualities each allows (best first), the cutoff it stops upgrading at, whether it upgrades at all, the custom formats it scores, and how many films are on it.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ noArgs) (*mcp.CallToolResult, profileListOut, error) {
		res, err := client.GetQualityprofile(ctx)
		if err != nil {
			return nil, profileListOut{}, err
		}
		movies, err := allMovies(ctx, client)
		if err != nil {
			return nil, profileListOut{}, err
		}
		counts := map[int]int{}
		for i := range movies {
			m := &movies[i]
			counts[m.QualityProfileId]++
		}
		out := profileListOut{}
		for i := range res.Model {
			out.Profiles = append(out.Profiles, profileOf(&res.Model[i], counts[res.Model[i].Id]))
		}

		return nil, out, nil
	})

	type profileEditIn struct {
		Profile           string `json:"profile"                       jsonschema:"the quality profile: its name or id"`
		UpgradeAllowed    *bool  `json:"upgrade_allowed,omitempty"`
		Cutoff            string `json:"cutoff,omitempty"              jsonschema:"the quality (or quality group) to stop upgrading at; it must be one the profile allows"`
		MinFormatScore    *int   `json:"min_format_score,omitempty"`
		CutoffFormatScore *int   `json:"cutoff_format_score,omitempty"`
	}
	add(r, writeTool, &mcp.Tool{
		Name: "qualityprofile_edit",
		Description: "Change a quality profile: whether it upgrades, the quality it stops upgrading at, and the custom format scores a release needs to be grabbed and to stop upgrading. " +
			"Every film on the profile is held to the change.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in profileEditIn) (*mcp.CallToolResult, profileRow, error) {
		p, err := resolveProfile(ctx, client, in.Profile)
		if err != nil {
			return nil, profileRow{}, err
		}
		if in.UpgradeAllowed == nil && in.Cutoff == "" && in.MinFormatScore == nil && in.CutoffFormatScore == nil {
			return nil, profileRow{}, errors.New("nothing to change: give upgrade_allowed, cutoff, min_format_score or cutoff_format_score")
		}
		edit := *p
		if in.UpgradeAllowed != nil {
			edit.UpgradeAllowed = in.UpgradeAllowed
		}
		if in.Cutoff != "" {
			if edit.Cutoff, err = cutoffID(p, in.Cutoff); err != nil {
				return nil, profileRow{}, err
			}
		}
		if in.MinFormatScore != nil {
			edit.MinFormatScore = *in.MinFormatScore
		}
		if in.CutoffFormatScore != nil {
			edit.CutoffFormatScore = *in.CutoffFormatScore
		}
		res, err := client.PutQualityprofileById(ctx, p.Id, edit)
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

		return nil, profileOf(res.Model, n), nil
	})

	type definitionListOut struct {
		Definitions []definitionRow `json:"definitions" jsonschema:"worst quality first"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "qualitydefinition_list",
		Description: "List the qualities Radarr knows, worst first, with the size limits each allows in MB per minute of runtime: a release outside them is not grabbed, and audit_size finds files outside them.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ noArgs) (*mcp.CallToolResult, definitionListOut, error) {
		rows, err := definitions(ctx, client)

		return nil, definitionListOut{Definitions: rows}, err
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
		current, err := client.GetQualitydefinitionById(ctx, d.ID)
		if err != nil {
			return nil, definitionRow{}, err
		}
		// a nil maximum or preference is Radarr's unlimited and none: the
		// generated model leaves it out, which Radarr reads as null
		edit := *current.Model
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
		if _, err := client.PutQualitydefinitionById(ctx, d.ID, edit); err != nil {
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
	}
	add(r, readTool, &mcp.Tool{
		Name:        "customformat_list",
		Description: "List the custom formats Radarr scores releases and files by (HDR, a release group, a codec, an edition), what each matches, and the score each quality profile gives it.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ noArgs) (*mcp.CallToolResult, formatListOut, error) {
		formats, err := client.GetCustomformat(ctx)
		if err != nil {
			return nil, formatListOut{}, err
		}
		profiles, err := client.GetQualityprofile(ctx)
		if err != nil {
			return nil, formatListOut{}, err
		}
		out := formatListOut{}
		for _, f := range formats.Model {
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
			for _, p := range profiles.Model {
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
