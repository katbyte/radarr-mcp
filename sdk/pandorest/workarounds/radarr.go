package workarounds

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	pandorest "github.com/katbyte/pandorest/importer/workarounds"
	"github.com/katbyte/pandorest/openapi"
)

// The Radarr document is the one Radarr's own repository carries at
// src/Radarr.Api.V3/openapi.json, written by Swashbuckle from the
// controllers. Swashbuckle can only describe what a controller's signature
// says, so every action that returns object or IActionResult comes out with
// an empty 200. What follows was found by the SDK suite against the
// server itself.

const radarr = Service

const (
	textHTML    = "text/html"
	textPlain   = "text/plain"
	octetStream = "application/octet-stream"
)

// radarrUndeclaredResponses declares what the GETs without a response schema
// answer.
var radarrUndeclaredResponses = pandorest.UndeclaredAnswers(pandorest.About{
	Name: "radarr-undeclared-responses", Service: radarr,
	Bug: "the GETs whose controllers return object or IActionResult declare a 200 with no content, so nothing says whether they answer JSON (and in what shape) or a file",
}, map[string]pandorest.Answer{
	// JSON the controllers build as anonymous objects
	"GET /api/v3/autotagging/schema":      pandorest.JSON(),
	"GET /api/v3/config/naming/examples":  pandorest.JSON(),
	"GET /api/v3/customformat/schema":     pandorest.JSON(),
	"GET /api/v3/filesystem":              pandorest.JSON(),
	"GET /api/v3/filesystem/mediafiles":   pandorest.JSON(),
	"GET /api/v3/filesystem/type":         pandorest.JSON(),
	"GET /api/v3/importlist/movie":        pandorest.JSON(),
	"GET /api/v3/movie/{id}/folder":       pandorest.JSON(),
	"GET /api/v3/system/routes/duplicate": pandorest.JSON(),
	// every route Radarr serves, as a graphviz digraph in plain text
	"GET /api/v3/system/routes": pandorest.File(textPlain),
	// a movie's cast and crew, the same resource GET /api/v3/credit/{id} answers
	"GET /api/v3/credit": pandorest.ListOf("CreditResource"),

	// files
	"GET /":                                  pandorest.File(textHTML),
	"GET /login":                             pandorest.File(textHTML),
	"GET /{path}":                            pandorest.File(octetStream),
	"GET /content/{path}":                    pandorest.File(octetStream),
	"GET /api/v3/log/file/{filename}":        pandorest.File(textPlain),
	"GET /api/v3/log/file/update/{filename}": pandorest.File(textPlain),
	"GET /api/v3/mediacover/{movieId}/{filename}": pandorest.File("image/*"),
	"GET /feed/v3/calendar/radarr.ics":            pandorest.File("text/calendar"),
})

// radarrRootPathParameter drops the {path} parameter GET / declares without
// having the placeholder: the root and the catch-all GET /{path} are one
// controller action, and Swashbuckle gives both routes its parameter.
type radarrRootPathParameter struct{}

func (radarrRootPathParameter) Name() string    { return "radarr-root-path-parameter" }
func (radarrRootPathParameter) Service() string { return radarr }
func (radarrRootPathParameter) Bug() string {
	return "GET / declares a path parameter named path that its template does not have: it shares an action with the catch-all GET /{path}"
}

func (radarrRootPathParameter) Apply(spec *openapi.Spec) error {
	op, err := pandorest.Operation(spec, http.MethodGet, "/")
	if err != nil {
		return err
	}
	if op.Parameter(openapi.InPath, "path") == nil {
		return errors.New("GET / no longer declares a path parameter")
	}
	op.Parameters = slices.DeleteFunc(op.Parameters, func(p *openapi.Parameter) bool {
		return p.In == openapi.InPath && p.Name == "path"
	})

	return nil
}

// radarrCommandBody makes POST /api/v3/command take any command. Radarr
// reads the body's name, finds the command class of that name and
// deserialises the body into it, so a RefreshMovie carries movieIds and a
// ManualImport carries files and importMode; CommandResource, which the
// document names, has none of those fields, and a typed body would drop
// them on the way out.
type radarrCommandBody struct{}

func (radarrCommandBody) Name() string    { return "radarr-command-body" }
func (radarrCommandBody) Service() string { return radarr }
func (radarrCommandBody) Bug() string {
	return "POST /api/v3/command declares a CommandResource body, which holds none of the fields a command reads (movieIds, files, importMode): the server deserialises the body into the command class its name names"
}

