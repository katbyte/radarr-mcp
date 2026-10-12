package tools

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/katbyte/radarr-mcp/sdk/radarr"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// profileChanges are what an add or an edit sets on a quality profile.
type profileChanges struct {
	UpgradeAllowed        *bool          `json:"upgrade_allowed,omitempty"`
	Allow                 []string       `json:"allow,omitempty"                    jsonschema:"qualities (or quality groups) to accept as well, by name, as qualityprofile_list names them"`
	Disallow              []string       `json:"disallow,omitempty"                 jsonschema:"qualities (or quality groups) to stop accepting"`
	Cutoff                string         `json:"cutoff,omitempty"                   jsonschema:"the quality (or quality group) to stop upgrading at; it must be one the profile allows"`
	FormatScores          map[string]int `json:"format_scores,omitempty"            jsonschema:"custom formats to score, by name, each with its score; 0 stops scoring one"`
	MinFormatScore        *int           `json:"min_format_score,omitempty"         jsonschema:"custom format score a release needs to be grabbed"`
	MinUpgradeFormatScore *int           `json:"min_upgrade_format_score,omitempty" jsonschema:"how much higher a release's custom format score must be to replace a file"`
	CutoffFormatScore     *int           `json:"cutoff_format_score,omitempty"      jsonschema:"custom format score it stops upgrading at"`
}

// none reports whether nothing was asked for.
func (ch *profileChanges) none() bool {
	return ch.UpgradeAllowed == nil && len(ch.Allow)+len(ch.Disallow) == 0 && ch.Cutoff == "" && len(ch.FormatScores) == 0 && ch.MinFormatScore == nil && ch.MinUpgradeFormatScore == nil && ch.CutoffFormatScore == nil
}

// apply writes the changes into a profile. The qualities are allowed first,
// since a cutoff has to be one the profile allows.
func (ch *profileChanges) apply(p *radarr.QualityProfileResource) error {
	if ch.UpgradeAllowed != nil {
		p.UpgradeAllowed = ch.UpgradeAllowed
	}
	for _, name := range ch.Allow {
		if err := allowQuality(p, name, true); err != nil {
			return err
		}
	}
	for _, name := range ch.Disallow {
		if err := allowQuality(p, name, false); err != nil {
			return err
		}
	}
	if ch.Cutoff != "" {
		id, err := cutoffID(p, ch.Cutoff)
		if err != nil {
			return err
		}
		p.Cutoff = id
	}
	for _, name := range sortedKeys(ch.FormatScores) {
		i := slices.IndexFunc(p.FormatItems, func(f radarr.ProfileFormatItemResource) bool {
			return strings.EqualFold(f.Name, strings.TrimSpace(name))
		})
		if i < 0 {
			have := make([]string, 0, len(p.FormatItems))
			for _, f := range p.FormatItems {
				have = append(have, f.Name)
			}
			return fmt.Errorf("no custom format %q to score (have: %s); customformat_add makes one", name, strings.Join(have, ", "))
		}
		p.FormatItems[i].Score = ch.FormatScores[name]
	}
	if ch.MinFormatScore != nil {
		p.MinFormatScore = *ch.MinFormatScore
	}
	if ch.MinUpgradeFormatScore != nil {
		p.MinUpgradeFormatScore = *ch.MinUpgradeFormatScore
	}
	if ch.CutoffFormatScore != nil {
		p.CutoffFormatScore = *ch.CutoffFormatScore
	}

	return nil
}

// allowQuality switches a quality, or a group of them, on or off in a
// profile. A profile holds its qualities as a list of single ones and groups;
// one inside a group is switched with its group, which is how Radarr's own
// editor does it.
func allowQuality(p *radarr.QualityProfileResource, name string, allowed bool) error {
	want := strings.TrimSpace(name)
	var have []string
	for i := range p.Items {
		item := &p.Items[i]
		label := item.Name
		if item.Quality != nil {
			label = item.Quality.Name
		}
		if strings.EqualFold(label, want) {
			item.Allowed = new(allowed)
			return nil
		}
		for _, child := range item.Items {
			if child.Quality != nil && strings.EqualFold(child.Quality.Name, want) {
				return fmt.Errorf("%s is part of the group %s: name the group", child.Quality.Name, item.Name)
			}
		}
		have = append(have, label)
	}

	return fmt.Errorf("no quality %q in the profile (it has: %s)", name, strings.Join(have, ", "))
}

