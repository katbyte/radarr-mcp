# A curation session

The order that works for bringing a library into shape. Load the `curation` toolset first (`RADARR_TOOLSETS=curation`). What each audit looks for is in [audits.md](audits.md).

1. `audit_all` says where the library needs work.
2. `audit_unmapped_folders` lists the films on disk Radarr has never heard of; `import_identify` says which film each folder holds and what is in it, `movie_add` with the folder takes each in, and the second copies it finds are for `import_scan`. `audit_missing_folders` is the reverse - folders renamed or moved behind Radarr's back - and `movie_edit` with the folder points each film at where it is now.
3. `audit_year_mismatch` finds folders matched to the wrong film; `movie_get` shows the match. A wrong match is replaced by adding the right film for the folder; a right one in a misnamed folder is renamed by `movie_edit` into the root folder it is already in.
4. `audit_runtime`, `audit_resolution_mismatch` and `audit_size` find files that are not what they claim; `moviefile_edit` corrects a wrong grade, and `history_mark_failed` rejects a bad release so Radarr looks for another.
5. `audit_quality` and `audit_cutoff_unmet` list what is worth upgrading; `qualityprofile_edit upgrade_allowed=true` lets Radarr do it, and `movie_download` searches now rather than waiting for the next RSS sync.
6. `audit_naming` and `movie_rename` (a preview, then `preview=false`) tidy the names.
7. `audit_collection_gaps` lists what is missing from each franchise; `movie_add` or `collection_edit` fill the gaps worth filling and `exclusion_add` rules out the rest.
8. `audit_queue` shows the downloads stuck in the queue; `import_scan` and `import_apply` import one by hand, `queue_remove` clears it.