func (radarrCommandBody) Apply(spec *openapi.Spec) error {
	op, err := pandorest.Operation(spec, http.MethodPost, "/api/v3/command")
	if err != nil {
		return err
	}
	if op.RequestBody == nil {
		return errors.New("POST /api/v3/command no longer takes a body")
	}
	changed := false
	for _, media := range op.RequestBody.Content {
		if media.Schema == nil || media.Schema.RefName() != "CommandResource" {
			continue
		}
		media.Schema = &openapi.Schema{Type: openapi.TypeObject, Description: "a command: its name, and the fields that command reads"}
		changed = true
	}
	if !changed {
		return errors.New("the body is no longer a CommandResource")
	}

	return nil
}

// radarrCommands names the commands the tools and suites queue, with the fields each reads, so each has a method of its own and one that
// waits for it to finish. Radarr has more; one not named here is still queued through PostCommand by name.
var radarrCommands = pandorest.Commands(pandorest.About{
	Name: "radarr-commands", Service: radarr,
	Bug: "POST /api/v3/command takes any command by name and the document declares none of them, nor the fields each reads",
}, pandorest.CommandSet{
	Post:     "POST /api/v3/command",
	Get:      "GET /api/v3/command/{id}",
	Status:   "status",
	Finished: []string{"completed", "failed", "aborted", "cancelled", "orphaned"},
	Failed:   []string{"failed"},
	Commands: map[string]map[string]*openapi.Schema{
		"ApplicationUpdate":         {},
		"Backup":                    {},
		"CheckHealth":               {},
		"CleanUpRecycleBin":         {},
		"CutOffUnmetMoviesSearch":   {},
		"Housekeeping":              {},
		"ImportListSync":            {},
		"MissingMoviesSearch":       {},
		"RefreshCollections":        {},
		"RefreshMonitoredDownloads": {},
		"RssSync":                   {},
		// every film when movieIds is left out
		"RefreshMovie": {"movieIds": numbers()},
		"MoviesSearch": {"movieIds": numbers()},
		"RescanMovie":  {"movieId": number()},
		"RenameFiles":  {"movieId": number(), "files": numbers()},
		"ManualImport": {"importMode": {Type: openapi.TypeString}, "files": {Type: openapi.TypeArray, Items: &openapi.Schema{Type: openapi.TypeObject, Properties: map[string]*openapi.Schema{
			"path":         {Type: openapi.TypeString},
			"movieId":      number(),
			"quality":      {Ref: openapi.SchemaRefPrefix + "QualityModel"},
			"languages":    {Type: openapi.TypeArray, Items: &openapi.Schema{Ref: openapi.SchemaRefPrefix + "Language"}},
			"releaseGroup": {Type: openapi.TypeString},
			"downloadId":   {Type: openapi.TypeString},
			"indexerFlags": number(),
		}}}},
	},
})

// number is a whole number as Radarr's document declares one, and numbers a list of them.
func number() *openapi.Schema { return &openapi.Schema{Type: openapi.TypeInteger, Format: "int32"} }

func numbers() *openapi.Schema { return &openapi.Schema{Type: openapi.TypeArray, Items: number()} }

// each is every path with its method, as a ready-made workaround names an
// operation.
func each(method string, paths ...string) []string {
	targets := make([]string, 0, len(paths))
	for _, path := range paths {
		targets = append(targets, method+" "+path)
	}

	return targets
}

