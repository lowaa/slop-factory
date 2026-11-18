package ticket

// replayableStatus is whether a ticket in status s takes replays. A parked
// ticket does so as a benchmark: nobody works it for real, so it has no
// original attempt and its replays have no baseline to compare against.
// AddAttempts additionally requires an explicit base, as main's head is not a
// fixed point to replay from.
func replayableStatus(s Status) bool {
	return s == StatusDone || s == StatusParked
}
