package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"

	"github.com/katbyte/radarr-mcp/sdk/radarr"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The five kinds of provider, each with the calls of Radarr's API it is read
// and written by (see provider_kinds.go for what they share).

func indexerKind(c *radarr.Client) *providerKind[radarr.IndexerResource] {
	return &providerKind[radarr.IndexerResource]{
		family: "indexer", noun: "indexer", plural: "indexers",
		does:     "where Radarr searches for releases and reads the feeds of new ones.",
		kinds:    "Newznab, Torznab, or a tracker Radarr knows by name",
		switches: "enableRss, enableAutomaticSearch, enableInteractiveSearch, priority, downloadClientId",
		loses:    "Radarr stops searching it and reading its feed",
		on:       []string{"enableRss", "enableAutomaticSearch", "enableInteractiveSearch"},
		starts:   "used for everything its kind supports, the RSS feed and both kinds of search",
		begin: func(values map[string]any) {
			values["enableRss"], values["enableAutomaticSearch"], values["enableInteractiveSearch"] = isSet(values["supportsRss"]), isSet(values["supportsSearch"]), isSet(values["supportsSearch"])
		},
		list: func(ctx context.Context) ([]radarr.IndexerResource, error) {
			res, err := c.GetIndexer(ctx)
			return res.Answer, err
		},
		schema: func(ctx context.Context) ([]radarr.IndexerResource, error) {
			res, err := c.GetIndexerSchema(ctx)
			return res.Answer, err
		},
		create: func(ctx context.Context, v radarr.IndexerResource, force bool) (*radarr.IndexerResource, error) {
			res, err := c.PostIndexer(ctx, v, force)
			return res.Answer, err
		},
		update: func(ctx context.Context, id int, v radarr.IndexerResource, force bool) (*radarr.IndexerResource, error) {
			res, err := c.PutIndexerById(ctx, radarr.IndexerID(id), v, force)
			return res.Answer, err
		},
		remove: func(ctx context.Context, id int) error {
			_, err := c.DeleteIndexerById(ctx, radarr.IndexerID(id))
			return err
		},
		action: func(ctx context.Context, name string, v radarr.IndexerResource) (json.RawMessage, error) {
			res, err := c.PostIndexerActionByName(ctx, name, v)
			return res.Answer, err
		},
	}
}

func downloadClientKind(c *radarr.Client) *providerKind[radarr.DownloadClientResource] {
	return &providerKind[radarr.DownloadClientResource]{
		family: "downloadclient", noun: "download client", plural: "download clients",
		does:     "what Radarr sends a grabbed release to and watches until it has finished.",
		kinds:    "SABnzbd, NZBGet, qBittorrent, Transmission, Deluge, a blackhole folder and the rest",
		switches: "enable, priority, removeCompletedDownloads, removeFailedDownloads",
		loses:    "Radarr stops sending grabs to it and no longer sees what it is downloading",
		on:       []string{"enable"},
		starts:   "switched on",
		begin:    func(values map[string]any) { values["enable"] = true },
		list: func(ctx context.Context) ([]radarr.DownloadClientResource, error) {
			res, err := c.GetDownloadClient(ctx)
			return res.Answer, err
		},
		schema: func(ctx context.Context) ([]radarr.DownloadClientResource, error) {
			res, err := c.GetDownloadClientSchema(ctx)
			return res.Answer, err
		},
		create: func(ctx context.Context, v radarr.DownloadClientResource, force bool) (*radarr.DownloadClientResource, error) {
			res, err := c.PostDownloadClient(ctx, v, force)
			return res.Answer, err
		},
		update: func(ctx context.Context, id int, v radarr.DownloadClientResource, force bool) (*radarr.DownloadClientResource, error) {
			res, err := c.PutDownloadClientById(ctx, radarr.DownloadClientID(id), v, force)
			return res.Answer, err
		},
		remove: func(ctx context.Context, id int) error {
			_, err := c.DeleteDownloadClientById(ctx, radarr.DownloadClientID(id))
			return err
		},
		action: func(ctx context.Context, name string, v radarr.DownloadClientResource) (json.RawMessage, error) {
			res, err := c.PostDownloadClientActionByName(ctx, name, v)
			return res.Answer, err
		},
	}
}