// radarrStatuses declares the statuses the writes answer. Radarr's REST
// controllers answer a create with 201 Created and an update with 202
// Accepted, both carrying the resource, but the document says 200 for all of
// them, so every create and update would come back as an undocumented
// status.
var radarrStatuses = pandorest.WrongStatus(pandorest.About{
	Name: "radarr-statuses", Service: radarr,
	Bug: "every create is documented as 200 but answers 201 Created, and every update (the bulk and editor ones too) as 200 but answers 202 Accepted",
}, http.StatusOK, map[int][]string{
	// the creates, answered 201 with the new resource
	http.StatusCreated: each(http.MethodPost,
		"/api/v3/autotagging",
		"/api/v3/command",
		"/api/v3/customfilter",
		"/api/v3/customformat",
		"/api/v3/delayprofile",
		"/api/v3/downloadclient",
		"/api/v3/exclusions",
		"/api/v3/importlist",
		"/api/v3/indexer",
		"/api/v3/metadata",
		"/api/v3/movie",
		"/api/v3/notification",
		"/api/v3/qualityprofile",
		"/api/v3/releaseprofile",
		"/api/v3/remotepathmapping",
		"/api/v3/rootfolder",
		"/api/v3/tag",
	),
	// the updates, answered 202 with what they changed
	http.StatusAccepted: each(http.MethodPut,
		"/api/v3/autotagging/{id}",
		"/api/v3/collection",
		"/api/v3/collection/{id}",
		"/api/v3/config/downloadclient/{id}",
		"/api/v3/config/host/{id}",
		"/api/v3/config/importlist/{id}",
		"/api/v3/config/indexer/{id}",
		"/api/v3/config/mediamanagement/{id}",
		"/api/v3/config/metadata/{id}",
		"/api/v3/config/naming/{id}",
		"/api/v3/config/ui/{id}",
		"/api/v3/customfilter/{id}",
		"/api/v3/customformat/bulk",
		"/api/v3/customformat/{id}",
		"/api/v3/delayprofile/{id}",
		"/api/v3/downloadclient/bulk",
		"/api/v3/downloadclient/{id}",
		"/api/v3/exclusions/{id}",
		"/api/v3/importlist/bulk",
		"/api/v3/importlist/{id}",
		"/api/v3/indexer/bulk",
		"/api/v3/indexer/{id}",
		"/api/v3/metadata/{id}",
		"/api/v3/movie/{id}",
		"/api/v3/movie/editor",
		"/api/v3/moviefile/{id}",
		"/api/v3/moviefile/editor",
		"/api/v3/moviefile/bulk",
		"/api/v3/notification/{id}",
		"/api/v3/qualitydefinition/update",
		"/api/v3/qualitydefinition/{id}",
		"/api/v3/qualityprofile/{id}",
		"/api/v3/releaseprofile/{id}",
		"/api/v3/remotepathmapping/{id}",
		"/api/v3/tag/{id}",
	),
})

// radarrUndeclaredFields adds the fields Radarr sends that its document does
// not declare: the document was written before them, and a model without
// them drops what the server says, and sends a resource back without them.
// The read sweep's strict decode finds each one.
var radarrUndeclaredFields = pandorest.UndeclaredProperties(pandorest.About{
	Name: "radarr-undeclared-fields", Service: radarr,
	Bug: "the server sends fields the document does not declare: allowedHosts and trustedNetworks on the host config, isContainerized on the system status, movieFileQualities on a film's statistics, isExcluded on a film a lookup finds",
}, map[string]map[string]*openapi.Schema{
	// the hosts Radarr answers on, and the networks it trusts as local,
	// comma-separated; Radarr 6 refuses a host config without allowed hosts
	// unless authentication is required everywhere
	"HostConfigResource": {
		"allowedHosts":    {Type: openapi.TypeString, Nullable: true},
		"trustedNetworks": {Type: openapi.TypeString, Nullable: true},
	},
	// whether Radarr runs in a container, which decides how it updates
	"SystemResource": {
		"isContainerized": {Type: openapi.TypeBoolean},
	},
	// the qualities of a film's files
	"MovieStatisticsResource": {
		"movieFileQualities": {Type: openapi.TypeArray, Items: &openapi.Schema{Ref: openapi.SchemaRefPrefix + "Quality"}},
	},
	// whether a film found by a lookup is on the import list exclusions
	"MovieResource": {
		"isExcluded": {Type: openapi.TypeBoolean},
	},
})

// radarrCommandResourceBody makes a command's body raw JSON. Like the body a
// command is started with (radarrCommandBody), the body a command is read
// back with is the command's own class, carrying the fields that command
// reads - movieIds, movieId, isNewMovie, files - none of which the Command
// schema the document names has.
type radarrCommandResourceBody struct{}

func (radarrCommandResourceBody) Name() string    { return "radarr-command-resource-body" }
func (radarrCommandResourceBody) Service() string { return radarr }
func (radarrCommandResourceBody) Bug() string {
	return "a command's body is declared as the Command schema, which holds none of the fields a command carries (movieIds, movieId, files...): the server serialises the command's own class"
}

