# go-clock

An injectable clock: services take a `clock.Clock` instead of calling
`time.Now`, so expiry, audit timestamps and timers are tested by moving a
`clock.Fake` (`Advance`, `BlockUntil`) rather than by waiting. `clock.Real`
is the wall clock; `clocktest` has `Fixed` and `Stepped` clocks for tests that
only read the time.

Consumed through `go.work`, like the other `go-*` modules.

## Tests

`./test.sh` runs them; plain Go, no Docker.
