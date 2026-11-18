package login_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-auth/login"
)

func newCodec(t *testing.T) *login.LoginStateCodec {
	t.Helper()
	c, err := login.NewLoginStateCodec([]byte(strings.Repeat("k", 32)))
	require.NoError(t, err)
	return c
}

func attempt() *login.LoginAttempt {
	return &login.LoginAttempt{State: "st", Verifier: strings.Repeat("v", 43), Nonce: "no"}
}

func TestLoginState_RoundTripsEveryIntent(t *testing.T) {
	codec := newCodec(t)
	for _, s := range []*login.LoginState{
		{Attempt: attempt(), Next: "/x", Intent: login.IntentLogin},
		{Attempt: attempt(), Next: "/x", Intent: login.IntentCancelDeletion},
		{Attempt: attempt(), Next: "/settings", Intent: login.IntentLink, UserID: "user-1"},
		{Attempt: attempt(), Next: "/", Intent: login.IntentReauth, UserID: "user-1"},
		{Attempt: attempt(), Next: "/", Intent: login.IntentAcceptInvite, InviteToken: "invite-token"},
		{Attempt: attempt(), Next: "/", Intent: login.IntentLogin, AppData: `{"agreed":true}`},
		{Attempt: attempt(), Next: "/", Intent: login.IntentAcceptInvite, InviteToken: "invite-token", AppData: "x"},
	} {
		t.Run(string(s.Intent)+s.AppData, func(t *testing.T) {
			enc, err := codec.Encode(s)
			require.NoError(t, err)
			got, err := codec.Decode(enc)
			require.NoError(t, err)
			assert.Equal(t, s, got)
		})
	}
}

// Each intent carries exactly the data it needs, both ways: encoding a
// state that doesn't fit is a bug in the caller, and decoding one is a
// cookie that was never ours.
func TestLoginState_IntentDataMustFit(t *testing.T) {
	codec := newCodec(t)
	cases := map[string]*login.LoginState{
		"unknown intent":                 {Attempt: attempt(), Intent: "impersonate"},
		"no intent":                      {Attempt: attempt()},
		"login with a user":              {Attempt: attempt(), Intent: login.IntentLogin, UserID: "u"},
		"login with an invite":           {Attempt: attempt(), Intent: login.IntentLogin, InviteToken: "t"},
		"cancel deletion with a user":    {Attempt: attempt(), Intent: login.IntentCancelDeletion, UserID: "u"},
		"cancel deletion with an invite": {Attempt: attempt(), Intent: login.IntentCancelDeletion, InviteToken: "t"},
		"link without a user":            {Attempt: attempt(), Intent: login.IntentLink},
		"link with an invite":            {Attempt: attempt(), Intent: login.IntentLink, UserID: "u", InviteToken: "t"},
		"reauth without a user":          {Attempt: attempt(), Intent: login.IntentReauth},
		"accept without an invite":       {Attempt: attempt(), Intent: login.IntentAcceptInvite},
		"accept with a user":             {Attempt: attempt(), Intent: login.IntentAcceptInvite, InviteToken: "t", UserID: "u"},
		"link with app data":             {Attempt: attempt(), Intent: login.IntentLink, UserID: "u", AppData: "x"},
		"reauth with app data":           {Attempt: attempt(), Intent: login.IntentReauth, UserID: "u", AppData: "x"},
		"cancel deletion with app data":  {Attempt: attempt(), Intent: login.IntentCancelDeletion, AppData: "x"},
		"login with too much app data":   {Attempt: attempt(), Intent: login.IntentLogin, AppData: strings.Repeat("d", 257)},
		"no attempt":                     {Intent: login.IntentLogin},
		"attempt missing its secrets":    {Attempt: &login.LoginAttempt{State: "s"}, Intent: login.IntentLogin},
	}
	for name, s := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := codec.Encode(s)
			assert.Error(t, err, "encode")

			wire := map[string]string{"i": string(s.Intent), "u": s.UserID, "t": s.InviteToken, "d": s.AppData, "r": "/"}
			if s.Attempt != nil {
				wire["s"], wire["v"], wire["n"] = s.Attempt.State, s.Attempt.Verifier, s.Attempt.Nonce
			}
			_, err = codec.Decode(codec.Seal(mustJSON(t, wire)))
			assert.Error(t, err, "decode")
		})
	}
}

