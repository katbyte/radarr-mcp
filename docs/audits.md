# The audits

Each audit sweeps the whole library for one kind of problem and answers with a worklist: the films it found, why each was flagged, and the tool that fixes it. This page has the full rules for each. The short table is in the [README](../README.md#the-audits).

Most audits are one request for the whole library, because Radarr answers every film with its file and what its probe found inside it. Two are slower, `audit_naming` and `audit_untracked_files`: they ask Radarr about every film in turn, so `audit_all` leaves them out unless called with `deep`.

Every audit takes `limit` (100 findings unless given) and answers `total_findings`, the real count. Most take `root_folder` and `tag` to look at part of the library.

## audit_all

Every audit in one call, counts only, so one call says where the library needs work - start here.

## audit_missing_files

Monitored films that are available but have no file: what Radarr is still looking for, each with whether a download is queued and when Radarr last searched.

## audit_unmonitored

Unmonitored films with no file: Radarr will never download them; `movie_edit` monitors them.

## audit_cutoff_unmet

Files below their profile's cutoff, by quality or custom format score, and whether the profile upgrades at all - Radarr's own profiles are made with upgrades off, so without `qualityprofile_edit` it never replaces them.

## audit_profile_mismatch

Files whose quality their profile does not allow: a 4K file on a 1080p profile, a DVD rip on an HD one; `movie_edit` moves the film to a profile that fits, `moviefile_edit` corrects a wrong grade.

## audit_runtime

Files whose runtime disagrees with the film's: a truncated download, a sample, a TV cut, or another film under this one's name; `history_mark_failed` rejects the release so Radarr looks for another.

## audit_resolution_mismatch

Files whose name claims a resolution the video does not have - a 720p file named 2160p, an upscale sold as 4K - or whose grade disagrees with the video.

## audit_quality

Files worth replacing: below a resolution (720 lines by default), in a legacy codec (XviD, DivX, MPEG-2, VC-1, WMV), or below a bitrate when one is given, lowest resolution first.

## audit_size

Files whose size per minute of runtime is outside the limits Radarr sets for their quality: a tiny fake or a sample graded as a full film, or a bloated mislabelled remux. Limits set after a file was imported never touched it; this finds what they would have refused.

## audit_language

Files with no audio in a language they should have: by default the film's original language, so a dubbed-only copy of a Japanese or French film; a track with no language tag is never taken as lacking one.

A track tagged as of no known language (`und`), as several (`mul`) or as none (`zxx`) is treated like one with no tag: the file is left alone.

## audit_year_mismatch

Films whose folder names a year two or more away from the film's: the folder says `(2021)`, the match says 1984 - the wrong edition, or the wrong film. A right match in a misnamed folder is fixed by `movie_edit` into the root folder it is already in, which renames the folder by the naming scheme.

## audit_naming

Files and folders not named the way Radarr's naming scheme would: a scene-named file, a folder named for the wrong film; `movie_rename` renames files, `movie_edit` into a film's own root folder renames its folder.

## audit_unmapped_folders

Folders in the root folders that no film is in: films on disk Radarr has never heard of, and second copies of films it keeps elsewhere, each named with the copy it duplicates. `import_identify` says which film each one holds, and `movie_add` with the folder takes it in.

A folder is matched to a film in the library by Radarr's main title and year. A folder named for a film's other title, or a year one out, is reported as not in the library.

## audit_missing_folders

The reverse: films whose folder is not on disk, renamed or moved behind Radarr's back, each named with the folder in the same root folder that reads as the film; `movie_edit` with that folder points the film at it.

## audit_untracked_files

Video files in the films' folders that Radarr does not track - a second copy, a failed upgrade, a file dropped in by hand - each with why Radarr would not take it; a theme, trailer or other video in a folder of extras is left out; `import_apply` imports one.

The folders left out are `backdrops`, `behind the scenes`, `clips`, `deleted scenes`, `extras`, `featurettes`, `interviews`, `other`, `samples`, `scenes`, `shorts`, `theme-music` and `trailers`, in any case. How many videos were left out is in `note`.

## audit_missing_metadata

Films whose TMDB metadata came back without an overview, an IMDb id, a poster, genres, a runtime or a year; `movie_refresh` fetches it again.

## audit_removed

Films TMDB no longer lists: Radarr can no longer refresh them, and they usually mean a duplicate TMDB entry was merged away.

## audit_collection_gaps

TMDB collections the library holds some but not all of, each with the released films it is missing, leaving out ones on the exclusions; `movie_add` fills a gap, `collection_edit` has Radarr fill them all.

## audit_queue

Downloads that will not finish on their own: imports Radarr blocked (no video file, not an upgrade, the wrong film) and failed downloads, with the reasons it gives; `import_apply` imports one by hand, `queue_remove` clears it.

## How the audits are built

Radarr already exposes a large API, and an MCP server that wraps it lets a model list films and start downloads. This one does that too, but the reason it exists is the layer above: **18 audits**, each a sweep over the whole library for one thing that goes wrong in a real collection - a folder matched to the wrong film, a folder renamed behind Radarr's back, a 720p file named 2160p, a download cut off halfway, a 4K file on a 1080p profile, a dubbed-only copy of a Japanese film, a second copy nobody told Radarr about, a trilogy with a hole in it, a download stuck in the queue - returning a worklist rather than a dump, and naming the tool that fixes it.

The design principle: **detection is code, correction is judgment.** The server runs cheap deterministic checks over the whole library and produces worklists; the AI reasons only about the anomalies. Most audits are one request for the whole library, because Radarr answers every film with its file and what its probe found inside it. Every response is a trimmed projection of what a decision needs, never the raw API object (`MovieResource` has 50 fields before its file and what is inside it; `movie_list` returns 12).