func importListKind(c *radarr.Client) *providerKind[radarr.ImportListResource] {
	return &providerKind[radarr.ImportListResource]{
		family: "importlist", noun: "import list", plural: "import lists",
		does:     "a list kept somewhere else that Radarr reads for films to add - a TMDb or Trakt list, an IMDb watchlist, another Radarr, a Plex watchlist.",
		kinds:    "TMDbListImport, TraktListImport, IMDbListImport, PlexImport, RadarrImport and the rest",
		switches: "enabled, enableAuto (add what it offers without being asked), monitor (movieOnly, movieAndCollection or none), qualityProfileId, rootFolderPath, minimumAvailability, searchOnAdd",
		loses:    "Radarr stops adding the films it offers; the ones it already added stay",
		on:       []string{"enabled"},
		starts:   "as the kind is described, waiting for films that are released",
		begin: func(values map[string]any) {
			if textOf(values, "minimumAvailability") == "" {
				values["minimumAvailability"] = "released"
			}
		},
		list: func(ctx context.Context) ([]radarr.ImportListResource, error) {
			res, err := c.GetImportList(ctx)
			return res.Answer, err
		},
		schema: func(ctx context.Context) ([]radarr.ImportListResource, error) {
			res, err := c.GetImportListSchema(ctx)
			return res.Answer, err
		},
		create: func(ctx context.Context, v radarr.ImportListResource, force bool) (*radarr.ImportListResource, error) {
			res, err := c.PostImportList(ctx, v, force)
			return res.Answer, err
		},
		update: func(ctx context.Context, id int, v radarr.ImportListResource, force bool) (*radarr.ImportListResource, error) {
			res, err := c.PutImportListById(ctx, id, v, force)
			return res.Answer, err
		},
		remove: func(ctx context.Context, id int) error {
			_, err := c.DeleteImportListById(ctx, id)
			return err
		},
		action: func(ctx context.Context, name string, v radarr.ImportListResource) (json.RawMessage, error) {
			res, err := c.PostImportListActionByName(ctx, name, v)
			return res.Answer, err
		},
		test: func(ctx context.Context, v radarr.ImportListResource) (*http.Response, error) {
			res, err := c.PostImportListTest(ctx, v, true)
			return res.HttpResponse, err
		},
		testAll: func(ctx context.Context) ([]radarr.ProviderTestAllResult, error) {
			res, err := c.PostImportListTestAll(ctx)
			return res.Answer, err
		},
	}
}

func notificationKind(c *radarr.Client) *providerKind[radarr.NotificationResource] {
	return &providerKind[radarr.NotificationResource]{
		family: "notification", noun: "notification", plural: "notifications",
		does:     "where Radarr sends word of what it does - a grab, an import, an upgrade, a film added or deleted, a health issue - or what it runs or calls when it happens.",
		kinds:    "Discord, Email, Telegram, Pushover, Webhook, a custom script, Plex, Emby, Jellyfin, Kodi and the rest",
		switches: "onGrab, onDownload (an import), onUpgrade, onRename, onMovieAdded, onMovieDelete, onMovieFileDelete, onMovieFileDeleteForUpgrade, onHealthIssue, onHealthRestored, onApplicationUpdate, onManualInteractionRequired, includeHealthWarnings",
		loses:    "Radarr no longer sends word through it",
		on:       notificationEvents,
		starts:   "sent for everything its kind can tell of except health",
		begin: func(values map[string]any) {
			for _, event := range notificationEvents {
				// the health events start off, as in the form
				if strings.HasPrefix(event, "onHealth") {
					continue
				}
				// onGrab starts as supportsOnGrab says
				values[event] = isSet(values["supports"+strings.ToUpper(event[:1])+event[1:]])
			}
		},
		list: func(ctx context.Context) ([]radarr.NotificationResource, error) {
			res, err := c.GetNotification(ctx)
			return res.Answer, err
		},
		schema: func(ctx context.Context) ([]radarr.NotificationResource, error) {
			res, err := c.GetNotificationSchema(ctx)
			return res.Answer, err
		},
		create: func(ctx context.Context, v radarr.NotificationResource, force bool) (*radarr.NotificationResource, error) {
			res, err := c.PostNotification(ctx, v, force)
			return res.Answer, err
		},
		update: func(ctx context.Context, id int, v radarr.NotificationResource, force bool) (*radarr.NotificationResource, error) {
			res, err := c.PutNotificationById(ctx, id, v, force)
			return res.Answer, err
		},
		remove: func(ctx context.Context, id int) error {
			_, err := c.DeleteNotificationById(ctx, id)
			return err
		},
		action: func(ctx context.Context, name string, v radarr.NotificationResource) (json.RawMessage, error) {
			res, err := c.PostNotificationActionByName(ctx, name, v)
			return res.Answer, err
		},
		test: func(ctx context.Context, v radarr.NotificationResource) (*http.Response, error) {
			res, err := c.PostNotificationTest(ctx, v, true)
			return res.HttpResponse, err
		},
		testAll: func(ctx context.Context) ([]radarr.ProviderTestAllResult, error) {
			res, err := c.PostNotificationTestAll(ctx)
			return res.Answer, err
		},
	}
}

