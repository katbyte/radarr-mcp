// Package config lists the services pandorest imports and generates, the
// equivalent of Pandora's resource-manager.hcl. Paths are relative to the
// repository root, which is where the make targets run pandorest from.
package config

import (
	"fmt"
	"path/filepath"
	"strings"
)

// Naming is how operations get their Go method names.
type Naming string

const (
	// PathNaming builds names from the HTTP method and path, for documents
	// with no operationIds at all (Radarr's: Swashbuckle writes none).
	PathNaming Naming = "path"
	// OperationIDNaming uses the operationId, for a document whose ids are
	// hand-written and unique. Radarr's document has none; the mode is kept
	// so the importer's tests can exercise both.
	OperationIDNaming Naming = "operationId"
)

// Service is one server API.
type Service struct {
	// Name is the -service flag and the definitions directory name.
	Name string
	// Package is the Go package name of the generated SDK.
	Package string
	// Spec is the vendored OpenAPI document.
	Spec string
	// Definitions is where the importer writes and the generator reads.
	Definitions string
	// Output is the generated package directory.
	Output string
	// Naming picks how method names are built.
	Naming Naming
	// TagSuffix is trimmed from tag names to make group names; Radarr's
	// tags need none.
	TagSuffix string
	// NamePrefix is the path prefix left out of method names: every Radarr
	// route but a handful sits under /api/v3, and GetApiV3MovieById says
	// nothing GetMovieById does not. The paths themselves keep it.
	NamePrefix string
	// Auth names the lib/client authorizer the generated New uses.
	Auth string

	// Root is the repository root the paths above are relative to; empty
	// is the working directory.
	Root string
}

// Services is every service, in the order the make targets process them.
// There is one: pandorest came from embyfin-mcp, which generates two SDKs,
// and keeps its shape so the two copies stay easy to compare.
var Services = []Service{
	{
		Name:        "radarr",
		Package:     "radarr",
		Spec:        "docs/radarr-openapi.json",
		Definitions: "api-definitions/radarr",
		Output:      "lib/radarr",
		Naming:      PathNaming,
		NamePrefix:  "/api/v3",
		Auth:        "Radarr",
	},
}

// Select returns the named services, or all of them for an empty list.
func Select(names string) ([]Service, error) {
	if names == "" {
		return Services, nil
	}
	var out []Service
	for name := range strings.SplitSeq(names, ",") {
		svc, ok := Find(strings.TrimSpace(name))
		if !ok {
			return nil, fmt.Errorf("unknown service %q (have %s)", name, strings.Join(serviceNames(), ", "))
		}
		out = append(out, svc)
	}

	return out, nil
}

// Find returns a service by name.
func Find(name string) (Service, bool) {
	for _, s := range Services {
		if s.Name == name {
			return s, true
		}
	}

	return Service{}, false
}

// In returns a copy of the service whose files are under root. The paths
// stay as configured, relative to the repository, because they are recorded
// in the definitions and the generated docs; Path resolves them.
func (s Service) In(root string) Service {
	s.Root = root

	return s
}

// Path resolves one of the service's paths under its root.
func (s Service) Path(p string) string { return filepath.Join(s.Root, p) }

func serviceNames() []string {
	out := make([]string, 0, len(Services))
	for _, s := range Services {
		out = append(out, s.Name)
	}

	return out
}
