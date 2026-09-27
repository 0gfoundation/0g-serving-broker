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
	"io"
	"os"

	"github.com/0glabs/0g-serving-broker/inference/config"
)

// OK is printed to stdout when the content loads. The caller requires it as well as
// exit 0, so an exec that never ran the applet (a docker exit code that decoded from
// null as 0) cannot pass for an acceptance.
const OK = "0g-validate-config: ok"

// Main validates the config named by the first argument — a file, or "-" for stdin,
// which is how the controller passes a candidate (no file to stage, and no path it
// would have to know inside the container). It prints OK and exits 0 when the content
// loads; otherwise it prints the reason to stderr, which the caller reads from the
// exec's stream, and exits 1 (2 on a usage error). It writes nothing.
func Main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: 0g-validate-config <config file | ->")
		os.Exit(2)
	}
	if err := run(os.Args[1]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(OK)
}

// run reports whether the config at path ("-" = stdin) loads.
func run(path string) error {
	var data []byte
	var err error
	if path == "-" {
		data, err = io.ReadAll(os.Stdin)
	} else {
		data, err = os.ReadFile(path)
	}
	if err != nil {
		return err
	}
	return config.ValidateConfigContent(data)
}