func metadataKind(c *radarr.Client) *providerKind[radarr.MetadataResource] {
	return &providerKind[radarr.MetadataResource]{
		family: "metadata", noun: "metadata consumer", plural: "metadata consumers",
		does:     "the files Radarr writes beside a film for a media player to read - an .nfo, a poster, fanart - one consumer for each player's format.",
		kinds:    "Kodi (XBMC) / Emby, Roksbox, WDTV",
		switches: "enable",
		fixed:    true,
		on:       []string{"enable"},
		list: func(ctx context.Context) ([]radarr.MetadataResource, error) {
			res, err := c.GetMetadata(ctx)
			return res.Answer, err
		},
		schema: func(ctx context.Context) ([]radarr.MetadataResource, error) {
			res, err := c.GetMetadataSchema(ctx)
			return res.Answer, err
		},
		update: func(ctx context.Context, id int, v radarr.MetadataResource, force bool) (*radarr.MetadataResource, error) {
			res, err := c.PutMetadataById(ctx, id, v, force)
			return res.Answer, err
		},
		test: func(ctx context.Context, v radarr.MetadataResource) (*http.Response, error) {
			res, err := c.PostMetadataTest(ctx, v, true)
			return res.HttpResponse, err
		},
		testAll: func(ctx context.Context) ([]radarr.ProviderTestAllResult, error) {
			res, err := c.PostMetadataTestAll(ctx)
			return res.Answer, err
		},
		action: func(ctx context.Context, name string, v radarr.MetadataResource) (json.RawMessage, error) {
			res, err := c.PostMetadataActionByName(ctx, name, v)
			return res.Answer, err
		},
	}
}

// notificationEvents are the switches that say what a notification is sent
// for: it is switched on when any of them is.
var notificationEvents = slices.DeleteFunc(providerSwitches[radarr.NotificationResource](), func(name string) bool { return !strings.HasPrefix(name, "on") })

// listedFilm is a film an import list offers, as Radarr answers it.
type listedFilm struct {
	Title            string `json:"title"`
	Year             int    `json:"year"`
	TmdbID           int    `json:"tmdbId"`
	ImdbID           string `json:"imdbId"`
	IsExisting       bool   `json:"isExisting"`
	IsExcluded       bool   `json:"isExcluded"`
	IsRecommendation bool   `json:"isRecommendation"`
	IsTrending       bool   `json:"isTrending"`
	IsPopular        bool   `json:"isPopular"`
	Lists            []int  `json:"lists"`
}

// kindDescriber is what provider_kinds answers for one of the things it
// knows the kinds of.
type kindDescriber = func(ctx context.Context, kind string) (kindDoc, error)

