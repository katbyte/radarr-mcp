package workarounds

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/katbyte/radarr-mcp/internal/pandorest/openapi"
)

// The Radarr document is the one Radarr's own repository carries at
// src/Radarr.Api.V3/openapi.json, written by Swashbuckle from the
// controllers. Swashbuckle can only describe what a controller's signature
// says, so every action that returns object or IActionResult comes out with
// an empty 200. What follows was found by the integration suite against the
// server itself.

const radarr = "radarr"

// radarrUndeclaredResponses declares what the GETs without a response schema
// answer. A file is a content type; json is JSON of no declared schema; []X
// is a list of the component schema X; any other value is the component
// schema the JSON decodes into.
type radarrUndeclaredResponses struct{}

const (
	answersJSON = "json"
	textHTML    = "text/html"
	textPlain   = "text/plain"
	octetStream = "application/octet-stream"
)

var radarrUndeclared = map[string]string{
	// JSON the controllers build as anonymous objects
	"/api/v3/autotagging/schema":      answersJSON,
	"/api/v3/config/naming/examples":  answersJSON,
	"/api/v3/customformat/schema":     answersJSON,
	"/api/v3/filesystem":              answersJSON,
	"/api/v3/filesystem/mediafiles":   answersJSON,
	"/api/v3/filesystem/type":         answersJSON,
	"/api/v3/importlist/movie":        answersJSON,
	"/api/v3/movie/{id}/folder":       answersJSON,
	"/api/v3/system/routes/duplicate": answersJSON,
	// every route Radarr serves, as a graphviz digraph in plain text
	"/api/v3/system/routes": textPlain,
	// a movie's cast and crew, the same resource GET /api/v3/credit/{id} answers
	"/api/v3/credit": "[]CreditResource",

	// files
	"/":                                  textHTML,
	"/login":                             textHTML,
	"/logout":                            textHTML,
	"/{path}":                            octetStream,
	"/content/{path}":                    octetStream,
	"/api/v3/log/file/{filename}":        textPlain,
	"/api/v3/log/file/update/{filename}": textPlain,
	"/api/v3/mediacover/{movieId}/{filename}": "image/*",
	"/feed/v3/calendar/radarr.ics":            "text/calendar",
}

func (radarrUndeclaredResponses) Name() string    { return "radarr-undeclared-responses" }
func (radarrUndeclaredResponses) Service() string { return radarr }
func (radarrUndeclaredResponses) Bug() string {
	return "the GETs whose controllers return object or IActionResult declare a 200 with no content, so nothing says whether they answer JSON (and in what shape) or a file"
}

func (radarrUndeclaredResponses) Apply(spec *openapi.Spec) error {
	var declared []string
	for _, path := range openapi.SortedKeys(radarrUndeclared) {
		op, err := operation(spec, http.MethodGet, path)
		if err != nil {
			return err
		}
		ok := op.Responses["200"]
		if ok == nil {
			return fmt.Errorf("GET %s has no 200 response", path)
		}
		if len(ok.Content) > 0 {
			declared = append(declared, path)
			continue
		}
		if ok.Content, err = answerContent(spec, radarrUndeclared[path]); err != nil {
			return fmt.Errorf("GET %s: %w", path, err)
		}
	}
	if len(declared) > 0 {
		return fmt.Errorf("these now declare their response, so take them out of the table: %s", strings.Join(declared, ", "))
	}

	return nil
}

// answerContent is the content of a response radarrUndeclared describes.
func answerContent(spec *openapi.Spec, answer string) (map[string]*openapi.MediaType, error) {
	switch {
	case answer == answersJSON:
		return map[string]*openapi.MediaType{"application/json": {}}, nil
	case strings.HasPrefix(answer, "[]"):
		item := strings.TrimPrefix(answer, "[]")
		if spec.Components.Schemas[item] == nil {
			return nil, fmt.Errorf("schema %s is not in the document", item)
		}
		return map[string]*openapi.MediaType{"application/json": {Schema: &openapi.Schema{
			Type: openapi.TypeArray, Items: &openapi.Schema{Ref: openapi.SchemaRefPrefix + item},
		}}}, nil
	case strings.Contains(answer, "/"):
		return map[string]*openapi.MediaType{answer: {Schema: &openapi.Schema{Type: openapi.TypeString, Format: "binary"}}}, nil
	default:
		if spec.Components.Schemas[answer] == nil {
			return nil, fmt.Errorf("schema %s is not in the document", answer)
		}
		return map[string]*openapi.MediaType{"application/json": {Schema: &openapi.Schema{Ref: openapi.SchemaRefPrefix + answer}}}, nil
	}
}

