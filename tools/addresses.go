package tools

import (
	"regexp"
	"strings"
)

// Radarr quotes an address back in what it says went wrong, with whatever
// the address carried still on it: an indexer that answers an error is
// reported as "HTTP request failed: [500:InternalServerError] [GET] at
// [http://indexer/api?t=movie&apikey=...]", the indexer's stored API key and
// all, and a download client's or a list's address the same way. Such words
// reach a caller in an error, and in an answer too: a test that fails is a
// row, a health check and a stuck download each have a message. So wherever
// the tools show what Radarr said, each address in it is shown without what
// it may carry a credential in.

// addresses finds each address in a piece of text: a scheme, "://" and what
// follows it up to a space, a quote or an angle bracket.
var addresses = regexp.MustCompile(`[A-Za-z][A-Za-z0-9+.-]*://[^\s"'<>]+`)

// addressShown is an address with what it may carry a credential in blanked:
// who it signs in as, the value of every parameter after its "?", and what
// follows its "#". The host, the path and the parameters' names stay.
func addressShown(raw string) string {
	// what closes a sentence or a bracket around the address is not part of it
	address := strings.TrimRight(raw, "].,;)")
	closing := raw[len(address):]

	rest, fragment, _ := strings.Cut(address, "#")
	rest, query, hasQuery := strings.Cut(rest, "?")
	scheme, rest, _ := strings.Cut(rest, "://")
	host, path, hasPath := strings.Cut(rest, "/")

	if at := strings.LastIndex(host, "@"); at >= 0 {
		host = hidden + host[at:]
	}
	shown := scheme + "://" + host
	if hasPath {
		shown += "/" + path
	}
	if hasQuery {
		params := strings.Split(query, "&")
		for i, param := range params {
			if name, _, valued := strings.Cut(param, "="); valued {
				params[i] = name + "=" + hidden
			}
		}
		shown += "?" + strings.Join(params, "&")
	}
	if fragment != "" {
		shown += "#" + hidden
	}

	return shown + closing
}

// addressesHidden is a piece of text with every address in it shown without
// what it may carry a credential in.
func addressesHidden(s string) string {
	if !strings.Contains(s, "://") {
		return s
	}

	return addresses.ReplaceAllStringFunc(s, addressShown)
}

// radarrSays is what Radarr said of something - why a test failed, why a
// download is stuck, what a health check found, a line of its log - made
// safe to show: cleaned as text from outside is, and its addresses shown
// without their credentials.
func radarrSays(s string) string { return addressesHidden(outsideProse(s)) }

// shownError is a failure whose words are shown without what an address in
// them carried, with the failure itself kept underneath.
type shownError struct {
	text string
	err  error
}

func (e shownError) Error() string { return e.text }
func (e shownError) Unwrap() error { return e.err }

// failureShown is a tool's failure as its caller is told it. go-kt takes an
// address's credentials out of every failure from the release after 0.6.1,
// and until that is taken it is done here.
func failureShown(err error) error {
	if err == nil {
		return nil
	}
	if shown := addressesHidden(err.Error()); shown != err.Error() {
		return shownError{text: shown, err: err}
	}

	return err
}