// registerKindsTool queues provider_kinds, which answers for every kind of
// thing the add tools take a kind of.
func registerKindsTool(r *registry, known ...map[string]kindDescriber) {
	describe := map[string]kindDescriber{}
	for _, m := range known {
		maps.Copy(describe, m)
	}

	type kindsIn struct {
		Provider string `json:"provider"       jsonschema:"what to list the kinds of: indexer, downloadclient, importlist, notification or metadata, or for the kinds of condition one is made of, customformat or autotagging"`
		Kind     string `json:"kind,omitempty" jsonschema:"one kind, to see the settings it takes; default lists the kinds there are"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "provider_kinds",
		Description: "The kinds of indexer, download client, import list, notification or metadata consumer Radarr can be set up with, and the kinds of condition a custom format or an auto tagging rule is made of; or with a kind named, the settings that kind takes: each setting's name, what it holds, what a select accepts, and whether it is a credential. What the _add tools need to be given.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in kindsIn) (*mcp.CallToolResult, kindDoc, error) {
		of, known := describe[strings.ToLower(strings.TrimSpace(in.Provider))]
		if !known {
			return nil, kindDoc{}, fmt.Errorf("no provider %q (have: %s)", in.Provider, strings.Join(sortedKeys(describe), ", "))
		}
		out, err := of(ctx, in.Kind)

		return nil, out, err
	})
}

// registerOptionsTool queues provider_options, which answers for every family
// of provider.
func registerOptionsTool(r *registry, readers map[string]optionsReader) {
	add(r, readTool, &mcp.Tool{
		Name:        "provider_options",
		Description: "What one setting of an indexer, download client, import list or notification can be set to, when the choices come from the service it talks to rather than from Radarr: the categories an indexer has, the quality profiles, tags and root folders another Radarr offers, the devices a notification can reach. provider_kinds marks such a setting with options_from. Name one Radarr already has, which is asked with the credentials it holds, or a kind not added yet with the address and key to ask with. Radarr asks the service and nothing is saved.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in optionsIn) (*mcp.CallToolResult, optionsOut, error) {
		read, known := readers[strings.ToLower(strings.TrimSpace(in.Provider))]
		if !known {
			return nil, optionsOut{}, fmt.Errorf("no provider %q (have: %s)", in.Provider, strings.Join(sortedKeys(readers), ", "))
		}
		out, err := read(ctx, in)

		return nil, out, err
	})
}

func registerProviderFamilyTools(r *registry) (kinds map[string]kindDescriber, options map[string]optionsReader) {
	c := r.client

	indexers, clients, lists, notifications, metadata := indexerKind(c), downloadClientKind(c), importListKind(c), notificationKind(c), metadataKind(c)
	registerListAndTest(r, lists)
	registerListAndTest(r, notifications)
	registerListAndTest(r, metadata)

	type listedIn struct {
		Recommendations bool `json:"recommendations,omitempty" jsonschema:"also the films TMDB recommends from what the library holds"`
		Trending        bool `json:"trending,omitempty"        jsonschema:"also the films trending on TMDB now"`
		Popular         bool `json:"popular,omitempty"         jsonschema:"also the films most popular on TMDB now"`
		NewOnly         bool `json:"new_only,omitempty"        jsonschema:"only films the library does not hold and no exclusion keeps out: what the lists would add"`
		pageIn
	}
	type listedRow struct {
		Title          string   `json:"title"`
		Year           int      `json:"year,omitempty"`
		TmdbID         int      `json:"tmdb_id"`
		ImdbID         string   `json:"imdb_id,omitempty"`
		InLibrary      bool     `json:"in_library"`
		Excluded       bool     `json:"excluded"                 jsonschema:"on the import list exclusions, so no list adds it"`
		Lists          []string `json:"lists"                    jsonschema:"the import lists that offer it, by name"`
		Recommendation bool     `json:"recommendation,omitempty" jsonschema:"offered by TMDB's recommendations rather than a list"`
		Trending       bool     `json:"trending,omitempty"       jsonschema:"trending on TMDB"`
		Popular        bool     `json:"popular,omitempty"        jsonschema:"among the most popular on TMDB"`
	}
	type listedOut struct {
		Movies []listedRow `json:"movies"`
		pageOut
	}
	add(r, readTool, &mcp.Tool{
		Name:        "importlist_movies",
		Description: "The films the enabled import lists offer, as Radarr last read them, each with the lists it is on and whether the library holds it or an exclusion keeps it out; and when asked for, the films TMDB recommends from the library, and the ones trending or most popular there now - what Radarr's own Discover page shows. new_only leaves what the library does not hold. Radarr reads the lists again on its schedule, and when its Import List Sync task is run.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in listedIn) (*mcp.CallToolResult, listedOut, error) {
		res, err := c.GetImportListMovie(ctx, radarr.GetImportListMovieOptions{IncludeRecommendations: new(in.Recommendations), IncludeTrending: new(in.Trending), IncludePopular: new(in.Popular)})
		if err != nil {
			return nil, listedOut{}, err
		}
		// the document does not say what this answers, so it is read as what Radarr sends
		var films []listedFilm
		if len(res.Answer) > 0 {
			if err := json.Unmarshal(res.Answer, &films); err != nil {
				return nil, listedOut{}, fmt.Errorf("reading what the import lists offer: %w", err)
			}
		}
		all, err := lists.all(ctx)
		if err != nil {
			return nil, listedOut{}, err
		}
		names := map[int]string{}
		for _, m := range all {
			names[idOf(m)] = textOf(m, "name")
		}

		rows := make([]listedRow, 0, len(films))
		for _, f := range films {
			if in.NewOnly && (f.IsExisting || f.IsExcluded) {
				continue
			}
			row := listedRow{Title: outsideName(f.Title), Year: f.Year, TmdbID: f.TmdbID, ImdbID: f.ImdbID, InLibrary: f.IsExisting, Excluded: f.IsExcluded, Recommendation: f.IsRecommendation, Trending: f.IsTrending, Popular: f.IsPopular, Lists: []string{}}
			for _, id := range f.Lists {
				row.Lists = append(row.Lists, names[id])
			}
			rows = append(rows, row)
		}
		slices.SortStableFunc(rows, func(a, b listedRow) int { return strings.Compare(strings.ToLower(a.Title), strings.ToLower(b.Title)) })

		out := listedOut{}
		out.Movies, out.pageOut = pageRows(rows, in.pageIn)

		return nil, out, nil
	})

	kinds = map[string]kindDescriber{
		indexers.family:      registerProviderWrites(r, indexers),
		clients.family:       registerProviderWrites(r, clients),
		lists.family:         registerProviderWrites(r, lists),
		notifications.family: registerProviderWrites(r, notifications),
		metadata.family:      registerProviderWrites(r, metadata),
	}
	options = map[string]optionsReader{indexers.family: indexers.options, clients.family: clients.options, lists.family: lists.options, notifications.family: notifications.options, metadata.family: metadata.options}

	return kinds, options
}
