package supervisorwatch

// VersionStatus compares what a slot or supervisor reports with what the
// server serves now.
type VersionStatus string

const (
	VersionCurrent VersionStatus = "current"
	// VersionStale: reported, and not the current one.
	VersionStale VersionStatus = "stale"
	// VersionUnknown: never reported, which is a client older than the field
	// (or a workspace with no bundle-hash file), so not known to be behind.
	VersionUnknown VersionStatus = "unknown"
)

// BundleStatus compares a slot's reported bundle hash with the current one.
func BundleStatus(reported, current string) VersionStatus {
	switch {
	case reported == "" || current == "":
		return VersionUnknown
	case reported == current:
		return VersionCurrent
	default:
		return VersionStale
	}
}

// protocolStatus: a supervisor's protocol is stale when it is below the
// newest one the server speaks; 0 is one that sent none.
func protocolStatus(reported, current int) VersionStatus {
	switch {
	case reported <= 0 || current <= 0:
		return VersionUnknown
	case reported < current:
		return VersionStale
	default:
		return VersionCurrent
	}
}