// Past the MAC, Decode still refuses a payload that isn't exactly what
// Encode writes.
func TestDecodeLoginState_RefusesWhatItDidNotWrite(t *testing.T) {
	codec := newCodec(t)
	good := map[string]string{"s": "st", "v": "ver", "n": "no", "r": "/", "i": "login"}
	withExtra := map[string]string{"x": "1"}
	for k, v := range good {
		withExtra[k] = v
	}
	cases := map[string]string{
		"empty":         "",
		"not json":      codec.Seal([]byte("st|/")),
		"unknown field": codec.Seal(mustJSON(t, withExtra)),
		"trailing data": codec.Seal(append(mustJSON(t, good), []byte(`{}`)...)),
		"oversized":     strings.Repeat("a", 2049),
	}
	for name, value := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := codec.Decode(value)
			assert.Error(t, err)
		})
	}
	_, err := codec.Decode(codec.Seal(mustJSON(t, good)))
	require.NoError(t, err, "the control case decodes")
}

// The state is only believed with this server's MAC: a cookie planted by
// another host on the domain -- a link intent naming the victim, say -- is
// refused before any of it is read.
func TestDecodeLoginState_RefusesAnythingItDidNotSeal(t *testing.T) {
	codec := newCodec(t)
	enc, err := codec.Encode(&login.LoginState{Attempt: attempt(), Next: "/", Intent: login.IntentLink, UserID: "user-1"})
	require.NoError(t, err)
	parts := strings.Split(enc, ".")
	require.Len(t, parts, 3)
	version, payload, mac := parts[0], parts[1], parts[2]

	forged := mustJSON(t, map[string]string{"s": "st", "v": "ver", "n": "no", "r": "/", "i": "link", "u": "victim"})
	otherKey, err := login.NewLoginStateCodec([]byte(strings.Repeat("x", 32)))
	require.NoError(t, err)
	rawKeyed := hmac.New(sha256.New, []byte(strings.Repeat("k", 32)))
	rawKeyed.Write([]byte(version + "." + payload))
	flipped := []byte(payload)
	flipped[len(flipped)/2] ^= 1

	cases := map[string]string{
		"unsealed payload":            base64.RawURLEncoding.EncodeToString(forged),
		"payload swapped, old MAC":    version + "." + base64.RawURLEncoding.EncodeToString(forged) + "." + mac,
		"payload bit flipped":         version + "." + string(flipped) + "." + mac,
		"sealed with another key":     otherKey.Seal(forged),
		"MAC keyed by the raw secret": version + "." + payload + "." + base64.RawURLEncoding.EncodeToString(rawKeyed.Sum(nil)),
		"no MAC":                      version + "." + payload,
		"empty MAC":                   version + "." + payload + ".",
		"truncated MAC":               version + "." + payload + "." + mac[:len(mac)-4],
		"MAC not base64":              version + "." + payload + ".!!!" + mac[3:],
		"another version":             "v2." + payload + "." + mac,
		"no version":                  payload + "." + mac,
		"truncated":                   enc[:len(enc)/2],
		"garbage":                     "not.a.state",
		"dots only":                   "..",
	}
	for name, value := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := codec.Decode(value)
			assert.Error(t, err)
		})
	}
	got, err := codec.Decode(enc)
	require.NoError(t, err, "the control case decodes")
	assert.Equal(t, "user-1", got.UserID)
}

func TestNewLoginStateCodec_RefusesAShortSecret(t *testing.T) {
	_, err := login.NewLoginStateCodec([]byte(strings.Repeat("k", 31)))
	assert.Error(t, err)
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	require.NoError(t, err)
	return raw
}

func TestDecodeLoginState_UnsafeNextBecomesRoot(t *testing.T) {
	codec := newCodec(t)
	got, err := codec.Decode(codec.Seal(mustJSON(t, map[string]string{"s": "st", "v": "ver", "n": "no", "r": "//evil.example", "i": "login"})))
	require.NoError(t, err)
	assert.Equal(t, "/", got.Next)
}

func TestEncodeLoginState_TooLongNextFallsBackToRootAndFits(t *testing.T) {
	s := &login.LoginState{Attempt: attempt(), Next: "/" + strings.Repeat("<", 1000), Intent: login.IntentAcceptInvite, InviteToken: strings.Repeat("t", 43)}
	codec := newCodec(t)
	enc, err := codec.Encode(s)
	require.NoError(t, err)
	assert.LessOrEqual(t, len(enc), 2048)
	got, err := codec.Decode(enc)
	require.NoError(t, err)
	assert.Equal(t, "/", got.Next)
	assert.Equal(t, s.InviteToken, got.InviteToken, "only Next is given up to make it fit")
}