func (radarrCommandResourceBody) Apply(spec *openapi.Spec) error {
	body, err := pandorest.Property(spec, "CommandResource", "body")
	if err != nil {
		return err
	}
	if body.RefName() != "Command" {
		return errors.New("CommandResource.body is no longer the Command schema")
	}
	*body = openapi.Schema{Type: openapi.TypeObject, Description: "the command's own fields, which depend on its name"}

	return nil
}

// radarrRequiredParameters marks the query parameters Radarr cannot answer
// without. The document declares every query parameter optional, and these
// answer 400, 500 or 503 (or, for parse, an empty 204) when one is left out.
type radarrRequiredParameters struct{}

// radarrRequired is the parameter each operation cannot do without.
var radarrRequired = map[string]string{
	"GET /api/v3/filesystem/mediafiles": "path",
	"GET /api/v3/filesystem/type":       "path",
	"GET /api/v3/movie/lookup":          "term",
	"GET /api/v3/movie/lookup/imdb":     "imdbId",
	"GET /api/v3/movie/lookup/tmdb":     "tmdbId",
	"GET /api/v3/parse":                 "title",
	"GET /api/v3/rename":                "movieId",
}

func (radarrRequiredParameters) Name() string    { return "radarr-required-parameters" }
func (radarrRequiredParameters) Service() string { return radarr }
func (radarrRequiredParameters) Bug() string {
	return "query parameters the server cannot answer without are declared optional: a lookup with no term, a rename preview with no film, a parse with no title"
}

func (radarrRequiredParameters) Apply(spec *openapi.Spec) error {
	for _, target := range openapi.SortedKeys(radarrRequired) {
		method, path, _ := strings.Cut(target, " ")
		op, err := pandorest.Operation(spec, method, path)
		if err != nil {
			return err
		}
		p := op.Parameter(openapi.InQuery, radarrRequired[target])
		if p == nil {
			return fmt.Errorf("%s no longer declares %s", target, radarrRequired[target])
		}
		if p.Required {
			return fmt.Errorf("%s declares %s required now", target, radarrRequired[target])
		}
		p.Required = true
	}

	return nil
}

// radarrWriteResponses declares what the writes that declare no content
// answer. Most are the controllers that return object: the editors and bulk
// edits answer what they changed, a grab the release it grabbed, a test of
// every provider one result each, and a provider action whatever that
// provider's action returns (null for an action it does not have).
//
// Two things about it are not ready-made, so it is one written by hand
// around the one that is: a test of every provider answers a schema the
// document does not have, which is added first, and answers the same list
// with 400 when any of them fails, which is declared after.
type radarrWriteResponses struct{}

// providerTestAllResult is Radarr's ProviderTestAllResult: a provider's id,
// and what failed when it was tested. The failures are FluentValidation's
// own, with a severity rather than the isWarning a single test's carry.
const providerTestAllResult = "ProviderTestAllResult"

// radarrTestAll are the tests of every provider.
var radarrTestAll = each(http.MethodPost, "/api/v3/downloadclient/testall", "/api/v3/importlist/testall", "/api/v3/indexer/testall", "/api/v3/metadata/testall", "/api/v3/notification/testall")

var radarrWriteAnswers = pandorest.UndeclaredAnswers(pandorest.About{Name: radarrWriteResponses{}.Name(), Service: radarr, Bug: radarrWriteResponses{}.Bug()}, map[string]pandorest.Answer{
	"POST /api/v3/downloadclient/action/{name}": pandorest.JSON(),
	"POST /api/v3/importlist/action/{name}":     pandorest.JSON(),
	"POST /api/v3/indexer/action/{name}":        pandorest.JSON(),
	"POST /api/v3/metadata/action/{name}":       pandorest.JSON(),
	"POST /api/v3/notification/action/{name}":   pandorest.JSON(),

	// a test of every provider answers 400 Bad Request, with the same
	// list, when any of them fails
	"POST /api/v3/downloadclient/testall": pandorest.ListOf(providerTestAllResult),
	"POST /api/v3/importlist/testall":     pandorest.ListOf(providerTestAllResult),
	"POST /api/v3/indexer/testall":        pandorest.ListOf(providerTestAllResult),
	"POST /api/v3/metadata/testall":       pandorest.ListOf(providerTestAllResult),
	"POST /api/v3/notification/testall":   pandorest.ListOf(providerTestAllResult),

	"POST /api/v3/exclusions/bulk":         pandorest.ListOf("ImportListExclusionResource"),
	"POST /api/v3/importlist/movie":        pandorest.ListOf("MovieResource"),
	"POST /api/v3/manualimport":            pandorest.ListOf("ManualImportReprocessResource"),
	"POST /api/v3/release":                 pandorest.Model("ReleaseResource"),
	"PUT /api/v3/collection":               pandorest.ListOf("CollectionResource"),
	"PUT /api/v3/movie/editor":             pandorest.ListOf("MovieResource"),
	"PUT /api/v3/moviefile/bulk":           pandorest.ListOf("MovieFileResource"),
	"PUT /api/v3/moviefile/editor":         pandorest.ListOf("MovieFileResource"),
	"PUT /api/v3/qualitydefinition/update": pandorest.ListOf("QualityDefinitionResource"),
})

