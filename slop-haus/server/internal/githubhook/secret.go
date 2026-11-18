package githubhook

import "github.com/google/uuid"

// AppSecrets opens the webhook secret slop-haus set on a tenant's GitHub App,
// from the sealed copy the installation lookup returns. The App's secret is
// random and stored, so a delivery is checked against what the database
// holds: the signature is checked before anything is trusted, but not before
// that one global read.
type AppSecrets interface {
	Open(tenant, credential uuid.UUID, sealed []byte) (secret string, err error)
}
