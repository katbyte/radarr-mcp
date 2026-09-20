package tools

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/katbyte/radarr-mcp/lib/radarr"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type tagRow struct {
	ID              int    `json:"id"`
	Label           string `json:"label"`
	Movies          int    `json:"movies"                     jsonschema:"films carrying it"`
	Indexers        int    `json:"indexers,omitempty"`
	DownloadClients int    `json:"download_clients,omitempty"`
	ImportLists     int    `json:"import_lists,omitempty"`
	Notifications   int    `json:"notifications,omitempty"`
	DelayProfiles   int    `json:"delay_profiles,omitempty"`
	ReleaseProfiles int    `json:"release_profiles,omitempty"`
	AutoTags        int    `json:"auto_tags,omitempty"`
}

func tagOf(t *radarr.TagDetailsResource) tagRow {
	return tagRow{
		ID: t.Id, Label: t.Label, Movies: len(t.MovieIds), Indexers: len(t.IndexerIds), DownloadClients: len(t.DownloadClientIds),
		ImportLists: len(t.ImportListIds), Notifications: len(t.NotificationIds), DelayProfiles: len(t.DelayProfileIds),
		ReleaseProfiles: len(t.ReleaseProfileIds), AutoTags: len(t.AutoTagIds),
	}
}

// resolveTag finds one tag, with what uses it.
func resolveTag(ctx context.Context, c *radarr.Client, ref string) (*radarr.TagDetailsResource, error) {
	res, err := c.GetTagDetail(ctx)
	if err != nil {
		return nil, err
	}
	ref = strings.TrimSpace(ref)
	labels := make([]string, 0, len(res.Model))
	for i := range res.Model {
		t := &res.Model[i]
		if strings.EqualFold(t.Label, ref) || strconv.Itoa(t.Id) == ref {
			return t, nil
		}
		labels = append(labels, t.Label)
	}
	slices.Sort(labels)

	return nil, fmt.Errorf("no tag %q; the tags are %s", ref, strings.Join(labels, ", "))
}

