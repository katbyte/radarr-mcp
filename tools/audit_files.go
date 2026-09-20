package tools

// The audits of what is inside the files: Radarr probes every file it
// imports, and these compare what the probe found with what the file is
// supposed to be.

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/katbyte/radarr-mcp/lib/radarr"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// resolutionClass is the class a width and height fall in: 2160, 1080, 720,
// or 480 for everything standard definition. Width decides first, so a
// 1920x800 widescreen film is 1080p like a 1920x1080 one.
func resolutionClass(width, height int) int {
	switch {
	case width >= 3200 || height >= 2000:
		return 2160
	case width >= 1800 || height >= 1000:
		return 1080
	case width >= 1200 || height >= 700:
		return 720
	case width > 0 || height > 0:
		return 480
	default:
		return 0
	}
}

// probeResolution reads the probe's "1920x1080".
func probeResolution(s string) (width, height int, ok bool) {
	w, h, found := strings.Cut(strings.ToLower(strings.TrimSpace(s)), "x")
	if !found {
		return 0, 0, false
	}
	width, errW := strconv.Atoi(w)
	height, errH := strconv.Atoi(h)

	return width, height, errW == nil && errH == nil && width > 0 && height > 0
}

// namedResolution is the resolution class a file or release name claims.
var namedResolution = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])(2160p|4k|uhd|1080p|1080i|720p|576p|480p)(?:$|[^a-z0-9])`)

func nameClass(name string) int {
	m := namedResolution.FindStringSubmatch(name)
	if m == nil {
		return 0
	}
	switch strings.ToLower(m[1]) {
	case "2160p", "4k", "uhd":
		return 2160
	case "1080p", "1080i":
		return 1080
	case "720p":
		return 720
	default:
		return 480
	}
}

func classLabel(c int) string {
	if c == 480 {
		return "SD"
	}

	return strconv.Itoa(c) + "p"
}

// legacyCodecs are the video codecs a film is worth replacing for, as
// Radarr's probe names them.
var legacyCodecs = []string{"XviD", "DivX", "MPEG2", "MPEG-2", "MPEG4", "VC1", "VC-1", "WMV", "h263", "RealVideo"}

// languageCodes maps a language as Radarr names it to the ISO 639-2 codes
// files tag audio with (both the bibliographic and terminology codes where
// they differ).
var languageCodes = map[string][]string{
	"arabic": {"ara"}, "bengali": {"ben"}, "bulgarian": {"bul"}, "catalan": {"cat"}, "chinese": {"chi", "zho", "cmn", "yue"},
	"czech": {"cze", "ces"}, "danish": {"dan"}, "dutch": {"dut", "nld"}, "english": {"eng"}, "estonian": {"est"},
	"finnish": {"fin"}, "flemish": {"dut", "nld"}, "french": {"fre", "fra"}, "german": {"ger", "deu"}, "greek": {"gre", "ell"},
	"hebrew": {"heb"}, "hindi": {"hin"}, "hungarian": {"hun"}, "icelandic": {"ice", "isl"}, "indonesian": {"ind"},
	"italian": {"ita"}, "japanese": {"jpn"}, "korean": {"kor"}, "latvian": {"lav"}, "lithuanian": {"lit"},
	"malay": {"may", "msa"}, "malayalam": {"mal"}, "norwegian": {"nor", "nob", "nno"}, "persian": {"per", "fas"},
	"polish": {"pol"}, "portuguese": {"por"}, "portuguese (brazil)": {"por"}, "romanian": {"rum", "ron"}, "russian": {"rus"},
	"serbian": {"srp"}, "slovak": {"slo", "slk"}, "slovenian": {"slv"}, "spanish": {"spa"}, "spanish (latino)": {"spa"},
	"swedish": {"swe"}, "tagalog": {"tgl"}, "tamil": {"tam"}, "telugu": {"tel"}, "thai": {"tha"}, "turkish": {"tur"},
	"ukrainian": {"ukr"}, "urdu": {"urd"}, "vietnamese": {"vie"},
}

// codesFor is the audio codes a language, named or already a code, is
// tagged with.
func codesFor(lang string) []string {
	l := strings.ToLower(strings.TrimSpace(lang))
	if codes, ok := languageCodes[l]; ok {
		return codes
	}

	return []string{l}
}

func registerFileAudits(r *registry) {
	client := r.client

	type runtimeIn struct {
		auditIn
		TolerancePercent int `json:"tolerance_percent,omitempty" jsonschema:"how far off, as a percentage of the film's runtime, a file may run, default 10 (and never flagged under 5 minutes off)"`
	}
	add(r, readTool, &mcp.Tool{
		Name: "audit_runtime",
		Description: "Sweep the library for files whose runtime disagrees with the film's (TMDB's runtime, as Radarr has it): a truncated download, a sample, a TV cut, or another film under this one's name. " +
			"history_mark_failed rejects the release that brought it, so Radarr looks for another.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in runtimeIn) (*mcp.CallToolResult, auditOut, error) {
		out, err := sweep(ctx, newAuditEnv(client), in.auditIn, runtimeCheck(in.TolerancePercent))
		return nil, out, err
	})

	add(r, readTool, &mcp.Tool{
		Name: "audit_resolution_mismatch",
		Description: "Sweep the library for files whose name claims a resolution the video does not have - a 720p file named 2160p, an upscale sold as 4K - or whose grade in Radarr disagrees with the video. " +
			"Radarr may quietly grade such a file by its video; the name is still a lie about what was downloaded. moviefile_edit corrects a wrong grade; history_mark_failed rejects the release.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in auditIn) (*mcp.CallToolResult, auditOut, error) {
		out, err := sweep(ctx, newAuditEnv(client), in, resolutionCheck)
		return nil, out, err
	})

	type qualityIn struct {
		auditIn
		MinResolution  int `json:"min_resolution,omitempty"   jsonschema:"flag video below this many lines (480, 720, 1080, 2160), default 720"`
		MinBitrateKbps int `json:"min_bitrate_kbps,omitempty" jsonschema:"also flag video below this bitrate; off by default"`
	}
	add(r, readTool, &mcp.Tool{
		Name: "audit_quality",
		Description: "Sweep the library for files worth replacing with a better copy: video below a resolution (default 720, so SD rips), in a legacy codec (XviD, DivX, MPEG-2, VC-1, WMV), or below a bitrate when one is given. " +
			"Lowest resolution first.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in qualityIn) (*mcp.CallToolResult, auditOut, error) {
		out, err := sweep(ctx, newAuditEnv(client), in.auditIn, qualityCheck(in.MinResolution, in.MinBitrateKbps))
		if err == nil {
			// fewest lines first; a finding for its codec or bitrate alone,
			// with no line count, after them
			slices.SortStableFunc(out.Findings, func(a, b auditFinding) int { return leadingLines(a.Detail) - leadingLines(b.Detail) })
		}
		return nil, out, err
	})

	add(r, readTool, &mcp.Tool{
		Name: "audit_size",
		Description: "Sweep the library for files whose size, in MB per minute of the film's runtime, is outside the limits Radarr sets for their quality (qualitydefinition_list): a tiny fake or a sample graded as a full film, or a bloated mislabelled remux. " +
			"Limits set after a file was imported never touched it; this finds what they would have refused.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in auditIn) (*mcp.CallToolResult, auditOut, error) {
		out, err := sweep(ctx, newAuditEnv(client), in, sizeCheck)
		return nil, out, err
	})

	type languageIn struct {
		auditIn
		Languages []string `json:"languages,omitempty" jsonschema:"the languages a film should have audio in (names or codes: English, jpn); default the film's original language"`
	}
	add(r, readTool, &mcp.Tool{
		Name: "audit_language",
		Description: "Sweep the library for files with no audio in a language they should have: by default the film's original language, so a dubbed-only copy of a Japanese or French film; or any languages given. " +
			"A file whose audio carries no language tags is never taken as lacking one.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in languageIn) (*mcp.CallToolResult, auditOut, error) {
		out, err := sweep(ctx, newAuditEnv(client), in.auditIn, languageCheck(in.Languages))
		return nil, out, err
	})
}

// runtimeCheck flags a file running tolerance percent or more (and five
// minutes or more) from its film's runtime.
func runtimeCheck(tolerance int) filmCheck {
	if tolerance <= 0 {
		tolerance = 10
	}

	return withFile(func(_ context.Context, _ *auditEnv, m *radarr.MovieResource, f *radarr.MovieFileResource) (string, bool, error) {
		if f.MediaInfo == nil || m.Runtime <= 0 {
			return "", false, nil
		}
		got := runtimeMinutes(f.MediaInfo.RunTime)
		if got <= 0 {
			return "", false, nil
		}
		want := float64(m.Runtime)
		diff := got - want
		pct := math.Abs(diff) / want * 100
		if pct < float64(tolerance) || math.Abs(diff) < 5 {
			return "", false, nil
		}
		shape := "shorter"
		if diff > 0 {
			shape = "longer"
		}

		return fmt.Sprintf("the file runs %.0f minutes, the film %d: %.0f%% %s", got, m.Runtime, pct, shape), true, nil
	})
}

// resolutionCheck flags a file whose name or grade claims a resolution its
// video does not have.
var resolutionCheck = withFile(func(_ context.Context, _ *auditEnv, _ *radarr.MovieResource, f *radarr.MovieFileResource) (string, bool, error) {
	if f.MediaInfo == nil {
		return "", false, nil
	}
	w, h, ok := probeResolution(f.MediaInfo.Resolution)
	if !ok {
		return "", false, nil
	}
	actual := resolutionClass(w, h)
	var claims []string
	if named := nameClass(f.RelativePath); named != 0 && named != actual {
		claims = append(claims, "named "+classLabel(named))
	} else if named == 0 {
		if scene := nameClass(f.SceneName); scene != 0 && scene != actual {
			claims = append(claims, "released as "+classLabel(scene))
		}
	}
	graded := 0
	if f.Quality != nil && f.Quality.Quality != nil {
		graded = f.Quality.Quality.Resolution
	}
	if graded != 0 && resolutionClass(0, graded) != actual {
		claims = append(claims, "graded "+qualityName(f.Quality))
	}
	if len(claims) == 0 {
		return "", false, nil
	}

	return fmt.Sprintf("%s, but the video is %dx%d (%s); Radarr graded it %s", strings.Join(claims, " and "), w, h, classLabel(actual), qualityName(f.Quality)), true, nil
})

// qualityCheck flags a file below a resolution, in a legacy codec, or below
// a bitrate. The detail starts with the resolution, so sorting by it puts
// the worst first.
func qualityCheck(minResolution, minKbps int) filmCheck {
	if minResolution <= 0 {
		minResolution = 720
	}

	return withFile(func(_ context.Context, _ *auditEnv, _ *radarr.MovieResource, f *radarr.MovieFileResource) (string, bool, error) {
		if f.MediaInfo == nil {
			return "", false, nil
		}
		mi := f.MediaInfo
		w, h, ok := probeResolution(mi.Resolution)
		var reasons []string
		if ok && resolutionClass(w, h) < minResolution {
			reasons = append(reasons, fmt.Sprintf("%d lines (%dx%d), below %d", h, w, h, minResolution))
		}
		if slices.ContainsFunc(legacyCodecs, func(c string) bool { return strings.EqualFold(c, mi.VideoCodec) }) {
			reasons = append(reasons, "a legacy codec ("+mi.VideoCodec+")")
		}
		if minKbps > 0 {
			// the probe's video bitrate when it recorded one, or the whole
			// file's over its runtime, which is what a bloated or starved
			// encode shows either way
			kbps, measured := float64(mi.VideoBitrate)/1000, mi.VideoBitrate > 0
			if minutes := runtimeMinutes(mi.RunTime); !measured && minutes > 0 {
				kbps, measured = float64(f.Size)*8/1000/(minutes*60), true
			}
			if measured && kbps < float64(minKbps) {
				reasons = append(reasons, fmt.Sprintf("%.0f kbps, below %d", kbps, minKbps))
			}
		}
		if len(reasons) == 0 {
			return "", false, nil
		}

		return strings.Join(reasons, "; "), true, nil
	})
}

// leadingLines reads the line count a quality finding starts with, a
// number past any real one when it starts with something else.
func leadingLines(detail string) int {
	n, rest, found := strings.Cut(detail, " lines")
	lines, err := strconv.Atoi(n)
	if !found || err != nil || rest == "" {
		return math.MaxInt32
	}

	return lines
}

// sizeCheck flags a file outside its quality's size limits, which Radarr
// measures in MB per minute of the film's runtime.
var sizeCheck = withFile(func(ctx context.Context, e *auditEnv, m *radarr.MovieResource, f *radarr.MovieFileResource) (string, bool, error) {
	q := qualityName(f.Quality)
	d, ok, err := e.definition(ctx, q)
	if err != nil || !ok {
		return "", false, err
	}
	minutes := float64(m.Runtime)
	if minutes <= 0 && f.MediaInfo != nil {
		minutes = runtimeMinutes(f.MediaInfo.RunTime)
	}
	if minutes <= 0 {
		return "", false, nil
	}
	perMinute := float64(f.Size) / (1 << 20) / minutes
	switch {
	case d.MinSize > 0 && perMinute < d.MinSize:
		return fmt.Sprintf("%.2f MB a minute, below %s's minimum of %g", perMinute, q, d.MinSize), true, nil
	case d.MaxSize != nil && *d.MaxSize > 0 && perMinute > *d.MaxSize:
		return fmt.Sprintf("%.2f MB a minute, above %s's maximum of %g", perMinute, q, *d.MaxSize), true, nil
	default:
		return "", false, nil
	}
})

// languageCheck flags a file with no audio in any of the wanted languages,
// by default the film's original language.
func languageCheck(wanted []string) filmCheck {
	return withFile(func(_ context.Context, _ *auditEnv, m *radarr.MovieResource, f *radarr.MovieFileResource) (string, bool, error) {
		if f.MediaInfo == nil {
			return "", false, nil
		}
		tracks := splitList(f.MediaInfo.AudioLanguages)
		// a track with no language tag could be anything, so a file with
		// one is never said to lack a language
		if len(tracks) == 0 || f.MediaInfo.AudioStreamCount > len(tracks) {
			return "", false, nil
		}
		want, label := wanted, ""
		if len(want) == 0 {
			if m.OriginalLanguage == nil || m.OriginalLanguage.Name == "" {
				return "", false, nil
			}
			want, label = []string{m.OriginalLanguage.Name}, " (the film's original language)"
		}
		var codes []string
		for _, w := range want {
			codes = append(codes, codesFor(w)...)
		}
		for _, t := range tracks {
			if slices.ContainsFunc(codes, func(c string) bool { return strings.EqualFold(c, t) }) {
				return "", false, nil
			}
		}

		return fmt.Sprintf("audio is %s, none in %s%s", strings.Join(tracks, ", "), strings.Join(want, " or "), label), true, nil
	})
}
