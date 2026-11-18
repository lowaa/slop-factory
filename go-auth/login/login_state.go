package login

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/lowaa/slop-factory/go-auth/auth"
)

// maxLoginStateLen bounds the state cookie: browsers drop cookies over
// ~4KB silently, and the callback shouldn't decode arbitrarily large
// input from a request.
const maxLoginStateLen = 2048

// Intent is what a round trip through the provider is for. The callback
// acts on the identity the provider vouches for very differently per
// intent -- logging in as whoever owns it, attaching it to the signed-in
// user, or only confirming the signed-in user is present -- so the intent
// travels with the attempt rather than being inferred at the callback.
type Intent string

const (
	// IntentLogin signs in as whoever the identity belongs to.
	IntentLogin Intent = "login"
	// IntentLink attaches the identity to the signed-in user (UserID).
	IntentLink Intent = "link"
	// IntentReauth confirms the signed-in user (UserID) is present, for
	// step-up before a sensitive change.
	IntentReauth Intent = "reauth"
	// IntentAcceptInvite attaches the identity to the user an invite
	// (InviteToken) was issued for, and signs in as them.
	IntentAcceptInvite Intent = "accept_invite"
	// IntentCancelDeletion proves, by signing in as them, that the owner of
	// an account scheduled for deletion wants to keep it. Like IntentLogin
	// it carries no user: the identity the provider vouches for says whose.
	IntentCancelDeletion Intent = "cancel_deletion"
)

// LoginState is what the state cookie carries from the start of a login to
// its callback: the attempt's secrets, where to go afterwards, and what the
// attempt is for.
type LoginState struct {
	Attempt *LoginAttempt
	// Next is a local path to send the browser to afterwards.
	Next   string
	Intent Intent
	// UserID is the signed-in user an IntentLink or IntentReauth attempt
	// was started by. The callback must still check it against the session
	// it sees: the cookie comes back from the browser, so this only says
	// what the attempt claimed, never who is signed in.
	UserID string
	// InviteToken is the invite an IntentAcceptInvite attempt spends.
	InviteToken string
	// AppData is what the app wants carried, sealed, from the start of an
	// IntentLogin or IntentAcceptInvite round trip to its callback -- what
	// the person agreed to on the page that started it, say, which a
	// callback arriving by redirect cannot otherwise know. Opaque here.
	// It carries this server's MAC, but it is only what the start request
	// claimed, so the app checks it as it would a request body.
	AppData string
}

// maxAppDataLen keeps AppData a small token, not a way to fill the cookie.
const maxAppDataLen = 256

// validate refuses a state whose fields don't fit its intent, so a
// callback can rely on exactly the data its intent needs being present --
// and nothing else, which would mean the cookie was built for something
// other than what it claims.
func (s *LoginState) validate() error {
	if s.Attempt == nil || s.Attempt.State == "" || s.Attempt.Verifier == "" || s.Attempt.Nonce == "" {
		return errors.New("login state is incomplete")
	}
	if s.AppData != "" && s.Intent != IntentLogin && s.Intent != IntentAcceptInvite {
		return fmt.Errorf("login state: %s carries no app data", s.Intent)
	}
	if len(s.AppData) > maxAppDataLen {
		return errors.New("login state: app data too long")
	}
	switch s.Intent {
	case IntentLogin, IntentCancelDeletion:
		if s.UserID != "" || s.InviteToken != "" {
			return errors.New("login state: a login carries no user or invite")
		}
	case IntentLink, IntentReauth:
		if s.UserID == "" || s.InviteToken != "" {
			return fmt.Errorf("login state: %s needs a user and no invite", s.Intent)
		}
	case IntentAcceptInvite:
		if s.InviteToken == "" || s.UserID != "" {
			return errors.New("login state: accepting an invite needs an invite and no user")
		}
	default:
		return fmt.Errorf("login state: unknown intent %q", s.Intent)
	}
	return nil
}

// wireLoginState is the cookie's payload, encoded as JSON, so no field's
// content can be confused with another's.
type wireLoginState struct {
	State       string `json:"s"`
	Verifier    string `json:"v"`
	Nonce       string `json:"n"`
	Next        string `json:"r"`
	Intent      Intent `json:"i"`
	UserID      string `json:"u,omitempty"`
	InviteToken string `json:"t,omitempty"`
	AppData     string `json:"d,omitempty"`
}

// loginStateVersion prefixes every sealed state and is covered by its MAC.
// A change of format or key derivation gets a new version, so a cookie
// sealed the old way fails closed instead of being read the new way.
const loginStateVersion = "v1"

