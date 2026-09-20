package fakeindexer

import (
	"encoding/xml"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
)

func offering(t *testing.T) *Indexer {
	t.Helper()

	ix, err := Start("127.0.0.1:0", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ix.Close() })
	ix.Offer(
		Release{Title: "Moon.2009.1080p.BluRay.x264-GRP", TmdbID: 17431, ImdbID: "tt1182345", Size: 8 << 30},
		Release{Title: "Moon.2009.720p.HDTV.x264-GRP", TmdbID: 17431, ImdbID: "tt1182345", Size: 4 << 30},
		Release{Title: "Heat.1995.1080p.BluRay.x264-GRP", TmdbID: 949, Size: 9 << 30},
	)

	return ix
}

func get(t *testing.T, url string) (status int, body string) {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}

	return res.StatusCode, string(raw)
}

// feed is the part of a Newznab search answer Radarr reads.
type feed struct {
	Items []struct {
		Title     string `xml:"title"`
		GUID      string `xml:"guid"`
		Enclosure struct {
			URL    string `xml:"url,attr"`
			Length int64  `xml:"length,attr"`
		} `xml:"enclosure"`
	} `xml:"channel>item"`
}

func search(t *testing.T, ix *Indexer, query string) []string {
	t.Helper()

	status, body := get(t, ix.BaseURL()+"/api?apikey="+ix.APIKey+"&"+query)
	if status != http.StatusOK {
		t.Fatalf("%s: HTTP %d", query, status)
	}
	var f feed
	if err := xml.Unmarshal([]byte(body), &f); err != nil {
		t.Fatalf("%s: %v\n%s", query, err, body)
	}
	titles := make([]string, 0, len(f.Items))
	for _, it := range f.Items {
		titles = append(titles, it.Title)
		if !strings.HasPrefix(it.Enclosure.URL, ix.BaseURL()+"/download/") || it.Enclosure.Length == 0 {
			t.Errorf("%s: enclosure %+v", it.Title, it.Enclosure)
		}
	}
	slices.Sort(titles)

	return titles
}

func TestSearches(t *testing.T) {
	t.Parallel()

	ix := offering(t)
	moon := []string{"Moon.2009.1080p.BluRay.x264-GRP", "Moon.2009.720p.HDTV.x264-GRP"}
	for query, want := range map[string][]string{
		"t=movie&tmdbid=17431":         moon,
		"t=movie&imdbid=1182345":       moon,
		"t=movie&imdbid=tt1182345":     moon,
		"t=movie&tmdbid=1":             nil,
		"t=search&q=heat+1995":         {"Heat.1995.1080p.BluRay.x264-GRP"},
		"t=search&q=moon+720p":         {"Moon.2009.720p.HDTV.x264-GRP"},
		"t=movie&cat=2000,2040":        append(slices.Clone(moon), "Heat.1995.1080p.BluRay.x264-GRP"),
		"t=search&q=nothing+like+this": nil,
	} {
		got := search(t, ix, query)
		want = slices.Clone(want)
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Errorf("%s = %v, want %v", query, got, want)
		}
	}

	ix.Withdraw("Heat.1995.1080p.BluRay.x264-GRP")
	if got := search(t, ix, "t=search&q=heat"); len(got) != 0 {
		t.Errorf("a withdrawn release is still offered: %v", got)
	}
}

func TestCapsAndKeys(t *testing.T) {
	t.Parallel()

	ix := offering(t)
	status, body := get(t, ix.BaseURL()+"/api?t=caps&apikey="+ix.APIKey)
	if status != http.StatusOK || !strings.Contains(body, `supportedParams="q,imdbid,tmdbid"`) {
		t.Errorf("caps = %d %s", status, body)
	}
	if _, body := get(t, ix.BaseURL()+"/api?t=caps&apikey=wrong"); !strings.Contains(body, `code="100"`) {
		t.Errorf("a wrong key = %s", body)
	}
	if _, body := get(t, ix.BaseURL()+"/api?t=music&apikey="+ix.APIKey); !strings.Contains(body, `code="202"`) {
		t.Errorf("an unknown function = %s", body)
	}
	if len(ix.Requests()) != 3 || ix.Requests()[0].Query["t"] != "caps" {
		t.Errorf("requests = %v", ix.Requests())
	}
}

func TestDownload(t *testing.T) {
	t.Parallel()

	ix := offering(t)
	status, body := get(t, ix.BaseURL()+"/download/moon.2009.1080p.bluray.x264-grp?apikey="+ix.APIKey)
	if status != http.StatusOK {
		t.Fatalf("download = %d", status)
	}
	var nzb struct {
		Files []struct {
			Subject  string `xml:"subject,attr"`
			Segments []struct {
				ID string `xml:",chardata"`
			} `xml:"segments>segment"`
		} `xml:"file"`
	}
	if err := xml.Unmarshal([]byte(body), &nzb); err != nil || len(nzb.Files) != 1 || len(nzb.Files[0].Segments) != 1 {
		t.Fatalf("the nzb = %v, %+v\n%s", err, nzb, body)
	}
	if !strings.Contains(nzb.Files[0].Subject, "Moon.2009.1080p.BluRay.x264-GRP.mkv") || !strings.HasPrefix(nzb.Files[0].Segments[0].ID, "Moon.2009.1080p.BluRay.x264.GRP@") {
		t.Errorf("the nzb = %+v", nzb)
	}
	if got := ix.Grabbed(); !slices.Equal(got, []string{"Moon.2009.1080p.BluRay.x264-GRP"}) {
		t.Errorf("grabbed = %v", got)
	}
	if status, _ := get(t, ix.BaseURL()+"/download/nothing?apikey="+ix.APIKey); status != http.StatusNotFound {
		t.Errorf("an unknown release = %d", status)
	}
	if status, _ := get(t, ix.BaseURL()+"/download/moon.2009.1080p.bluray.x264-grp"); status != http.StatusUnauthorized {
		t.Errorf("a download with no key = %d", status)
	}
}
