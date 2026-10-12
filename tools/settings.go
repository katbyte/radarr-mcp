package tools

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/katbyte/radarr-mcp/sdk/radarr"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Radarr's own settings are kept in sections, each read and written back
// whole: host (how it is reached, who may sign in, its proxy, logs, backups
// and updates), ui (how its web interface shows dates and which theme),
// mediamanagement (how files are imported, moved, deleted and kept an eye
// on), and the handful that apply to every indexer, download client, import
// list and metadata consumer at once. The naming settings are a section too,
// with tools of their own that show what a change does to a name
// (naming_get, naming_edit).

// settingsSection is one section as the tools read and write it: every
// setting by the name Radarr's API gives it.
type settingsSection struct {
	name  string
	about string
	// names are the settings the section has
	names []string
	read  func(ctx context.Context) (map[string]any, error)
	write func(ctx context.Context, values map[string]any) error
}

// sectionOf makes a section of one of the client's settings records: read
// into a map so a setting can be named, and written back through the record
// so a value of the wrong kind is refused before anything is sent.
func sectionOf[T any](name, about string, get func(context.Context) (*T, error), put func(context.Context, int, T) error) settingsSection {
	names := slices.DeleteFunc(namesOf[T](), func(n string) bool { return n == "id" })

	return settingsSection{
		name: name, about: about, names: names,
		read: func(ctx context.Context) (map[string]any, error) {
			v, err := get(ctx)
			if err != nil {
				return nil, err
			}
			values, err := asMap(v)
			if err != nil {
				return nil, err
			}

			return whole[T](values), nil
		},
		write: func(ctx context.Context, values map[string]any) error {
			v, err := fromMap[T](values, "the "+name+" settings")
			if err != nil {
				return err
			}
			id := idOf(values)
			if id == 0 {
				return fmt.Errorf("the %s settings as read carry no id to save them under", name)
			}

			return put(ctx, id, v)
		},
	}
}

// settingsSections are the sections Radarr keeps settings in.
func (r *registry) settingsSections() []settingsSection {
	c := r.client

	getHost := func(ctx context.Context) (*radarr.HostConfigResource, error) {
		res, err := c.GetConfigHost(ctx)
		return res.Answer, err
	}
	putHost := func(ctx context.Context, id int, v radarr.HostConfigResource) error {
		_, err := c.PutConfigHostById(ctx, id, v)
		return err
	}
	getUI := func(ctx context.Context) (*radarr.UiConfigResource, error) {
		res, err := c.GetConfigUi(ctx)
		return res.Answer, err
	}
	putUI := func(ctx context.Context, id int, v radarr.UiConfigResource) error {
		_, err := c.PutConfigUiById(ctx, id, v)
		return err
	}
	getMediaManagement := func(ctx context.Context) (*radarr.MediaManagementConfigResource, error) {
		res, err := c.GetConfigMediaManagement(ctx)
		return res.Answer, err
	}
	putMediaManagement := func(ctx context.Context, id int, v radarr.MediaManagementConfigResource) error {
		_, err := c.PutConfigMediaManagementById(ctx, id, v)
		return err
	}
	getIndexer := func(ctx context.Context) (*radarr.IndexerConfigResource, error) {
		res, err := c.GetConfigIndexer(ctx)
		return res.Answer, err
	}
	putIndexer := func(ctx context.Context, id int, v radarr.IndexerConfigResource) error {
		_, err := c.PutConfigIndexerById(ctx, id, v)
		return err
	}
	getDownloadClient := func(ctx context.Context) (*radarr.DownloadClientConfigResource, error) {
		res, err := c.GetConfigDownloadClient(ctx)
		return res.Answer, err
	}
	putDownloadClient := func(ctx context.Context, id int, v radarr.DownloadClientConfigResource) error {
		_, err := c.PutConfigDownloadClientById(ctx, id, v)
		return err
	}
	getImportList := func(ctx context.Context) (*radarr.ImportListConfigResource, error) {
		res, err := c.GetConfigImportList(ctx)
		return res.Answer, err
	}
	putImportList := func(ctx context.Context, id int, v radarr.ImportListConfigResource) error {
		_, err := c.PutConfigImportListById(ctx, id, v)
		return err
	}
	getMetadata := func(ctx context.Context) (*radarr.MetadataConfigResource, error) {
		res, err := c.GetConfigMetadata(ctx)
		return res.Answer, err
	}
	putMetadata := func(ctx context.Context, id int, v radarr.MetadataConfigResource) error {
		_, err := c.PutConfigMetadataById(ctx, id, v)
		return err
	}

	return []settingsSection{
		sectionOf("host", "how Radarr is reached and who may sign in, its own proxy, its logs, backups and updates", getHost, putHost),
		sectionOf("ui", "how the web interface shows dates, times and film titles, its language and its theme", getUI, putUI),
		sectionOf("mediamanagement", "how files are handled: whether a film whose file is deleted stays monitored, the recycling bin, hard links, permissions, free space, extra files, and when a download counts as a proper or a repack", getMediaManagement, putMediaManagement),
		sectionOf("indexer", "what holds for every indexer: the smallest and largest age and size of a release, how long a release is kept on the usenet servers, how often the RSS feeds are read, and what a film's availability waits for", getIndexer, putIndexer),
		sectionOf("downloadclient", "what holds for every download client: whether a finished download is imported on its own, whether a failed one is searched for again and taken out of the client, and how often the clients are asked", getDownloadClient, putDownloadClient),
		sectionOf("importlist", "what holds for every import list: what happens to a film in the library that no list offers any more", getImportList, putImportList),
		sectionOf("metadata", "which country's certification a film is given", getMetadata, putMetadata),
	}
}

