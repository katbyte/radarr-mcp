// Package fakeindexer is a Newznab indexer the live test suites run, so the
// Radarr in the container has something to search, grab from and follow
// into its queue: the one part of Radarr's world a recorded cassette cannot
// stand in for, because what an indexer offers is what a test decides.
//
// Radarr reaches it the way it reaches a real indexer - the tests add it as
// a Newznab indexer at Addr - and sends a grab to a Usenet Blackhole
// download client, which writes the release's .nzb into a folder and imports
// whatever appears in another: a download a test finishes by putting a file
// there.
//
// It answers the three things Radarr asks a Newznab indexer: its
// capabilities (t=caps), a search (t=movie by imdb or tmdb id, t=search by
// text, and the same with no terms for the RSS sync), and the .nzb of a
// release.
package fakeindexer

import (
	"context"
	"encoding/xml"
	"fmt"
	"html"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Release is one release the indexer offers.
type Release struct {
	// Title is the release name Radarr parses, e.g.
	// Moon.2009.1080p.BluRay.x264-GRP.
	Title string
	// TmdbID and ImdbID are what a search by id matches; ImdbID with or
	// without its tt.
	TmdbID int
	ImdbID string
	// Size is in bytes; Radarr rejects a release too small or too large for
	// its quality, so a believable one matters.
	Size int64
	// Published is when the indexer says it was posted; zero is a day before
	// it was offered, and it stays put from one search to the next.
	Published time.Time
	// Category is the Newznab category, 2040 (HD movies) when zero.
	Category int
}

// guid is the release's id on this indexer, stable across searches so a
// grab can name what a search returned.
func (r Release) guid() string {
	return strings.ToLower(strings.NewReplacer(" ", ".", "/", ".").Replace(r.Title))
}

// Request is one request Radarr made, for a test to assert on.
type Request struct {
	Path  string
	Query map[string]string
}

// Indexer is a running fake indexer.
type Indexer struct {
	// APIKey is what Radarr must send; anything else is refused the way a
	// real indexer refuses a wrong key.
	APIKey string

	srv      *http.Server
	listener net.Listener
	host     string

	mu       sync.Mutex
	releases []Release
	seen     []Request
	grabbed  []string
}

// Start listens on addr (":17881" for every interface, which a container
// needs) and serves until Close. host is how Radarr reaches it, e.g.
// host.docker.internal: the release links it hands out point there.
func Start(addr, host string) (*Indexer, error) {
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", addr)
	if err != nil {
		return nil, err
	}
	ix := &Indexer{APIKey: "fakeindexer-key", listener: ln, host: host} //nolint:gosec // the fake's own key, which Radarr is given to send back
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api", ix.api)
	mux.HandleFunc("GET /download/{guid}", ix.download)
	ix.srv = &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = ix.srv.Serve(ln) }()

	return ix, nil
}

// Port is the port the indexer listens on.
func (ix *Indexer) Port() int {
	addr, ok := ix.listener.Addr().(*net.TCPAddr)
	if !ok {
		return 0
	}

	return addr.Port
}

// BaseURL is the address Radarr is given for the indexer.
func (ix *Indexer) BaseURL() string {
	return "http://" + net.JoinHostPort(ix.host, strconv.Itoa(ix.Port()))
}

// Close stops the indexer.
func (ix *Indexer) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	return ix.srv.Shutdown(ctx)
}

// Offer adds releases the indexer answers searches with.
func (ix *Indexer) Offer(releases ...Release) {
	ix.mu.Lock()
	defer ix.mu.Unlock()

	for _, r := range releases {
		// a release keeps the date it was posted: Radarr recognises a
		// blocklisted usenet release by it, so one that moved each search
		// would never be blocklisted
		if r.Published.IsZero() {
			r.Published = time.Now().Add(-24 * time.Hour).Truncate(time.Second)
		}
		if !slices.ContainsFunc(ix.releases, func(have Release) bool { return have.guid() == r.guid() }) {
			ix.releases = append(ix.releases, r)
		}
	}
}

