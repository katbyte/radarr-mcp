// Package cli implements the radarr-mcp command line interface: the cobra commands, flag and
// config handling, and the MCP server exposing Radarr library tools to AI clients.
package cli

import (
	"errors"
	"fmt"
	"strings"

	"github.com/katbyte/go-kt/version"
	"github.com/katbyte/radarr-mcp/tools"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// firstSentence trims a tool description to its opening sentence, so the list
// stays one line per tool.
func firstSentence(s string) string {
	if i := strings.Index(s, ". "); i > 0 {
		return s[:i+1]
	}

	return s
}

func ValidateParams(params []string) func(cmd *cobra.Command, args []string) error {
	return func(_ *cobra.Command, _ []string) error {
		for _, p := range params {
			if viper.GetString(p) != "" {
				continue
			}
			return errors.New(p + " parameter can't be empty")
		}

		return nil
	}
}

// connectionParams are the flags every command that talks to Radarr needs.
var connectionParams = []string{"server", "token"}

// toolsetOrder is how `radarr-mcp tools` lists the sets: the base first,
// then the job most sessions are for.
var toolsetOrder = []string{"core", "curation", "acquire", "organise", "admin"}

func Make() (*cobra.Command, error) {
	root := &cobra.Command{
		Use:   "radarr-mcp [command]",
		Short: "radarr-mcp is an MCP server and CLI for curating a Radarr movie library",
		Long: `An MCP server (and CLI) for curating a Radarr movie library: audit it for the
things that go wrong in a real collection - wrong matches, truncated or mislabelled files,
films on disk Radarr does not know about, downloads that will never finish - and fix what
the audits find, from an AI client such as Claude Code.
Complete documentation is available at https://github.com/katbyte/radarr-mcp`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "Run \"radarr-mcp help\" for more information about available radarr-mcp commands.")
			return nil
		},
	}

	root.AddCommand(&cobra.Command{
		Use:           "version",
		Short:         "Print the version number of radarr-mcp",
		Long:          `Print the version number of radarr-mcp`,
		Args:          cobra.NoArgs,
		SilenceErrors: true,
		Run: func(cmd *cobra.Command, _ []string) {
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "radarr-mcp "+version.Version)
		},
	})

	root.AddCommand(&cobra.Command{
		Use:   "tools",
		Short: "List the tools and toolsets, and what the current flags would register",
		Long: `Lists every tool radarr-mcp would register with the current flags, grouped by toolset,
with its kind (read, write or delete) and what it does.

Needs no server: it reports what would be registered, not what a server accepts.

  radarr-mcp tools                        # the default set (core)
  radarr-mcp tools --toolsets all         # every tool
  radarr-mcp tools --toolsets curation    # audits and the tools that fix what they find
  radarr-mcp tools --read-only            # only the tools that never change state
  radarr-mcp tools -q                     # names only`,
		Args:          cobra.NoArgs,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cmd.SilenceUsage = true
			out := cmd.OutOrStdout()
			quiet, _ := cmd.Flags().GetBool("quiet")

			f := GetFlags()
			list, err := tools.Describe(f.ToolOptions())
			if err != nil {
				return err
			}

			if quiet {
				for _, t := range list {
					_, _ = fmt.Fprintln(out, t.Name)
				}
				return nil
			}

			byset := map[string][]tools.ToolInfo{}
			for _, t := range list {
				byset[t.Toolset] = append(byset[t.Toolset], t)
			}
			counts := map[string]int{}
			for _, t := range list {
				counts[t.Kind]++
			}

			for _, set := range toolsetOrder {
				in := byset[set]
				if len(in) == 0 {
					continue
				}
				_, _ = fmt.Fprintf(out, "\n%s (%d)\n", set, len(in))
				for _, t := range in {
					_, _ = fmt.Fprintf(out, "  %-32s %-6s %s\n", t.Name, t.Kind, firstSentence(t.Description))
				}
			}
			_, _ = fmt.Fprintf(out, "\n%d tools: %d read, %d write, %d delete\n",
				len(list), counts["read"], counts["write"], counts["delete"])
			if !f.EnableDelete {
				_, _ = fmt.Fprintln(out, "delete tools are hidden; --enable-delete registers them")
			}
			_, _ = fmt.Fprintf(out, "\ntoolsets: all, %s\n", strings.Join(tools.ToolsetNames(), ", "))
			_, _ = fmt.Fprintf(out, "families: %s\n", strings.Join(tools.FamilyNames(), ", "))
			_, _ = fmt.Fprintf(out, "select with --toolsets / RADARR_TOOLSETS; core is always included. "+
				"Default is %s - use --toolsets all for every tool.\n", strings.Join(DefaultToolsets, ","))

			return nil
		},
	})
	if c, _, err := root.Find([]string{"tools"}); err == nil {
		c.Flags().BoolP("quiet", "q", false, "print tool names only")
	}

	root.AddCommand(&cobra.Command{
		Use:           "info",
		Short:         "Check connectivity and print Radarr's version and root folders",
		Long:          `Connects to the configured Radarr and prints its version, operating system and the root folders it keeps films in, with their free space.`,
		Args:          cobra.NoArgs,
		PreRunE:       ValidateParams(connectionParams),
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cmd.SilenceUsage = true
			out := cmd.OutOrStdout()

			client, err := GetFlags().NewClient()
			if err != nil {
				return err
			}

			status, err := client.GetSystemStatus(cmd.Context())
			if err != nil {
				return err
			}
			folders, err := client.GetRootfolder(cmd.Context())
			if err != nil {
				return err
			}

			s := status.Model
			_, _ = fmt.Fprintf(out, "%s %s (%s %s) at %s\n", s.AppName, s.Version, s.OsName, s.OsVersion, client.Client.BaseURL)
			for _, f := range folders.Model {
				_, _ = fmt.Fprintf(out, "  root folder %q, %s free\n", f.Path, humanBytes(val(f.FreeSpace)))
			}
			return nil
		},
	})

	root.AddCommand(serveCmd())

	if err := configureFlags(root); err != nil {
		return nil, fmt.Errorf("unable to configure flags: %w", err)
	}

	return root, nil
}

// val is what a nullable field holds, its zero value when it is null.
func val[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}

	return *p
}

// humanBytes renders a byte count the way a disk reports it.
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}

	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