// secretSetting reports whether a setting is a credential, whose value no
// answer shows.
func secretSetting(name string) bool {
	lower := strings.ToLower(name)

	return lower == "apikey" || strings.Contains(lower, "password")
}

// reachSettings are the settings that decide how Radarr is reached and who
// may sign in. Changing one can cut radarr-mcp off from it, or lock its
// owner out, and this tool cannot put that back: they take confirm.
var reachSettings = []string{
	"port", "sslPort", "enableSsl", "bindAddress", "urlBase", "apiKey", "allowedHosts", "trustedNetworks",
	"authenticationMethod", "authenticationRequired", "username", "password", "passwordConfirmation",
	"sslCertPath", "sslCertPassword",
}

// shownSettings is a section's settings without its credentials, and the
// names of the credentials that are set.
func shownSettings(values map[string]any) (shown map[string]any, credentials []string) {
	shown = map[string]any{}
	for name, v := range values {
		switch {
		case name == "id":
		case !secretSetting(name):
			shown[name] = v
		case v != nil && v != "":
			credentials = append(credentials, name)
		}
	}
	slices.Sort(credentials)

	return shown, credentials
}

type settingChange struct {
	Setting string `json:"setting"`
	From    any    `json:"from"    jsonschema:"what it was; a credential's value is not shown"`
	To      any    `json:"to"      jsonschema:"what it is set to; a credential's value is not shown"`
}