// Withdraw stops offering releases by title.
func (ix *Indexer) Withdraw(titles ...string) {
	ix.mu.Lock()
	defer ix.mu.Unlock()

	ix.releases = slices.DeleteFunc(ix.releases, func(r Release) bool { return slices.Contains(titles, r.Title) })
}

// Requests returns what Radarr asked, in order.
func (ix *Indexer) Requests() []Request {
	ix.mu.Lock()
	defer ix.mu.Unlock()

	return slices.Clone(ix.seen)
}

// Grabbed returns the release titles whose .nzb Radarr fetched: what it
// grabbed.
func (ix *Indexer) Grabbed() []string {
	ix.mu.Lock()
	defer ix.mu.Unlock()

	return slices.Clone(ix.grabbed)
}

func (ix *Indexer) record(r *http.Request) {
	q := map[string]string{}
	for k, v := range r.URL.Query() {
		q[k] = strings.Join(v, ",")
	}
	ix.mu.Lock()
	ix.seen = append(ix.seen, Request{Path: r.URL.Path, Query: q})
	ix.mu.Unlock()
}

// api answers the Newznab API: caps, and the searches.
func (ix *Indexer) api(w http.ResponseWriter, r *http.Request) {
	ix.record(r)
	q := r.URL.Query()
	if q.Get("apikey") != ix.APIKey {
		writeError(w, 100, "Incorrect user credentials")
		return
	}
	switch q.Get("t") {
	case "caps":
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(caps))
	case "movie", "search":
		ix.search(w, r)
	default:
		writeError(w, 202, "No such function")
	}
}

