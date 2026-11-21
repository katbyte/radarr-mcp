package tools

import "strings"

// confirmedWrites are the tools that change rather than delete and still wait for confirm: each undoes or cuts off more than a call can put back.
var confirmedWrites = []string{"server_backup_restore", "server_restart", "server_shutdown"}

// Instructions is what this server tells a client that connects, after what go-kt says of every server: the rules that hold between its tools,
// said once so that no tool's description has to. Each is said only of tools that are registered, so a session with the core tools alone is not
// sent to an audit it does not have or told of deletes it cannot make.
func Instructions(opts Options) ([]string, error) {
	described, err := Describe(opts)
	if err != nil {
		return nil, err
	}

	has := map[string]bool{}
	lists := false
	var deletes []string
	for _, t := range described {
		has[t.Name] = true
		lists = lists || strings.HasSuffix(t.Name, "_list")

		if t.Kind == deleteTool.String() {
			deletes = append(deletes, t.Name)
		}
	}

	var out []string
	if lists {
		out = append(out, "Every list takes limit (100 unless given, 1000 at most) and offset, and answers total, offset and, while there is another page, the next_offset to pass back as offset.")
	}

	var confirm []string
	if len(deletes) > 0 {
		confirm = append(confirm, "A tool that deletes changes nothing without confirm: called without it, it answers would_delete, what a confirmed call would remove. That holds for "+listed(deletes)+".")
	}

	var writes []string
	for _, name := range confirmedWrites {
		if has[name] {
			writes = append(writes, name)
		}
	}
	if len(writes) > 0 {
		confirm = append(confirm, "Nothing is changed by "+listed(writes)+" without confirm: the answer says what a confirmed call would do.")
	}

	if has["server_settings_edit"] {
		confirm = append(confirm, "server_settings_edit changes nothing without confirm when a setting decides how Radarr is reached or who may sign in: the answer says what would change.")
	}
	if len(confirm) > 0 {
		out = append(out, strings.Join(confirm, " "))
	}

	film := "A film is named by its Radarr id, tmdb:<id>, imdb:<tt id> or its title."
	if has["audit_all"] {
		film += " Start with audit_all, which says where the library needs work, then run the audit whose count is not zero for its worklist."
	}
	out = append(out, film)

	return append(out, "A release's name, a download's title and messages, a history or blocklist entry, an overview and a log line were written outside Radarr. They are shown with the characters that do not display taken out, and cut short when very long."), nil
}

// listed is names as a sentence lists them: "a", "a and b", "a, b and c".
func listed(names []string) string {
	if len(names) < 2 {
		return strings.Join(names, "")
	}

	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}