// success is an operation's one success response, whatever its status: the
// document says 200, and radarrStatuses moves the creates and updates to
// what they answer. Every workaround must apply to the document as it is,
// so none can assume another has run.
func success(op *openapi.Operation, target string) (*openapi.Response, error) {
	var found *openapi.Response
	for _, status := range []string{"200", "201", "202"} {
		r := op.Responses[status]
		if r == nil {
			continue
		}
		if found != nil {
			return nil, fmt.Errorf("%s documents more than one success", target)
		}
		found = r
	}
	if found == nil {
		return nil, fmt.Errorf("%s documents no success", target)
	}

	return found, nil
}

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
	op, err := operation(spec, http.MethodGet, "/")
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
	op, err := operation(spec, http.MethodPost, "/api/v3/command")
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

// radarrStatuses declares the statuses the writes answer. Radarr's REST
// controllers answer a create with 201 Created and an update with 202
// Accepted, both carrying the resource, but the document says 200 for all of
// them, so every create and update would come back as an undocumented
// status.
type radarrStatuses struct{}

// radarrCreated are the creates, answered 201 with the new resource.
var radarrCreated = []string{
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
}

// radarrAccepted are the updates, answered 202 with what they changed.
var radarrAccepted = []string{
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
}

func (radarrStatuses) Name() string    { return "radarr-statuses" }
func (radarrStatuses) Service() string { return radarr }
func (radarrStatuses) Bug() string {
	return "every create is documented as 200 but answers 201 Created, and every update (the bulk and editor ones too) as 200 but answers 202 Accepted"
}

func (radarrStatuses) Apply(spec *openapi.Spec) error {
	for _, set := range []struct {
		method string
		paths  []string
		status string
	}{
		{http.MethodPost, radarrCreated, "201"},
		{http.MethodPut, radarrAccepted, "202"},
	} {
		for _, path := range set.paths {
			op, err := operation(spec, set.method, path)
			if err != nil {
				return err
			}
			ok := op.Responses["200"]
			if ok == nil {
				return fmt.Errorf("%s %s no longer documents a 200", set.method, path)
			}
			if op.Responses[set.status] != nil {
				return fmt.Errorf("%s %s already documents a %s", set.method, path, set.status)
			}
			delete(op.Responses, "200")
			op.Responses[set.status] = ok
		}
	}

	return nil
}

// radarrTextPlainTwin drops the text/plain Swashbuckle lists beside every
// JSON response of a controller that returns an object: ASP.NET's formatters
// could render a string that way, but these actions return resources, and
// Radarr answers them with JSON whatever the Accept header says. Left in,
// the text/plain twin reads as "this operation answers a file".
type radarrTextPlainTwin struct{}

func (radarrTextPlainTwin) Name() string    { return "radarr-text-plain-twin" }
func (radarrTextPlainTwin) Service() string { return radarr }
func (radarrTextPlainTwin) Bug() string {
	return "about a hundred JSON responses also list text/plain, which the server never sends for them: they answer JSON whatever is asked for"
}

func (radarrTextPlainTwin) Apply(spec *openapi.Spec) error {
	n := 0
	for _, path := range openapi.SortedKeys(spec.Paths) {
		for _, m := range spec.Paths[path].Methods() {
			for _, resp := range m.Operation.Responses {
				if resp.Content["text/plain"] == nil || resp.Content["application/json"] == nil {
					continue
				}
				delete(resp.Content, "text/plain")
				n++
			}
		}
	}
	if n == 0 {
		return errors.New("no response lists text/plain beside application/json")
	}

	return nil
}

// radarrUndeclaredFields adds the fields Radarr sends that its document does
// not declare: the document was written before them, and a model without
// them drops what the server says, and sends a resource back without them.
// The read sweep's strict decode finds each one.
type radarrUndeclaredFields struct{}

// radarrFields are the properties to add, by schema.
var radarrFields = map[string]map[string]*openapi.Schema{
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
}

func (radarrUndeclaredFields) Name() string    { return "radarr-undeclared-fields" }
func (radarrUndeclaredFields) Service() string { return radarr }
func (radarrUndeclaredFields) Bug() string {
	return "the server sends fields the document does not declare: allowedHosts and trustedNetworks on the host config, isContainerized on the system status, movieFileQualities on a film's statistics, isExcluded on a film a lookup finds"
}

