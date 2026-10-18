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

## audit_subtitles

Films a viewer could not follow: audio in none of the viewer's languages and no subtitles in them either. The languages are by default the one Radarr's own interface is set to, or any given. With `all`, every film without subtitles in the languages is flagged, whatever its audio.

Subtitles inside the file always count. Subtitle files beside it, which Radarr keeps track of, count with `files`; that asks Radarr about each film the audit would flag, so it is slower. A subtitle file's language is read from its name: `Alien (1979).en.srt` or `.eng.forced.srt`. Audio with no language tag, or tagged as unknown, could be in the viewer's language, so such a film is never flagged unless `all` is set.

Radarr cannot fetch subtitles itself. The findings are a list for a subtitle tool such as Bazarr, or for finding a copy that has them.

## audit_dynamic_range

Files whose name claims a dynamic range the video does not have: named HDR but SDR, or named Dolby Vision but only HDR10. Also Dolby Vision with no HDR10 layer under it, which a TV or player without Dolby Vision shows in the wrong colours. The name is the file's own, or the release's when the file's says nothing; "HDRip" is a rip, not a claim. Radarr reads the video itself, so its grade is not wrong; the release is. `history_mark_failed` rejects it so Radarr looks for another.

## audit_year_mismatch

Films whose folder names a year two or more away from the film's: the folder says `(2021)`, the match says 1984 - the wrong edition, or the wrong film. A right match in a misnamed folder is fixed by `movie_edit` into the root folder it is already in, which renames the folder by the naming scheme.

## audit_naming

Files and folders not named the way Radarr's naming scheme would: a scene-named file, a folder named for the wrong film; `movie_rename` renames files, `movie_edit` into a film's own root folder renames its folder.

## audit_unmapped_folders

Folders in the root folders that no film is in: films on disk Radarr has never heard of, second copies of films it keeps elsewhere, and where a film it has with no file probably is. `import_identify` says which film each one holds, and `movie_add` with the folder takes a new one in. For a film with no file, `movie_edit` with the folder points the film at it.

A folder is matched to a film by any title the film goes by: its own, its original and its alternative titles, so a folder named for the title a film had in another country still finds it. The year may be one either side of the film's, or its second year, since TMDB and a release often disagree by one. `audit_missing_folders` matches a film to a folder the same way.

`rootfolder_list` counts these folders for each root folder, and with `unmapped` names them, for a session with only the default tools.

## audit_disk_space

Root folders running out of room. A root folder is flagged when Radarr cannot reach it, when it has less free than Radarr keeps free when importing (it then refuses every import there), and when the disk it is on has less than a share free: 5% unless `min_free_percent` says otherwise. `min_free_gb` flags one under a number of GB as well. `movie_batch_edit` with a root folder moves films to another; `audit_quality` and `audit_size` find copies worth replacing.

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
