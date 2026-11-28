package sessionhost

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSystemdScopeOnlyUnderAUserManager(t *testing.T) {
	t.Parallel()
	service := "0::/user.slice/user-1000.slice/user@1000.service/app.slice/slop-bot.service\n"
	assert.Equal(t, []string{
		"/usr/bin/systemd-run", "--user", "--scope", "--quiet", "--collect",
		"--unit=slop-bot-tmux-slop-bot", "--description=slop-bot's agent sessions (tmux -L slop-bot)", "--",
	}, systemdScope(service, "/usr/bin/systemd-run", "slop-bot"))
	// A terminal app's cgroup is the manager's too: closing the terminal
	// would end a server started in it.
	assert.NotNil(t, systemdScope("0::/user.slice/user-1000.slice/user@1000.service/app.slice/app-term.slice/app-term@x.service\n", "/bin/systemd-run", "s"))

	for name, cgroup := range map[string]string{
		"a login session":  "0::/user.slice/user-1000.slice/session-2.scope\n",
		"a container":      "0::/\n",
		"cgroup v1":        "12:pids:/user.slice\n1:name=systemd:/user.slice/user-1000.slice/user@1000.service/app.slice\n",
		"no /proc (macOS)": "",
	} {
		assert.Nil(t, systemdScope(cgroup, "/usr/bin/systemd-run", "s"), name)
	}
	assert.Nil(t, systemdScope(service, "", "s"), "no systemd-run")
}
