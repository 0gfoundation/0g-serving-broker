package config

import (
	"fmt"
	"io"
	"os"
)

// HandleCLIFlags inspects argv (typically os.Args after the applet name has
// been stripped by the top-level main dispatcher) for config-introspection
// flags and, if one is present, writes the requested output to w and
// returns true so the caller can exit before starting the server.
//
// Supported flags:
//
//   --print-config              YAML dump of the currently-resolved config
//                               (defaults + yaml + env), with secret fields
//                               masked to "***". Safe to paste into a bug
//                               report.
//   --print-config-help         Text-table reference of every BROKER_* env
//                               name, its yaml path, type, and default.
//   --print-config-help --markdown
//                               Same content as above but emitted as
//                               markdown for committing to docs/CONFIG.md.
//
// Returning (false, nil) means no flag was found; caller proceeds normally.
// Returning (true, err) means the caller should exit — err is non-nil only
// if writing the output failed (env-var typos and similar config errors
// surface inside GetConfig as usual).
func HandleCLIFlags(argv []string, w io.Writer) (handled bool, err error) {
	if len(argv) < 2 {
		return false, nil
	}
	switch argv[1] {
	case "--print-config":
		cfg := GetConfig()
		return true, RenderEffectiveConfig(cfg, w)
	case "--print-config-help":
		// Optional second flag: --markdown switches to docs-friendly output.
		if len(argv) >= 3 && argv[2] == "--markdown" {
			return true, RenderMarkdown(w)
		}
		return true, RenderTextHelp(w)
	}
	return false, nil
}

// HandleCLIFlagsOrExit is the one-call form most callers want: if a flag
// was handled, write the output to stdout and exit; if writing fails, exit
// non-zero.
func HandleCLIFlagsOrExit() {
	handled, err := HandleCLIFlags(os.Args, os.Stdout)
	if !handled {
		return
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}