var radarrTestSchemas = map[string]*openapi.Schema{
	providerTestAllResult: {Type: openapi.TypeObject, Properties: map[string]*openapi.Schema{
		"id":                 {Type: openapi.TypeInteger, Format: "int32"},
		"isValid":            {Type: openapi.TypeBoolean},
		"validationFailures": {Type: openapi.TypeArray, Items: &openapi.Schema{Ref: openapi.SchemaRefPrefix + "ValidationFailure"}},
	}},
	"ValidationFailure": {Type: openapi.TypeObject, Properties: map[string]*openapi.Schema{
		"propertyName":        {Type: openapi.TypeString, Nullable: true},
		"errorMessage":        {Type: openapi.TypeString, Nullable: true},
		"severity":            {Type: openapi.TypeString, Description: "error, warning or info"},
		"isWarning":           {Type: openapi.TypeBoolean},
		"detailedDescription": {Type: openapi.TypeString, Nullable: true},
		"infoLink":            {Type: openapi.TypeString, Nullable: true},
	}},
}

func (radarrWriteResponses) Name() string    { return "radarr-write-responses" }
func (radarrWriteResponses) Service() string { return radarr }
func (radarrWriteResponses) Bug() string {
	return "the writes whose controllers return object declare a success with no content, though they answer what they changed: the editors and bulk edits their resources, a grab its release, a manual import's reprocess its candidates, a test of every provider a result each (and 400 when one fails)"
}

func (radarrWriteResponses) Apply(spec *openapi.Spec) error {
	for _, name := range openapi.SortedKeys(radarrTestSchemas) {
		if spec.Components.Schemas[name] != nil {
			return fmt.Errorf("the document has a %s schema now", name)
		}
		spec.Components.Schemas[name] = radarrTestSchemas[name]
	}
	if err := radarrWriteAnswers.Apply(spec); err != nil {
		return err
	}

	// a test of every provider answers the same list with 400 when any of them fails, which is an answer to read and not a refusal
	for _, target := range radarrTestAll {
		method, path, _ := strings.Cut(target, " ")
		op, err := pandorest.Operation(spec, method, path)
		if err != nil {
			return err
		}
		if op.Responses["400"] != nil {
			return fmt.Errorf("%s declares its 400 now", target)
		}
		results := &openapi.Schema{Type: openapi.TypeArray, Items: &openapi.Schema{Ref: openapi.SchemaRefPrefix + providerTestAllResult}}
		op.Responses["400"] = &openapi.Response{Description: "At least one provider failed its test", Content: map[string]*openapi.MediaType{"application/json": {Schema: results}}}
	}

	return nil
}

// radarrBulkResponses makes the bulk provider edits answer lists. Each
// declares the one resource the single edit answers, but a bulk edit answers
// every provider it changed.
var radarrBulkResponses = pandorest.WrongAnswers(pandorest.About{
	Name: "radarr-bulk-responses", Service: radarr,
	Bug: "the bulk edits of custom formats, download clients, import lists and indexers declare one resource, but answer a list of every one they changed",
}, map[string]pandorest.Correction{
	"PUT /api/v3/customformat/bulk":   {Declared: pandorest.Model("CustomFormatResource"), Answers: pandorest.ListOf("CustomFormatResource")},
	"PUT /api/v3/downloadclient/bulk": {Declared: pandorest.Model("DownloadClientResource"), Answers: pandorest.ListOf("DownloadClientResource")},
	"PUT /api/v3/importlist/bulk":     {Declared: pandorest.Model("ImportListResource"), Answers: pandorest.ListOf("ImportListResource")},
	"PUT /api/v3/indexer/bulk":        {Declared: pandorest.Model("IndexerResource"), Answers: pandorest.ListOf("IndexerResource")},
})