func registerSettingsTools(r *registry) {
	sectionNamed := func(name string) (*settingsSection, error) {
		all := r.settingsSections()
		names := make([]string, 0, len(all))
		for i := range all {
			if strings.EqualFold(all[i].name, strings.TrimSpace(name)) {
				return &all[i], nil
			}
			names = append(names, all[i].name)
		}

		return nil, fmt.Errorf("no settings section %q (have: %s)", name, strings.Join(names, ", "))
	}

	type settingsIn struct {
		Section string `json:"section,omitempty" jsonschema:"only this section: host, ui, mediamanagement, indexer, downloadclient, importlist or metadata; default every one"`
	}
	type sectionOut struct {
		Section     string         `json:"section"`
		About       string         `json:"about"`
		Settings    map[string]any `json:"settings"    jsonschema:"every setting by the name server_settings_edit takes, without credentials"`
		Credentials []string       `json:"credentials" jsonschema:"the credentials that are set, by name; their values are never shown"`
	}
	type settingsOut struct {
		Sections []sectionOut `json:"sections"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "server_settings",
		Description: "Radarr's own settings, by section: host (how it is reached, who may sign in, its proxy, its log level, how often it backs up, how it updates), ui (dates, language, theme), mediamanagement (whether a film whose file is deleted stays monitored, the recycling bin, hard links, permissions, extra files), and what holds for every indexer, download client, import list and metadata consumer. Credentials are named when set and never shown. server_settings_edit changes them; the naming settings have tools of their own.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in settingsIn) (*mcp.CallToolResult, settingsOut, error) {
		sections := r.settingsSections()
		if in.Section != "" {
			one, err := sectionNamed(in.Section)
			if err != nil {
				return nil, settingsOut{}, err
			}
			sections = []settingsSection{*one}
		}
		out := settingsOut{}
		for _, sec := range sections {
			values, err := sec.read(ctx)
			if err != nil {
				return nil, settingsOut{}, fmt.Errorf("%s settings: %w", sec.name, err)
			}
			row := sectionOut{Section: sec.name, About: sec.about}
			row.Settings, row.Credentials = shownSettings(values)
			out.Sections = append(out.Sections, row)
		}

		return nil, out, nil
	})

	type editIn struct {
		Section  string         `json:"section"           jsonschema:"host, ui, mediamanagement, indexer, downloadclient, importlist or metadata"`
		Settings map[string]any `json:"settings"          jsonschema:"the settings to change, by the name server_settings shows them under, each with its new value: logLevel, backupInterval, backupRetention, recycleBin, autoUnmonitorPreviouslyDownloadedMovies, rssSyncInterval, theme and the rest. A name the section does not have is refused with the ones it does"`
		Confirm  bool           `json:"confirm,omitempty" jsonschema:"true to change a setting that decides how Radarr is reached or who may sign in (its port, address, URL base, SSL, API key, the host names it answers to, the networks it trusts, authentication, username or password); without it such a change is not made, and the answer says what would change"`
	}
	type editOut struct {
		Section     string          `json:"section"`
		Changed     []settingChange `json:"changed"                jsonschema:"what was changed, as Radarr reads back"`
		NotKept     []settingChange `json:"not_kept,omitempty"     jsonschema:"what Radarr took without complaint and did not keep: read back, the setting is as it was. Some only hold beside another, as a username does beside a way of signing in"`
		WouldChange []settingChange `json:"would_change,omitempty" jsonschema:"without confirm: what a confirmed call changes; nothing has changed"`
		Note        string          `json:"note,omitempty"         jsonschema:"what the change means, or what has to happen next"`
		Settings    map[string]any  `json:"settings"               jsonschema:"the section as it is now, without credentials"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "server_settings_edit",
		Description: "Change Radarr's own settings, by section and name: its log level, how often it backs up and how many backups it keeps, whether a film whose file is deleted stays monitored, the recycling bin, how often the RSS feeds are read, whether finished downloads are imported, its theme and date formats. A setting that decides how Radarr is reached or who may sign in changes nothing without confirm, since a wrong value cuts radarr-mcp off from it and this tool cannot put it back. The answer says what Radarr kept, read back after saving.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in editIn) (*mcp.CallToolResult, editOut, error) {
		sec, err := sectionNamed(in.Section)
		if err != nil {
			return nil, editOut{}, err
		}
		if len(in.Settings) == 0 {
			return nil, editOut{}, errors.New("nothing to change: name a setting and its new value")
		}

		// the section is written back whole, so it is read once no other edit of it can run
		unlock := r.locking().ByString("settings/" + sec.name)
		defer unlock()

		values, err := sec.read(ctx)
		if err != nil {
			return nil, editOut{}, err
		}
		out := editOut{Section: sec.name}
		var changes []settingChange
		reach := false
		for _, asked := range sortedKeys(in.Settings) {
			i := slices.IndexFunc(sec.names, func(n string) bool { return strings.EqualFold(n, strings.TrimSpace(asked)) })
			if i < 0 {
				return nil, editOut{}, fmt.Errorf("the %s section has no setting %q (have: %s)", sec.name, asked, strings.Join(sec.names, ", "))
			}
			name, to := sec.names[i], in.Settings[asked]
			if sameJSON(values[name], to) {
				continue
			}
			change := settingChange{Setting: name, From: values[name], To: to}
			if secretSetting(name) {
				change.From, change.To = "(not shown)", "(not shown)"
			}
			changes = append(changes, change)
			values[name] = to
			reach = reach || slices.Contains(reachSettings, name)
		}
		if len(changes) == 0 {
			return nil, editOut{}, errors.New("nothing to change: every setting named already has that value")
		}
		if reach && !in.Confirm {
			out.WouldChange = changes
			out.Note = "nothing has changed: at least one of these decides how Radarr is reached or who may sign in, and a wrong value cuts radarr-mcp off from it. confirm=true makes the change"
			if now, readErr := sec.read(ctx); readErr == nil {
				out.Settings, _ = shownSettings(now)
			}

			return nil, out, nil
		}

		if err := sec.write(ctx, values); err != nil {
			return nil, editOut{}, err
		}
		// read back rather than trusted: what Radarr kept is what it says now, and it takes some settings without keeping them
		after, err := sec.read(ctx)
		if err != nil {
			return nil, out, fmt.Errorf("the %s settings were saved, but reading them back failed: %w", sec.name, err)
		}
		for _, change := range changes {
			// a credential is not read back as it was sent, so it cannot be compared
			if secretSetting(change.Setting) || sameJSON(after[change.Setting], change.To) {
				out.Changed = append(out.Changed, change)
				continue
			}
			out.NotKept = append(out.NotKept, change)
		}
		if reach && len(out.Changed) > 0 {
			out.Note = "a change to how Radarr is reached takes effect once it has restarted (server_restart), and radarr-mcp must then be given the new address or key"
		}
		out.Settings, _ = shownSettings(after)

		return nil, out, nil
	})
}
