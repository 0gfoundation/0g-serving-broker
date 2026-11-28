package server

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"syscall"
)

func listenAdminOverlay(path string) (net.Listener, error) {
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("admin overlay socket must be an absolute filesystem path")
	}
	parent, err := os.Lstat(filepath.Dir(path))
	if err != nil {
		return nil, fmt.Errorf("admin overlay socket directory: %w", err)
	}
	owner, ok := parent.Sys().(*syscall.Stat_t)
	if !parent.IsDir() || parent.Mode().Perm()&0o077 != 0 || !ok || int(owner.Uid) != os.Geteuid() {
		return nil, fmt.Errorf("admin overlay socket directory must be private and owned by the broker user")
	}
	return net.Listen("unix", path)
}