// radarrHostRequired marks the host settings Radarr cannot save without.
// The host settings are saved field by field into config.xml, and Radarr
// compares each value it is sent with the one it has by calling ToString on
// it: a field sent as null (or left out, which is the same thing) crashes
// the save with a NullReferenceException, and a few more fail validation.
// The document calls them all optional and nullable, and the models leave
// an empty string out, so the settings Radarr starts with - no URL base, no
// certificate - could never be sent back.
type radarrHostRequired struct{}

// radarrHostFields are the host settings that must be sent: without one,
// Radarr answers 500 (consoleLogLevel, instanceName, logLevel,
// sslCertPassword, sslCertPath, updateScriptPath, urlBase) or 400
// (allowedHosts, bindAddress, branch). allowedHosts is one the document does
// not declare at all (radarrUndeclaredFields); a required list may name it
// either way.
var radarrHostFields = []string{
	"allowedHosts", "bindAddress", "branch", "consoleLogLevel", "instanceName",
	"logLevel", "sslCertPassword", "sslCertPath", "updateScriptPath", "urlBase",
}

func (radarrHostRequired) Name() string    { return "radarr-host-required" }
func (radarrHostRequired) Service() string { return radarr }
func (radarrHostRequired) Bug() string {
	return "the host settings are all optional and nullable in the document, but a save without one of ten of them fails, most with a 500 from a NullReferenceException; the empty ones Radarr starts with (URL base, certificate path) would never be sent"
}

func (radarrHostRequired) Apply(spec *openapi.Spec) error {
	s := spec.Components.Schemas["HostConfigResource"]
	if s == nil {
		return errors.New("no schema HostConfigResource")
	}
	if len(s.Required) > 0 {
		return fmt.Errorf("HostConfigResource requires %v now", s.Required)
	}
	for _, name := range radarrHostFields {
		if p := s.Properties[name]; p != nil {
			p.Nullable = false
		}
	}
	s.Required = slices.Clone(radarrHostFields)

	return nil
}

// radarrPutIDs makes the id a PUT takes in its path an integer. The REST
// controllers' updates take the resource's id as a string route value (a
// quirk of how the RestPutById attribute binds it), so Swashbuckle calls
// them strings, while every GET, DELETE and POST by id, and the other
// updates, declare the same ids as int32; a string there puts a strconv
// call at every call site for an id that is always a number.
type radarrPutIDs struct{}

func (radarrPutIDs) Name() string    { return "radarr-put-ids" }
func (radarrPutIDs) Service() string { return radarr }
func (radarrPutIDs) Bug() string {
	return "21 updates declare the id in their path as a string, where every other operation by id declares it an int32: the same ids, which are always numbers"
}

func (radarrPutIDs) Apply(spec *openapi.Spec) error {
	n := 0
	for _, path := range openapi.SortedKeys(spec.Paths) {
		op := spec.Operation(http.MethodPut, path)
		if op == nil {
			continue
		}
		p := op.Parameter(openapi.InPath, "id")
		if p == nil || p.Schema == nil || p.Schema.Type != openapi.TypeString || p.Schema.Format != "" {
			continue
		}
		p.Schema = &openapi.Schema{Type: openapi.TypeInteger, Format: "int32"}
		n++
	}
	if n == 0 {
		return errors.New("no PUT declares its path id a string")
	}

	return nil
}

// radarrAuthRedirects declares what the login form and the logout answer: a
// redirect, and nothing else. A login is sent to where it asked to land, or
// back to the form marked failed, and a logout to the front page; the
// document says 200 for both, which is the page a client that follows the
// redirect lands on.
var radarrAuthRedirects = pandorest.WrongStatus(pandorest.About{
	Name: "radarr-auth-redirects", Service: radarr,
	Bug: "the login form and the logout are documented as answering 200, but each answers 302 Found: a login to where it asked to land, or back to the form with loginFailed=true, and a logout to the front page",
}, http.StatusOK, map[int][]string{http.StatusFound: {"POST /login", "GET /logout"}})
