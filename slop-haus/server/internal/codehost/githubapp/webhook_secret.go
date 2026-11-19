package githubapp

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/secretbox"
)

// webhookSecretBytes is 256 bits of entropy; GitHub signs deliveries with
// HMAC-SHA256 under it.
const webhookSecretBytes = 32

// WebhookSecret is the secret GitHub signs an App's deliveries with. It
// prints as a placeholder however it is formatted, logged or encoded; the
// one way to its value is Reveal, for sending to GitHub and sealing.
type WebhookSecret struct {
	secretbox.Redacted
	value string
}

func newWebhookSecret() (*WebhookSecret, error) {
	b := make([]byte, webhookSecretBytes)
	if _, err := rand.Read(b); err != nil {
		return nil, fmt.Errorf("githubapp: generate webhook secret: %w", err)
	}
	return &WebhookSecret{value: hex.EncodeToString(b)}, nil
}

func (s *WebhookSecret) Reveal() string { return s.value }

// OpenWebhookSecret opens a sealed webhook secret copied out of an App row, for
// the receiver that checks the App's deliveries. The tenant and the App's id
// are what it was sealed for.
func OpenWebhookSecret(keyring *secretbox.Keyring, tenant, app uuid.UUID, blob []byte) (*WebhookSecret, error) {
	plain, err := keyring.Open(blob, secretbox.Binding{Tenant: tenant, Row: app, Purpose: webhookSecretPurpose})
	if err != nil {
		return nil, err
	}
	return &WebhookSecret{value: string(plain)}, nil
}
