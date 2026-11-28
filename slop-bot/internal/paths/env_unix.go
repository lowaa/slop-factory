//go:build unix

package paths

import (
	"os"
	"runtime"
	"syscall"
)

// FromOS is the real environment.
func FromOS() (*Env, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	uid := os.Getuid()
	return &Env{
		Lookup: os.LookupEnv,
		Home:   home,
		UID:    uid,
		GOOS:   runtime.GOOS,
		Stat:   os.Stat,
		OwnedByUser: func(info os.FileInfo) bool {
			st, ok := info.Sys().(*syscall.Stat_t)
			return ok && int(st.Uid) == uid
		},
	}, nil
}