func registerTagTools(r *registry) {
	client := r.client

	type tagListOut struct {
		Tags []tagRow `json:"tags"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "tag_list",
		Description: "List Radarr's tags and what carries each: how many films, and which indexers, download clients, import lists, notifications and profiles are limited to films with it.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ noArgs) (*mcp.CallToolResult, tagListOut, error) {
		res, err := client.GetTagDetail(ctx)
		if err != nil {
			return nil, tagListOut{}, err
		}
		out := tagListOut{}
		for i := range res.Model {
			out.Tags = append(out.Tags, tagOf(&res.Model[i]))
		}
		slices.SortFunc(out.Tags, func(a, b tagRow) int { return strings.Compare(a.Label, b.Label) })

		return nil, out, nil
	})

	type tagCreateIn struct {
		Label string `json:"label" jsonschema:"the new tag: lower case letters, digits and hyphens, as Radarr allows"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "tag_create",
		Description: "Create a tag. Tags are how Radarr limits an indexer, download client, import list or profile to some films, and how films are grouped for movie_list.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in tagCreateIn) (*mcp.CallToolResult, tagRow, error) {
		label, err := tagLabel(in.Label)
		if err != nil {
			return nil, tagRow{}, err
		}
		if _, err := resolveTag(ctx, client, label); err == nil {
			return nil, tagRow{}, fmt.Errorf("tag %q already exists", label)
		}
		res, err := client.PostTag(ctx, radarr.TagResource{Label: label})
		if err != nil {
			return nil, tagRow{}, err
		}

		return nil, tagRow{ID: res.Model.Id, Label: res.Model.Label}, nil
	})

	type tagRenameIn struct {
		Tag   string `json:"tag"   jsonschema:"the tag: its label or id"`
		Label string `json:"label" jsonschema:"its new label: lower case letters, digits and hyphens"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "tag_rename",
		Description: "Rename a tag: everything carrying it keeps it under the new label.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in tagRenameIn) (*mcp.CallToolResult, tagRow, error) {
		t, err := resolveTag(ctx, client, in.Tag)
		if err != nil {
			return nil, tagRow{}, err
		}
		label, err := tagLabel(in.Label)
		if err != nil {
			return nil, tagRow{}, err
		}
		if other, err := resolveTag(ctx, client, label); err == nil && other.Id != t.Id {
			return nil, tagRow{}, fmt.Errorf("tag %q already exists (id %d)", label, other.Id)
		}
		if _, err := client.PutTagById(ctx, t.Id, radarr.TagResource{Id: t.Id, Label: label}); err != nil {
			return nil, tagRow{}, err
		}
		after, err := resolveTag(ctx, client, strconv.Itoa(t.Id))
		if err != nil {
			return nil, tagRow{}, err
		}

		return nil, tagOf(after), nil
	})

	type tagDeleteIn struct {
		Tag string `json:"tag" jsonschema:"the tag: its label or id"`
	}
	add(r, writeTool, &mcp.Tool{
		Name: "tag_delete",
		Description: "Delete a tag, taking it off the films that carry it first, and answer with how many did. " +
			"A tag an indexer, download client, import list, notification or profile still uses is refused: deleting it would change which films those serve.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in tagDeleteIn) (*mcp.CallToolResult, tagRow, error) {
		t, err := resolveTag(ctx, client, in.Tag)
		if err != nil {
			return nil, tagRow{}, err
		}
		// Radarr refuses to delete a tag anything still carries (409); the
		// films are this tool's to take it off, the settings are not
		if users := tagUsers(t); len(users) > 0 {
			return nil, tagRow{}, fmt.Errorf("tag %q still limits %s; take it off those in Radarr first, since deleting it would change which films they serve", t.Label, strings.Join(users, ", "))
		}
		if len(t.MovieIds) > 0 {
			if _, err := client.PutMovieEditor(ctx, radarr.MovieEditorResource{MovieIds: t.MovieIds, Tags: []int{t.Id}, ApplyTags: radarr.ApplyTagsRemove}); err != nil {
				return nil, tagRow{}, fmt.Errorf("taking %q off its films: %w", t.Label, err)
			}
		}
		if _, err := client.DeleteTagById(ctx, t.Id); err != nil {
			return nil, tagRow{}, err
		}

		return nil, tagOf(t), nil
	})
}

// tagLabelRe is what Radarr allows in a tag's label.
var tagLabelRe = regexp.MustCompile(`^[a-z0-9-]+$`)

// tagLabel is a label as Radarr keeps it, lower case, or an error saying what
// Radarr would refuse it for.
func tagLabel(s string) (string, error) {
	label := strings.ToLower(strings.TrimSpace(s))
	switch {
	case label == "":
		return "", errors.New("no label")
	case !tagLabelRe.MatchString(label):
		return "", fmt.Errorf("tag %q: Radarr allows only a-z, 0-9 and - in a tag (a hyphen for a space)", s)
	default:
		return label, nil
	}
}

// tagUsers names the settings that carry a tag, other than films: "1
// indexer", "2 download clients".
func tagUsers(t *radarr.TagDetailsResource) []string {
	var out []string
	for _, u := range []struct {
		n    int
		kind string
	}{
		{len(t.IndexerIds), "indexer"},
		{len(t.DownloadClientIds), "download client"},
		{len(t.ImportListIds), "import list"},
		{len(t.NotificationIds), "notification"},
		{len(t.DelayProfileIds), "delay profile"},
		{len(t.ReleaseProfileIds), "release profile"},
		{len(t.AutoTagIds), "auto tag"},
	} {
		switch {
		case u.n == 1:
			out = append(out, "1 "+u.kind)
		case u.n > 1:
			out = append(out, fmt.Sprintf("%d %ss", u.n, u.kind))
		}
	}

	return out
}
