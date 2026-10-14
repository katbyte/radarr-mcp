# Choosing which tools load

**The default is `core`: six read-only tools, about 950 tokens.** The whole surface is around 26,500 tokens of tool definitions before anyone asks a question, which is a poor way to spend a client's context by default. `--toolsets` / `RADARR_TOOLSETS` loads the groups a session actually needs, and `core` comes along with whatever else is asked for, because nothing else can find a film.

**Curating a library needs `RADARR_TOOLSETS=curation`** - the audits and everything that fixes what they find. **Setting Radarr up needs `RADARR_TOOLSETS=setup`** - its indexers, download clients, lists, notifications, rules and settings: more than a third of all the tools, and worth loading only for that job. `RADARR_TOOLSETS=all` restores every tool.

| toolset | tools | with core | ~tokens |
|---|---|---|---|
| `core` *(default)* | 6 | 6 | 950 |
| `organise` | 9 | 15 | 2,550 |
| `acquire` | 13 | 19 | 3,000 |
| `admin` | 17 | 23 | 3,500 |
| `curation` | 40 | 46 | 8,450 |
| `setup` | 52 | 58 | 12,800 |
| `all` | 137 | 137 | 26,500 |

Tokens are what the model sees: each tool's name, description and input schema, measured over a real `tools/list` at four bytes a token, with `--enable-delete` on (the 14 delete tools are among them). Every tool also carries an output schema, another 34,000 tokens across `all`, but clients keep that to themselves to validate results rather than sending it to the model.

`--toolsets` also takes a resource family - `movie`, `moviefile`, `audit`, `import`, `release`, `queue`, `history`, `blocklist`, `calendar`, `collection`, `exclusion`, `tag`, `rootfolder`, `qualityprofile`, `qualitydefinition`, `customformat`, `naming`, `indexer`, `downloadclient`, `importlist`, `notification`, `metadata`, `provider`, `delayprofile`, `releaseprofile`, `autotagging`, `remotepathmapping`, `customfilter`, `filesystem`, `server`, `task` - which is every tool with that prefix:

```sh
RADARR_TOOLSETS=all                 # every tool
RADARR_TOOLSETS=curation            # audits plus everything that fixes what they find
RADARR_TOOLSETS=acquire             # searching, grabbing and following downloads
RADARR_TOOLSETS=setup               # indexers, download clients, lists, notifications, rules and settings
RADARR_TOOLSETS=audit               # read-only detection, nothing that writes
RADARR_TOOLSETS=core,movie,queue    # core plus two whole families
```

`radarr-mcp tools` prints what the current flags would register, grouped by toolset, and needs no server:

```sh
radarr-mcp tools                    # the default set
radarr-mcp tools --toolsets all     # every tool
radarr-mcp tools --read-only -q     # names only
```

## One tool more, or a few less

`--allow-tools` asks for tools by name, as `--toolsets` asks for them by set, and a session gets what either names: "these sets, and this tool as well". On its own, with no toolset beside it, it is only the tools it names. `--deny-tools` takes tools out of whatever was asked for, which is how a toolset is narrowed. Both take comma-separated tool names, globs with a leading or trailing `*`, and `--allow-tools` the `essential` preset (`movie_list`, `movie_get`, `movie_lookup`, `movie_add`, `queue_list`). Neither gets a tool past `--read-only` or the delete gate.

```sh
RADARR_TOOLSETS=curation RADARR_ALLOW_TOOLS=queue_list    # the curation set, and the queue as well
RADARR_ALLOW_TOOLS=essential                              # the five essential tools and nothing else
RADARR_ALLOW_TOOLS=movie_*,queue_list                     # only these
RADARR_TOOLSETS=curation RADARR_DENY_TOOLS=audit_*        # the curation set without its audits
RADARR_DENY_TOOLS=*_delete,release_grab
```

Before 0.2 an allow list beside toolsets narrowed them, so a tool the sets did not hold was silently left out.

A pattern that matches no tool aborts startup and names it, so a typo cannot silently hide a tool.