// loginStateKeyInfo is the HKDF info that makes a login-state key its own:
// a service hands in a secret it may use for other things (slop-haus, its
// JWT signing secret), and the key derived under this label is useless for
// any of them, as theirs is for this.
const loginStateKeyInfo = "go-auth/login: login state MAC v1"

// LoginStateCodec seals login states into state cookies and opens them
// again. The cookie comes back from the browser, and a browser's cookies
// can be planted by any host sharing its registrable domain (cookie
// tossing) whatever the cookie's name or path -- so what the callback acts
// on, like the user a link attaches an identity to, is only believed when
// it carries this server's MAC.
type LoginStateCodec struct {
	key []byte
}

// NewLoginStateCodec keys a codec from secret, which must be at least 32
// bytes. The MAC key is derived from it with auth.DeriveKey under a label of
// its own, so secret itself is never used as a MAC key here.
func NewLoginStateCodec(secret []byte) (*LoginStateCodec, error) {
	key, err := auth.DeriveKey(secret, loginStateKeyInfo)
	if err != nil {
		return nil, fmt.Errorf("login: login state key: %w", err)
	}
	return &LoginStateCodec{key: key}, nil
}

// Encode seals s for the state cookie. A Next too long to fit (JSON
// escaping can grow it several times over) is dropped for "/": losing the
// way back is better than failing the login.
func (c *LoginStateCodec) Encode(s *LoginState) (string, error) {
	if err := s.validate(); err != nil {
		return "", fmt.Errorf("login: encode login state: %w", err)
	}
	raw, err := json.Marshal(&wireLoginState{
		State: s.Attempt.State, Verifier: s.Attempt.Verifier, Nonce: s.Attempt.Nonce,
		Next: s.Next, Intent: s.Intent, UserID: s.UserID, InviteToken: s.InviteToken, AppData: s.AppData,
	})
	if err != nil {
		return "", fmt.Errorf("login: encode login state: %w", err)
	}
	out := c.seal(raw)
	if len(out) <= maxLoginStateLen {
		return out, nil
	}
	if s.Next != "/" {
		shorter := *s
		shorter.Next = "/"
		return c.Encode(&shorter)
	}
	return "", errors.New("login: login state too large")
}

// seal is version.payload.mac, each part base64url, the MAC over the
// version and payload as they appear.
func (c *LoginStateCodec) seal(payload []byte) string {
	signed := loginStateVersion + "." + base64.RawURLEncoding.EncodeToString(payload)
	return signed + "." + base64.RawURLEncoding.EncodeToString(c.mac(signed))
}

func (c *LoginStateCodec) mac(signed string) []byte {
	m := hmac.New(sha256.New, c.key)
	m.Write([]byte(signed))
	return m.Sum(nil)
}

// open returns the payload value sealed, if this codec's key sealed it.
// Nothing of the payload is decoded before the MAC checks out.
func (c *LoginStateCodec) open(value string) ([]byte, error) {
	if value == "" {
		return nil, errors.New("no login state")
	}
	if len(value) > maxLoginStateLen {
		return nil, errors.New("login state too large")
	}
	i := strings.LastIndexByte(value, '.')
	if i < 0 {
		return nil, errors.New("login state: not sealed")
	}
	signed, sealedMAC := value[:i], value[i+1:]
	version, payload, ok := strings.Cut(signed, ".")
	if !ok || version != loginStateVersion {
		return nil, errors.New("login state: unknown format")
	}
	got, err := base64.RawURLEncoding.DecodeString(sealedMAC)
	if err != nil || !hmac.Equal(got, c.mac(signed)) {
		return nil, errors.New("login state: bad MAC")
	}
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return nil, fmt.Errorf("login state: %w", err)
	}
	return raw, nil
}

// Decode returns the state Encode sealed, or an error for anything else:
// a MAC that doesn't check out, another format version, a field it doesn't
// know, a missing secret, or an intent without exactly the data it needs.
// Next is re-checked with LocalRedirect all the same: a bug that let an
// unsafe one through Encode must not become an open redirect.
func (c *LoginStateCodec) Decode(value string) (*LoginState, error) {
	raw, err := c.open(value)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var w wireLoginState
	if err := dec.Decode(&w); err != nil {
		return nil, fmt.Errorf("login state: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("login state: trailing data")
	}
	s := &LoginState{
		Attempt: &LoginAttempt{State: w.State, Verifier: w.Verifier, Nonce: w.Nonce},
		Next:    LocalRedirect(w.Next), Intent: w.Intent, UserID: w.UserID, InviteToken: w.InviteToken, AppData: w.AppData,
	}
	if err := s.validate(); err != nil {
		return nil, err
	}
	return s, nil
}
