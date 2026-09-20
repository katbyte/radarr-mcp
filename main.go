// Package main implements radarr-mcp, an MCP server and CLI for curating a Radarr film library.
package main

import (
	"os"

	c "github.com/gookit/color"
	"github.com/katbyte/go-kt/clog"
	"github.com/katbyte/radarr-mcp/cli"
)

func main() {
	// the log level comes from RADARR_LOG; read it once here, before anything logs
	clog.SetLevelFromEnv("RADARR_LOG")

	cmd, err := cli.Make()
	if err != nil {
		clog.Log.Error(c.Sprintf("<red>radarr-mcp: building cmd</> %v", err))

		os.Exit(1)
	}

	if err := cmd.Execute(); err != nil {
		clog.Log.Error(c.Sprintf("<red>radarr-mcp:</> %v", err))

		os.Exit(1)
	}

	os.Exit(0)
}