// search answers a search: by tmdb or imdb id, by text, or - with neither -
// everything, which is what the RSS sync asks for.
func (ix *Indexer) search(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	tmdb, _ := strconv.Atoi(q.Get("tmdbid"))
	imdb := strings.TrimPrefix(strings.ToLower(q.Get("imdbid")), "tt")
	text := strings.ToLower(q.Get("q"))

	ix.mu.Lock()
	var hits []Release
	for _, rel := range ix.releases {
		switch {
		case tmdb != 0 && rel.TmdbID == tmdb,
			imdb != "" && strings.TrimPrefix(strings.ToLower(rel.ImdbID), "tt") == imdb,
			text != "" && tmdb == 0 && imdb == "" && matchesText(rel.Title, text),
			tmdb == 0 && imdb == "" && text == "":
			hits = append(hits, rel)
		}
	}
	ix.mu.Unlock()

	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<rss version="2.0" xmlns:newznab="http://www.newznab.com/DTD/2010/feeds/attributes/"><channel><title>radarr-mcp fake indexer</title>` + "\n")
	fmt.Fprintf(&b, `<newznab:response offset="0" total="%d"/>`+"\n", len(hits))
	for _, rel := range hits {
		b.WriteString(ix.item(rel))
	}
	b.WriteString("</channel></rss>\n")
	w.Header().Set("Content-Type", "application/rss+xml")
	_, _ = w.Write([]byte(b.String()))
}

// matchesText reports whether every word of a text search is in a title.
func matchesText(title, text string) bool {
	t := strings.ToLower(strings.NewReplacer(".", " ", "-", " ", "_", " ").Replace(title))
	for word := range strings.FieldsSeq(text) {
		if !strings.Contains(t, word) {
			return false
		}
	}

	return true
}

func (ix *Indexer) item(rel Release) string {
	published := rel.Published
	category := rel.Category
	if category == 0 {
		category = 2040
	}
	link := ix.BaseURL() + "/download/" + rel.guid() + "?apikey=" + ix.APIKey
	var b strings.Builder
	b.WriteString("<item>")
	fmt.Fprintf(&b, "<title>%s</title>", html.EscapeString(rel.Title))
	fmt.Fprintf(&b, `<guid isPermaLink="false">%s</guid>`, rel.guid())
	fmt.Fprintf(&b, "<link>%s</link>", html.EscapeString(link))
	fmt.Fprintf(&b, "<comments>%s/details/%s</comments>", ix.BaseURL(), rel.guid())
	fmt.Fprintf(&b, "<pubDate>%s</pubDate>", published.UTC().Format(time.RFC1123Z))
	fmt.Fprintf(&b, "<category>%d</category>", category)
	fmt.Fprintf(&b, `<enclosure url="%s" length="%d" type="application/x-nzb"/>`, html.EscapeString(link), rel.Size)
	fmt.Fprintf(&b, `<newznab:attr name="category" value="2000"/><newznab:attr name="category" value="%d"/>`, category)
	fmt.Fprintf(&b, `<newznab:attr name="size" value="%d"/>`, rel.Size)
	if rel.ImdbID != "" {
		fmt.Fprintf(&b, `<newznab:attr name="imdb" value="%s"/>`, strings.TrimPrefix(rel.ImdbID, "tt"))
	}
	if rel.TmdbID != 0 {
		fmt.Fprintf(&b, `<newznab:attr name="tmdbid" value="%d"/>`, rel.TmdbID)
	}
	b.WriteString("</item>\n")

	return b.String()
}

// download answers the .nzb of a release: one file of one segment, which is
// all Radarr's validation asks of it and all a blackhole needs.
func (ix *Indexer) download(w http.ResponseWriter, r *http.Request) {
	ix.record(r)
	if r.URL.Query().Get("apikey") != ix.APIKey {
		http.Error(w, "Incorrect user credentials", http.StatusUnauthorized)
		return
	}
	guid := strings.TrimSuffix(r.PathValue("guid"), ".nzb")
	ix.mu.Lock()
	i := slices.IndexFunc(ix.releases, func(rel Release) bool { return rel.guid() == guid })
	var rel Release
	if i >= 0 {
		rel = ix.releases[i]
		ix.grabbed = append(ix.grabbed, rel.Title)
	}
	ix.mu.Unlock()
	if i < 0 {
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Content-Type", "application/x-nzb")
	w.Header().Set("Content-Disposition", `attachment; filename="`+rel.Title+`.nzb"`)
	_, _ = fmt.Fprint(w, NZB(rel.Title))
}

// NZB is a minimal valid .nzb for a release: what Radarr fetches on a grab
// and validates before handing it to the download client.
func NZB(title string) string {
	var subject strings.Builder
	_ = xml.EscapeText(&subject, []byte(`"`+title+`.mkv" yEnc (1/1)`))

	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE nzb PUBLIC "-//newzBin//DTD NZB 1.1//EN" "http://www.newzbin.com/DTD/nzb/nzb-1.1.dtd">
<nzb xmlns="http://www.newzbin.com/DTD/2003/nzb">
  <file poster="fakeindexer@radarr-mcp.invalid" date="1758240000" subject="` + subject.String() + `">
    <groups><group>alt.binaries.radarr-mcp</group></groups>
    <segments><segment bytes="1024" number="1">` + messageID(title) + `@radarr-mcp.invalid</segment></segments>
  </file>
</nzb>
`
}

// messageID is a title as a Usenet message id: letters and digits, with
// dots for the rest.
func messageID(title string) string {
	return strings.Map(func(r rune) rune {
		if r < 128 && (r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return r
		}
		return '.'
	}, title)
}

// writeError answers a Newznab error, which comes with a 200.
func writeError(w http.ResponseWriter, code int, description string) {
	w.Header().Set("Content-Type", "application/xml")
	_, _ = fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?><error code="%d" description="%s"/>`, code, html.EscapeString(description))
}

// caps is what the indexer says it can do: search movies by text, imdb id
// and tmdb id, in the movie categories.
const caps = `<?xml version="1.0" encoding="UTF-8"?>
<caps>
  <server version="1.0" title="radarr-mcp fake indexer"/>
  <limits max="100" default="100"/>
  <searching>
    <search available="yes" supportedParams="q"/>
    <movie-search available="yes" supportedParams="q,imdbid,tmdbid"/>
  </searching>
  <categories>
    <category id="2000" name="Movies">
      <subcat id="2030" name="SD"/>
      <subcat id="2040" name="HD"/>
      <subcat id="2045" name="UHD"/>
    </category>
  </categories>
</caps>
`
