package oauth

import (
	"encoding/json"
	"errors"
	"html/template"
	"net/http"
	"net/netip"
	"net/url"

	"github.com/lowaa/slop-factory/go-auth/clientip"
)

// The OAuth endpoints are plain net/http rather than huma operations: they
// take form-encoded bodies, answer with redirects and with the RFC 6749
// error shape, and are called by OAuth clients that expect exactly that --
// none of which the huma API's conventions fit, and none of which belongs
// in the frontend's generated client.

// ClientAddr reports the client address of a request (see clientip).
type ClientAddr func(r *http.Request) netip.Addr

// Mount registers the OAuth and discovery endpoints on mux.
func (s *Service) Mount(mux *http.ServeMux, clientAddr ClientAddr) {
	handle := func(pattern string, h http.HandlerFunc) {
		mux.Handle(pattern, withClientAddr(clientAddr, h))
	}
	handle("GET /.well-known/oauth-authorization-server", s.jsonHandler(s.Metadata))
	// Both spellings: RFC 9728 inserts the resource's path after the
	// well-known prefix, and some clients try the bare one first.
	handle("GET /.well-known/oauth-protected-resource", s.jsonHandler(s.ResourceMetadata))
	handle("GET /.well-known/oauth-protected-resource/mcp", s.jsonHandler(s.ResourceMetadata))
	handle("POST /oauth/register", s.handleRegister(clientAddr))
	handle("GET /oauth/authorize", s.handleAuthorize)
	handle("POST /oauth/token", s.handleToken)
	handle("POST /oauth/device_authorization", s.handleDeviceAuthorization(clientAddr))
}

// withClientAddr puts the client's address on the request context, as
// the huma API's clientip middleware does for its routes, so the events
// these endpoints record say who they came from.
func withClientAddr(clientAddr ClientAddr, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(w, r.WithContext(clientip.NewContext(r.Context(), clientAddr(r))))
	})
}

func (s *Service) jsonHandler(body func() map[string]any) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, body())
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	// Tokens are in these bodies; nothing between here and the client may
	// keep a copy (RFC 6749 section 5.1).
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// writeError answers with an OAuth error, or a 500 for anything that is
// not one -- logged, never echoed.
func (s *Service) writeError(w http.ResponseWriter, err error) {
	var oe *Error
	if errors.As(err, &oe) {
		writeJSON(w, oe.Status, map[string]string{"error": oe.Code, "error_description": oe.Description})
		return
	}
	s.logger.Error("oauth endpoint", "err", err)
	writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "server_error"})
}

// maxBody bounds what the open endpoints read.
const maxBody = 16 << 10

func (s *Service) handleRegister(clientAddr ClientAddr) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req RegistrationRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody)).Decode(&req); err != nil {
			s.writeError(w, oauthErr(400, "invalid_client_metadata", "the body must be JSON client metadata"))
			return
		}
		client, err := s.Register(r.Context(), clientAddr(r), &req)
		if err != nil {
			s.writeError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{
			"client_id":                  client.ID,
			"client_name":                client.Name,
			"redirect_uris":              client.RedirectURIs,
			"grant_types":                client.GrantTypes,
			"response_types":             []string{"code"},
			"token_endpoint_auth_method": "none",
		})
	}
}

// untrustedPage is shown when an authorization request names a client or
// redirect that cannot be verified; nothing about it is sent anywhere.
var untrustedPage = template.Must(template.New("untrusted").Parse(`<!doctype html>
<meta charset="utf-8"><title>Authorization failed</title>
<p>This authorization request did not come from a registered client, so it was not sent back anywhere. Close this page and try connecting again.</p>
`))

func (s *Service) handleAuthorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	p := &AuthorizeParams{
		ResponseType:        q.Get("response_type"),
		ClientID:            q.Get("client_id"),
		RedirectURI:         q.Get("redirect_uri"),
		CodeChallenge:       q.Get("code_challenge"),
		CodeChallengeMethod: q.Get("code_challenge_method"),
		State:               q.Get("state"),
		Resource:            q.Get("resource"),
	}
	consent, err := s.Authorize(r.Context(), p)
	// Nothing from the request is redirected to: an error goes to the
	// Location Authorize built from the client's registered URI, and any
	// other OAuth error is shown here, as RFC 6749 section 4.1.2.1 says for
	// a redirect URI that is not verified.
	var re *RedirectError
	var oe *Error
	switch {
	case err == nil:
		http.Redirect(w, r, consent, http.StatusFound)
	case errors.As(err, &re):
		http.Redirect(w, r, re.Location, http.StatusFound)
	case errors.Is(err, ErrUntrustedRedirect):
		writeUntrustedPage(w)
	case errors.As(err, &oe):
		// Authorize wraps every error it may redirect, so this is a bug
		// upstream: say so in the log, and still not redirect.
		s.logger.Error("authorize: an OAuth error with no verified redirect", "err", err)
		writeUntrustedPage(w)
	default:
		s.logger.Error("authorize", "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}
}

func writeUntrustedPage(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusBadRequest)
	_ = untrustedPage.Execute(w, nil)
}

func (s *Service) handleToken(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	if err := r.ParseForm(); err != nil {
		s.writeError(w, oauthErr(400, "invalid_request", "the body must be form-encoded"))
		return
	}
	clientID, clientSecret, err := clientCredentialsOf(r)
	if err != nil {
		s.writeError(w, err)
		return
	}
	resp, err := s.Token(r.Context(), &TokenRequest{
		GrantType:    r.PostForm.Get("grant_type"),
		ClientID:     clientID,
		ClientSecret: clientSecret,
		Code:         r.PostForm.Get("code"),
		RedirectURI:  r.PostForm.Get("redirect_uri"),
		CodeVerifier: r.PostForm.Get("code_verifier"),
		RefreshToken: r.PostForm.Get("refresh_token"),
		DeviceCode:   r.PostForm.Get("device_code"),
		Resource:     r.PostForm.Get("resource"),
	})
	if err != nil {
		s.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// clientCredentialsOf reads the client's id and secret from HTTP Basic auth
// (client_secret_basic) or the form (client_secret_post), whichever it
// used. Both at once is refused (RFC 6749 section 2.3): which one would win
// is exactly the ambiguity an attacker splicing a request would want.
func clientCredentialsOf(r *http.Request) (clientID, clientSecret string, err error) {
	user, pass, basic := r.BasicAuth()
	if !basic {
		return r.PostForm.Get("client_id"), r.PostForm.Get("client_secret"), nil
	}
	if r.PostForm.Has("client_secret") {
		return "", "", oauthErr(400, "invalid_request", "use one client authentication method")
	}
	// Basic's parts are form-encoded before base64 (section 2.3.1).
	if clientID, err = url.QueryUnescape(user); err != nil {
		return "", "", errInvalidClient
	}
	if clientSecret, err = url.QueryUnescape(pass); err != nil {
		return "", "", errInvalidClient
	}
	if form := r.PostForm.Get("client_id"); form != "" && form != clientID {
		return "", "", oauthErr(400, "invalid_request", "client_id does not match the authorization header")
	}
	return clientID, clientSecret, nil
}

func (s *Service) handleDeviceAuthorization(clientAddr ClientAddr) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxBody)
		if err := r.ParseForm(); err != nil {
			s.writeError(w, oauthErr(400, "invalid_request", "the body must be form-encoded"))
			return
		}
		resp, err := s.StartDevice(r.Context(), clientAddr(r), r.PostForm.Get("client_id"), r.PostForm.Get("resource"))
		if err != nil {
			s.writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, resp)
	}
}
