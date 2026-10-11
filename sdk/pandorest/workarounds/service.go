// Package workarounds is what is radarr-mcp's own about generating its
// client: the one service pandorest generates here, and a named fix for each
// thing Radarr's API document gets wrong. The generator itself is
// github.com/katbyte/pandorest.
//
// Every workaround patches the loaded document before it is read, and every
// one first checks that the bug it fixes is still there: when the document no
// longer has the problem the workaround fails the import instead of silently
// doing nothing, so dead workarounds get deleted rather than accumulating.
// Only the shape of the document belongs here: what an operation takes and
// answers. Behaviour the document cannot express stays in the tools, next to
// the live tests that found it.
package workarounds

import (
	"github.com/katbyte/pandorest/config"
	pandorest "github.com/katbyte/pandorest/importer/workarounds"
)

// Service is the name the document, its definitions and every workaround
// carry.
const Service = "radarr"

// Services is the one API generated here.
var Services = []config.Service{{
	Name:    Service,
	Package: "radarr",
	Output:  "sdk/radarr",
	// Swashbuckle leaves the document without operationIds, so the methods are named from the method and the path, with the prefix every path carries left out: GET /api/v3/movie/{id} is GetMovieById
	Naming:     config.PathNaming,
	PathPrefix: "/api/v3",
	Words:      radarrWords,
	Auth:       "APIKey",
	// what is Radarr's own about being talked to, declared by hand beside the generated files (sdk/radarr/service.go)
	ClientService: "service",
	// how the generated New names what it is given, and says where that is found
	Credential: "API key",
	NewDoc:     "New returns a client for the Radarr server at baseURL (scheme and host,\nplus the URL base when Radarr is set up with one, e.g. /radarr) that\nauthenticates with the API key from Settings > General > Security.",
	Notes:      `Dates are strings: Radarr sends RFC 3339 in UTC ("2024-03-01T00:00:00Z").`,
	// history, the queue, the blocklist, the wanted lists, the exclusions and the log page by number, and answer a PagingResource
	Paging: &config.Paging{Start: "page", Limit: "pageSize", Items: "records", Total: "totalRecords", ByPage: true},
	// Radarr refuses a body without a property its document requires, an empty string included
	SendRequired: true,
	// Swashbuckle lists text/plain beside about a hundred JSON responses, which Radarr never sends for them
	PreferJSON: true,
	// a test of every provider answers 400 with the same list of results when any of them fails
	ExpectSameShape: true,
	// a null here means something - no size limit, no film, no score - that a zero would say wrongly
	KeepNull: []string{
		"CollectionUpdateResource.qualityProfileId",
		"DownloadClientBulkResource.priority",
		"ExtraFileResource.movieFileId",
		"ImportListBulkResource.qualityProfileId",
		"IndexerBulkResource.priority",
		"ManualImportResource.movieFileId",
		"MovieEditorResource.qualityProfileId",
		"MovieFileListResource.indexerFlags",
		"MovieFileResource.customFormatScore",
		"MovieFileResource.indexerFlags",
		"MovieResource.secondaryYear",
		"MovieResource.sizeOnDisk",
		"QualityDefinitionResource.maxSize",
		"QualityDefinitionResource.minSize",
		"QualityDefinitionResource.preferredSize",
		"QueueResource.movieId",
		"ReleaseResource.downloadClientId",
		"ReleaseResource.leechers",
		"ReleaseResource.mappedMovieId",
		"ReleaseResource.movieId",
		"ReleaseResource.seeders",
		"RootFolderResource.freeSpace",
	},
}}

// radarrWords are the path segments of Radarr's API that run two or more
// words together, spelled the way its tags and models spell them, so that
// GET /api/v3/moviefile/{id} is GetMovieFileById.
var radarrWords = map[string]string{
	"alttitle":          "AltTitle",
	"autotagging":       "AutoTagging",
	"customfilter":      "CustomFilter",
	"customformat":      "CustomFormat",
	"delayprofile":      "DelayProfile",
	"diskspace":         "DiskSpace",
	"downloadclient":    "DownloadClient",
	"extrafile":         "ExtraFile",
	"filesystem":        "FileSystem",
	"importlist":        "ImportList",
	"indexerflag":       "IndexerFlag",
	"manualimport":      "ManualImport",
	"mediacover":        "MediaCover",
	"mediafiles":        "MediaFiles",
	"mediamanagement":   "MediaManagement",
	"moviefile":         "MovieFile",
	"qualitydefinition": "QualityDefinition",
	"qualityprofile":    "QualityProfile",
	"releaseprofile":    "ReleaseProfile",
	"remotepathmapping": "RemotePathMapping",
	"rootfolder":        "RootFolder",
	"testall":           "TestAll",
}

// All is every workaround, in the order they are applied.
var All = []pandorest.Workaround{
	radarrUndeclaredResponses,
	radarrAuthRedirects,
	radarrRootPathParameter{},
	radarrCommandBody{},
	radarrStatuses,
	radarrUndeclaredFields,
	radarrCommandResourceBody{},
	radarrRequiredParameters{},
	radarrWriteResponses{},
	radarrBulkResponses,
	radarrHostRequired{},
	radarrPutIDs{},
}
