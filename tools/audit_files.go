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

	"github.com/katbyte/radarr-mcp/sdk/radarr"
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

// shortCodes are the two-letter codes a subtitle file beside a film is often named with ("Alien (1979).en.srt"), by the language as Radarr
// names it.
var shortCodes = map[string]string{
	"arabic": "ar", "bengali": "bn", "bulgarian": "bg", "catalan": "ca", "chinese": "zh", "czech": "cs", "danish": "da", "dutch": "nl",
	"english": "en", "estonian": "et", "finnish": "fi", "flemish": "nl", "french": "fr", "german": "de", "greek": "el", "hebrew": "he",
	"hindi": "hi", "hungarian": "hu", "icelandic": "is", "indonesian": "id", "italian": "it", "japanese": "ja", "korean": "ko",
	"latvian": "lv", "lithuanian": "lt", "malay": "ms", "malayalam": "ml", "norwegian": "no", "persian": "fa", "polish": "pl",
	"portuguese": "pt", "portuguese (brazil)": "pt", "romanian": "ro", "russian": "ru", "serbian": "sr", "slovak": "sk", "slovenian": "sl",
	"spanish": "es", "spanish (latino)": "es", "swedish": "sv", "tagalog": "tl", "tamil": "ta", "telugu": "te", "thai": "th",
	"turkish": "tr", "ukrainian": "uk", "urdu": "ur", "vietnamese": "vi",
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

	add(r, readTool, &mcp.Tool{
		Name: "audit_dynamic_range",
		Description: "Sweep the library for files whose name claims HDR or Dolby Vision the video does not have, and Dolby Vision files with no HDR10 layer under them, which a TV or player without Dolby Vision shows in the wrong colours. " +
			"Radarr reads the video itself, so the grade is not wrong; the release is. history_mark_failed rejects it and lets Radarr look for another.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in auditIn) (*mcp.CallToolResult, auditOut, error) {
		out, err := sweep(ctx, newAuditEnv(client), in, rangeCheck)
		return nil, out, err
	})

	type subtitlesIn struct {
		auditIn
		Languages []string `json:"languages,omitempty" jsonschema:"the languages a film should be followable in (names or codes: English, fre); default the language Radarr's interface is in"`
		All       bool     `json:"all,omitempty"       jsonschema:"flag every film without subtitles in them, not only those whose audio is in none of them"`
		Files     bool     `json:"files,omitempty"     jsonschema:"also count the subtitle files Radarr keeps beside a film, asking Radarr about each film it would flag, which is slower"`
	}
	add(r, readTool, &mcp.Tool{
		Name: "audit_subtitles",
		Description: "Sweep the library for films a viewer could not follow: audio in none of their languages (by default the language Radarr's interface is in) and no subtitles in them either. " +
			"With all, every film without subtitles in them. Subtitles in the file are always counted; subtitle files beside it only with files. " +
			"Radarr cannot fetch subtitles itself, so this is a list for a subtitle tool such as Bazarr, or for a copy that has them.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in subtitlesIn) (*mcp.CallToolResult, auditOut, error) {
		languages := in.Languages
		if len(languages) == 0 {
			languages = []string{interfaceLanguage(ctx, client)}
		}
		out, err := sweep(ctx, newAuditEnv(client), in.auditIn, subtitleCheck(languages, in.All, in.Files))
		if err == nil && !in.Files {
			out.Note = "subtitles inside each file were counted; files also counts the subtitle files beside them"
		}
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
	// the grade is said once: as the claim when it is the wrong one, and after the claim when it is right and the name is not
	grade := "; Radarr graded it " + qualityName(f.Quality)
	if graded != 0 && resolutionClass(0, graded) != actual {
		claims = append(claims, "graded "+qualityName(f.Quality))
		grade = ""
	}
	if len(claims) == 0 {
		return "", false, nil
	}

	return fmt.Sprintf("%s, but the video is %dx%d (%s)%s", strings.Join(claims, " and "), w, h, classLabel(actual), grade), true, nil
})

// namedRange finds a claim of high dynamic range in a file's name: Dolby Vision, or HDR in one of its forms. HDRip is a rip, not HDR.
var namedRange = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])(dv|dovi|dolby[ ._-]?vision|hdr10\+|hdr10plus|hdr10|hdr)(?:$|[^a-z0-9+])`)

// rangeClaim is what a name claims of the video's dynamic range: "Dolby Vision", "HDR", or "" for nothing.
func rangeClaim(name string) string {
	claim := ""
	for _, m := range namedRange.FindAllStringSubmatch(name, -1) {
		switch strings.ToLower(m[1]) {
		case "dv", "dovi":
			return "Dolby Vision"
		default:
			if strings.HasPrefix(strings.ToLower(m[1]), "dolby") {
				return "Dolby Vision"
			}
			claim = "HDR"
		}
	}

	return claim
}

// rangeCheck flags a file whose name claims a dynamic range its video does not have, and Dolby Vision with no HDR10 layer under it, which a
// TV or player without Dolby Vision shows in the wrong colours.
var rangeCheck = withFile(func(_ context.Context, _ *auditEnv, _ *radarr.MovieResource, f *radarr.MovieFileResource) (string, bool, error) {
	if f.MediaInfo == nil {
		return "", false, nil
	}
	kind := f.MediaInfo.VideoDynamicRangeType
	dolby := strings.HasPrefix(kind, "DV")
	// Dolby Vision over an SDR picture is SDR to everything else
	hdr := kind != "" && kind != "DV SDR" || kind == "" && f.MediaInfo.VideoDynamicRange == "HDR"
	video := "SDR"
	if kind != "" {
		video = kind
	}

	claim := rangeClaim(f.RelativePath)
	said := "named"
	if claim == "" {
		claim, said = rangeClaim(f.SceneName), "released as"
	}

	switch {
	case claim == "Dolby Vision" && !dolby:
		return fmt.Sprintf("%s Dolby Vision, but the video is %s", said, video), true, nil
	case claim == "HDR" && !hdr:
		return said + " HDR, but the video is SDR", true, nil
	case kind == "DV":
		return "Dolby Vision with no HDR10 layer under it: a TV or player without Dolby Vision shows it in the wrong colours", true, nil
	}

	return "", false, nil
})

// inLanguages reports whether a track's or a file's language tag is one of the languages: by its name, its three-letter codes or, for a
// subtitle file's name, its two-letter one.
func inLanguages(tag string, languages []string) bool {
	tag = strings.ToLower(strings.TrimSpace(tag))
	for _, l := range languages {
		name := strings.ToLower(strings.TrimSpace(l))
		if tag == name || tag == shortCodes[name] || slices.Contains(codesFor(l), tag) {
			return true
		}
	}

	return false
}

// subtitleFileIn reports whether a subtitle file beside a film is in one of the languages, by the language its name carries before the
// extension: "Alien (1979).en.srt", "Alien (1979).eng.forced.srt".
func subtitleFileIn(name string, languages []string) bool {
	parts := strings.Split(baseName(name), ".")
	for _, p := range parts[1:max(len(parts)-1, 1)] {
		if inLanguages(p, languages) {
			return true
		}
	}

	return false
}

// subtitleCheck flags a film no one who speaks only the languages could follow: audio in none of them and no subtitles in them. With all,
// every film without subtitles in them. With files, a subtitle file Radarr keeps beside the film counts too, asked for film by film.
func subtitleCheck(languages []string, all, files bool) filmCheck {
	want := strings.Join(languages, " or ")

	return withFile(func(ctx context.Context, e *auditEnv, m *radarr.MovieResource, f *radarr.MovieFileResource) (string, bool, error) {
		if f.MediaInfo == nil {
			return "", false, nil
		}
		audio := splitList(f.MediaInfo.AudioLanguages)
		inLanguage := func(tag string) bool { return inLanguages(tag, languages) }
		if !all {
			// audio with no language tag, or tagged as unknown, could be in the language, so the film is never said to need subtitles
			if len(audio) == 0 || f.MediaInfo.AudioStreamCount > len(audio) || slices.ContainsFunc(audio, anyLanguage) || slices.ContainsFunc(audio, inLanguage) {
				return "", false, nil
			}
		}

		subs := splitList(f.MediaInfo.Subtitles)
		if slices.ContainsFunc(subs, inLanguage) {
			return "", false, nil
		}
		if files {
			extras, err := e.c.GetExtraFile(ctx, m.Id)
			if err != nil {
				return "", false, err
			}
			for _, x := range extras.Answer {
				if x.Type == radarr.ExtraFileTypeSubtitle && subtitleFileIn(x.RelativePath, languages) {
					return "", false, nil
				}
			}
		}

		heard := "audio is " + strings.Join(audio, ", ")
		if len(audio) == 0 {
			heard = "audio has no language tag"
		}
		if len(subs) == 0 {
			return fmt.Sprintf("%s, no subtitles in %s", heard, want), true, nil
		}

		return fmt.Sprintf("%s, subtitles %s, none in %s", heard, strings.Join(subs, ", "), want), true, nil
	})
}

// interfaceLanguage is the language Radarr's own interface is set to, the one its user reads; English when it cannot be told.
func interfaceLanguage(ctx context.Context, c *radarr.Client) string {
	ui, err := c.GetConfigUi(ctx)
	if err != nil || ui.Answer == nil {
		return "English"
	}
	languages, err := c.GetLanguage(ctx)
	if err != nil {
		return "English"
	}

	for _, l := range languages.Answer {
		if l.Id == ui.Answer.UiLanguage && l.Name != "" {
			return l.Name
		}
	}

	return "English"
}

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

// anyLanguage reports whether a track's language tag says nothing of what is spoken: undetermined, several languages, or none at all.
func anyLanguage(tag string) bool {
	return slices.Contains([]string{"und", "mul", "zxx"}, strings.ToLower(tag))
}

// languageCheck flags a file with no audio in any of the wanted languages,
// by default the film's original language.
func languageCheck(wanted []string) filmCheck {
	return withFile(func(_ context.Context, _ *auditEnv, m *radarr.MovieResource, f *radarr.MovieFileResource) (string, bool, error) {
		if f.MediaInfo == nil {
			return "", false, nil
		}
		tracks := splitList(f.MediaInfo.AudioLanguages)
		// a track with no language tag, or one tagged as unknown or as several, could be anything, so a file with one is never said to lack a language
		if len(tracks) == 0 || f.MediaInfo.AudioStreamCount > len(tracks) || slices.ContainsFunc(tracks, anyLanguage) {
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
