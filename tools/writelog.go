package tools

import (
	"encoding/json"
	"strings"

	"github.com/katbyte/go-kt/clog"
)

// go-kt's registry writes one line for every call of a write or a delete
// tool, with what the call sent. It blanks the arguments a credential is
// usually passed under - a password, a token, an API key - which is not all
// of them here: an indexer's cookie or passkey, a download client's user
// name and a notification's webhook address come in a tool's settings, under
// names of the kind's own choosing. So a line is written with every value
// under settings blanked and the names kept, which says what a call set and
// not what to. Radarr's own settings, which server_settings_edit takes by
// section, are shown but for its credentials: what a port or an address was
// changed to is the point of the line.

// hidden stands in a line for a value it does not show.
const hidden = "REDACTED"

// logWrite is where the registry's line for a write goes: on to the log,
// with what the call set under settings blanked first.
func (r *registry) logWrite(format string, args ...any) {
	for i, a := range args {
		if s, ok := a.(string); ok && strings.HasPrefix(s, "{") {
			args[i] = blankSettings(s)
		}
	}

	write := r.writeLog
	if write == nil {
		write = clog.Log.Infof
	}
	write(format, args...)
}

// blankSettings is what a call sent, as the registry is about to write it,
// with the values under settings blanked.
func blankSettings(sent string) string {
	var args map[string]any
	if json.Unmarshal([]byte(sent), &args) != nil {
		// cut short, so it cannot be read for which values are whose: from settings on, none of it is shown
		before, _, found := strings.Cut(sent, `"settings"`)
		if !found {
			return sent
		}

		return before + `"settings":"` + hidden + `"…`
	}

	settings, ok := args["settings"].(map[string]any)
	if !ok {
		return sent
	}
	_, own := args["section"]
	for name := range settings {
		if own && !secretSetting(name) {
			continue
		}
		settings[name] = hidden
	}
	out, err := json.Marshal(args)
	if err != nil {
		return `{"settings":"` + hidden + `"}`
	}

	return string(out)
}