func registerQualityProfileTools(r *registry) {
	c := r.client

	type profileAddIn struct {
		Name   string `json:"name"              jsonschema:"what to call the new profile"`
		CopyOf string `json:"copy_of,omitempty" jsonschema:"a profile to start from, by name or id; default one that accepts nothing, to be filled in with allow"`
		profileChanges
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "qualityprofile_add",
		Description: "Add a quality profile, the set of qualities films on it are held to: start from a copy of another (copy_of) or from one that accepts nothing, and say which qualities it accepts, where it stops upgrading, and how it scores the custom formats. A film is put on it by editing the film.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in profileAddIn) (*mcp.CallToolResult, profileRow, error) {
		name := strings.TrimSpace(in.Name)
		if name == "" {
			return nil, profileRow{}, errors.New("give the new profile a name")
		}
		if _, err := resolveProfile(ctx, c, name); err == nil {
			return nil, profileRow{}, fmt.Errorf("there is already a quality profile %q; qualityprofile_edit changes it", name)
		}

		var p radarr.QualityProfileResource
		if in.CopyOf != "" {
			from, err := resolveProfile(ctx, c, in.CopyOf)
			if err != nil {
				return nil, profileRow{}, err
			}
			p = *from
		} else {
			blank, err := c.GetQualityProfileSchema(ctx)
			if err != nil {
				return nil, profileRow{}, err
			}
			if blank.Answer == nil {
				return nil, profileRow{}, errors.New("no answer from Radarr when asked for a blank quality profile")
			}

			p = *blank.Answer
		}
		p.Id, p.Name = 0, name
		if err := in.apply(&p); err != nil {
			return nil, profileRow{}, err
		}
		if allowed, _ := profileQualities(&p); len(allowed) == 0 {
			return nil, profileRow{}, errors.New("the new profile accepts no quality: name some with allow, or start from another profile with copy_of")
		}
		// a profile that accepts something stops upgrading at one of them: Radarr refuses one whose cutoff it does not accept
		if in.Cutoff == "" && in.CopyOf == "" {
			if p.Cutoff, _ = bestAllowed(&p); p.Cutoff == 0 {
				return nil, profileRow{}, errors.New("give the new profile a cutoff: the quality to stop upgrading at")
			}
		}

		res, err := c.PostQualityProfile(ctx, p)
		if err != nil {
			return nil, profileRow{}, err
		}

		return nil, profileOf(res.Answer, 0), nil
	})

	type profileDeleteIn struct {
		Profile string `json:"profile" jsonschema:"the quality profile to delete: its name or id"`
		confirmIn
	}
	add(r, deleteTool, &mcp.Tool{
		Name:        "qualityprofile_delete",
		Description: "Delete a quality profile no film is on. One that films, a collection or an import list still use is refused, with how many: move them to another profile first. Without confirm nothing changes and the answer says what would go.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in profileDeleteIn) (*mcp.CallToolResult, deletedOut, error) {
		p, err := resolveProfile(ctx, c, in.Profile)
		if err != nil {
			return nil, deletedOut{}, err
		}
		movies, err := allMovies(ctx, c)
		if err != nil {
			return nil, deletedOut{}, err
		}
		if n := len(slices.DeleteFunc(movies, func(m radarr.MovieResource) bool { return m.QualityProfileId != p.Id })); n > 0 {
			films := "films are"
			if n == 1 {
				films = "film is"
			}

			return nil, deletedOut{}, fmt.Errorf("%d %s on %s: movie_batch_edit moves them to another profile first", n, films, p.Name)
		}

		unlock := r.locking().By(profileLock(p.Id))
		defer unlock()
		loses := []string{"the qualities it accepted, its cutoff and its score for every custom format"}
		out, err := deleting(in.Confirm, p.Name, loses, func() error {
			_, delErr := c.DeleteQualityProfileById(ctx, p.Id)
			return delErr
		})

		return nil, out, err
	})
}

// bestAllowed is the id of the best quality, or group, a profile accepts:
// Radarr lists them worst first.
func bestAllowed(p *radarr.QualityProfileResource) (id int, name string) {
	for _, item := range slices.Backward(p.Items) {
		if !isTrue(item.Allowed) {
			continue
		}
		if item.Quality != nil {
			return item.Quality.Id, item.Quality.Name
		}

		return item.Id, item.Name
	}

	return 0, ""
}
