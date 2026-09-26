// Package validateconfig is the 0g-validate-config applet: it reports whether a
// config file would load under THIS image's broker code.
//
// The controller runs it inside the broker and event containers (docker exec) before
// accepting a config push whose keys its own, older code does not read: only the image
// that will restart onto the file can say whether it loads it — including the values of
// keys the controller has never heard of.
package validateconfig

import (
	"fmt"
	"os"

	"github.com/0glabs/0g-serving-broker/inference/config"
)

// Main validates the file named by the first argument. It exits 0 when the content
// loads; otherwise it prints the reason to stderr, which the caller reads from the
// exec's stream, and exits 1. It writes nothing: the config volume is read-only in the
// containers it runs in.
func Main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: 0g-validate-config <config file>")
		os.Exit(2)
	}
	if err := run(os.Args[1]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// run reports whether the config at path loads.
func run(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return config.ValidateConfigContent(data)
}