func (radarrUndeclaredFields) Apply(spec *openapi.Spec) error {
	for _, schema := range openapi.SortedKeys(radarrFields) {
		s := spec.Components.Schemas[schema]
		if s == nil {
			return fmt.Errorf("no schema %s", schema)
		}
		for _, name := range openapi.SortedKeys(radarrFields[schema]) {
			if s.Properties[name] != nil {
				return fmt.Errorf("%s declares %s now", schema, name)
			}
			if s.Properties == nil {
				s.Properties = map[string]*openapi.Schema{}
			}
			s.Properties[name] = radarrFields[schema][name]
		}
	}

	return nil
}

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
	body, err := property(spec, "CommandResource", "body")
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
		op, err := operation(spec, method, path)
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
// answer, in radarrUndeclared's terms. Most are the controllers that return
// object: the editors and bulk edits answer what they changed, a grab the
// release it grabbed, a test of every provider one result each, and a
// provider action whatever that provider's action returns (null for an
// action it does not have).
type radarrWriteResponses struct{}

var radarrWriteAnswers = map[string]string{
	"POST /api/v3/downloadclient/action/{name}": answersJSON,
	"POST /api/v3/importlist/action/{name}":     answersJSON,
	"POST /api/v3/indexer/action/{name}":        answersJSON,
	"POST /api/v3/metadata/action/{name}":       answersJSON,
	"POST /api/v3/notification/action/{name}":   answersJSON,

	// a test of every provider answers 400 Bad Request, with the same
	// list, when any of them fails
	"POST /api/v3/downloadclient/testall": "[]" + providerTestAllResult,
	"POST /api/v3/importlist/testall":     "[]" + providerTestAllResult,
	"POST /api/v3/indexer/testall":        "[]" + providerTestAllResult,
	"POST /api/v3/metadata/testall":       "[]" + providerTestAllResult,
	"POST /api/v3/notification/testall":   "[]" + providerTestAllResult,

	"POST /api/v3/exclusions/bulk":         "[]ImportListExclusionResource",
	"POST /api/v3/importlist/movie":        "[]MovieResource",
	"POST /api/v3/manualimport":            "[]ManualImportReprocessResource",
	"POST /api/v3/release":                 "ReleaseResource",
	"PUT /api/v3/collection":               "[]CollectionResource",
	"PUT /api/v3/movie/editor":             "[]MovieResource",
	"PUT /api/v3/moviefile/bulk":           "[]MovieFileResource",
	"PUT /api/v3/moviefile/editor":         "[]MovieFileResource",
	"PUT /api/v3/qualitydefinition/update": "[]QualityDefinitionResource",
}

// providerTestAllResult is Radarr's ProviderTestAllResult: a provider's id,
// and what failed when it was tested. The failures are FluentValidation's
// own, with a severity rather than the isWarning a single test's carry.
const providerTestAllResult = "ProviderTestAllResult"

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
	for _, target := range openapi.SortedKeys(radarrWriteAnswers) {
		method, path, _ := strings.Cut(target, " ")
		op, err := operation(spec, method, path)
		if err != nil {
			return err
		}
		ok, err := success(op, target)
		if err != nil {
			return err
		}
		if len(ok.Content) > 0 {
			return fmt.Errorf("%s declares its response now", target)
		}
		if ok.Content, err = answerContent(spec, radarrWriteAnswers[target]); err != nil {
			return fmt.Errorf("%s: %w", target, err)
		}
	}

	return nil
}

// radarrBulkResponses makes the bulk provider edits answer lists. Each
// declares the one resource the single edit answers, but a bulk edit answers
// every provider it changed.
type radarrBulkResponses struct{}

var radarrBulk = map[string]string{
	"/api/v3/customformat/bulk":   "CustomFormatResource",
	"/api/v3/downloadclient/bulk": "DownloadClientResource",
	"/api/v3/importlist/bulk":     "ImportListResource",
	"/api/v3/indexer/bulk":        "IndexerResource",
}

func (radarrBulkResponses) Name() string    { return "radarr-bulk-responses" }
func (radarrBulkResponses) Service() string { return radarr }
func (radarrBulkResponses) Bug() string {
	return "the bulk edits of custom formats, download clients, import lists and indexers declare one resource, but answer a list of every one they changed"
}

func (radarrBulkResponses) Apply(spec *openapi.Spec) error {
	for _, path := range openapi.SortedKeys(radarrBulk) {
		target := http.MethodPut + " " + path
		op, err := operation(spec, http.MethodPut, path)
		if err != nil {
			return err
		}
		ok, err := success(op, target)
		if err != nil {
			return err
		}
		media := ok.Content["application/json"]
		if media == nil || media.Schema == nil || media.Schema.RefName() != radarrBulk[path] {
			return fmt.Errorf("%s no longer declares a single %s", target, radarrBulk[path])
		}
		media.Schema = &openapi.Schema{Type: openapi.TypeArray, Items: media.Schema}
	}

	return nil
}

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
