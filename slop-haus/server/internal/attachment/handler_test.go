package attachment_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-auth/authn/authntest"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/attachment"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
)

type httpRig struct {
	*fixture
	mux    *http.ServeMux
	tokens map[string]*authn.User
}

// newHTTPRig mounts the routes behind the real signed-in-user middleware, so
// what is tested is the wire: status, headers, and whose files a token reaches.
func newHTTPRig(t *testing.T, enabled bool) *httpRig {
	t.Helper()
	f := newFixture(t)
	r := &httpRig{fixture: f, mux: http.NewServeMux(), tokens: map[string]*authn.User{}}
	api := humago.New(r.mux, huma.DefaultConfig("test", "0.0.0"))
	api.UseMiddleware(authn.UserMiddleware(api, func(_ context.Context, token string) (*authn.User, error) {
		if u, ok := r.tokens[token]; ok {
			return u, nil
		}
		return nil, errors.New("unknown token")
	}, authntest.Guard(), slog.New(slog.DiscardHandler)))
	svc := f.svc
	if !enabled {
		svc = nil
	}
	attachment.RegisterRoutes(api, attachment.NewHandler(svc, slog.DiscardHandler))
	return r
}

func (r *httpRig) signIn(t *tenantRig) string {
	token := uuid.NewString()
	r.tokens[token] = &authn.User{ID: t.user.ID, TenantID: t.id}
	return token
}

func (r *httpRig) do(method, path, token string, body []byte, contentType string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	r.mux.ServeHTTP(rec, req)
	return rec
}

func multipartBody(t *testing.T, field, filename string, content []byte) ([]byte, string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition", `form-data; name="`+field+`"; filename="`+filename+`"`)
	h.Set("Content-Type", "text/html") // what the client claims is ignored
	part, err := w.CreatePart(h)
	require.NoError(t, err)
	_, err = part.Write(content)
	require.NoError(t, err)
	require.NoError(t, w.Close())
	return buf.Bytes(), w.FormDataContentType()
}

func (r *httpRig) postFile(t *testing.T, path, token, filename string, content []byte) *httptest.ResponseRecorder {
	t.Helper()
	body, ct := multipartBody(t, "file", filename, content)
	return r.do(http.MethodPost, path, token, body, ct)
}

func ticketPath(t *tenantRig) string { return "/api/tickets/" + itoa(t.ticket) + "/attachments" }

func itoa(n int64) string { b, _ := json.Marshal(n); return string(b) }

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &v), rec.Body.String())
	return v
}

func TestHTTPUploadListContentDelete(t *testing.T) {
	r := newHTTPRig(t, true)
	tn := r.newTenant()
	token := r.signIn(tn)

	rec := r.postFile(t, ticketPath(tn), token, "my shot.png", pngBytes)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	created := decode[attachment.AttachmentBody](t, rec)
	assert.Equal(t, "image/png", created.ContentType)
	assert.Equal(t, "my shot.png", created.Filename)
	assert.Equal(t, "user", created.UploaderKind)

	rec = r.do(http.MethodGet, ticketPath(tn), token, nil, "")
	require.Equal(t, http.StatusOK, rec.Code)
	listed := decode[struct {
		Attachments []attachment.AttachmentBody `json:"attachments"`
	}](t, rec)
	require.Len(t, listed.Attachments, 1)

	rec = r.do(http.MethodGet, "/api/attachments/"+created.ID.String(), token, nil, "")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, pngBytes, rec.Body.Bytes())
	assert.Equal(t, "image/png", rec.Header().Get("Content-Type"))
	assert.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))
	assert.Equal(t, "sandbox", rec.Header().Get("Content-Security-Policy"))
	assert.Equal(t, `inline; filename="my shot.png"`, rec.Header().Get("Content-Disposition"))

	rec = r.do(http.MethodGet, "/api/attachments/"+created.ID.String()+"?download=true", token, nil, "")
	assert.Equal(t, `attachment; filename="my shot.png"`, rec.Header().Get("Content-Disposition"))

	rec = r.do(http.MethodDelete, "/api/attachments/"+created.ID.String(), token, nil, "")
	assert.Equal(t, http.StatusNoContent, rec.Code)
	rec = r.do(http.MethodGet, "/api/attachments/"+created.ID.String(), token, nil, "")
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

// The filename is the client's: it is quoted into the header, never pasted.
func TestHTTPContentDispositionEscapesTheFilename(t *testing.T) {
	r := newHTTPRig(t, true)
	tn := r.newTenant()
	token := r.signIn(tn)
	a := r.mustUpload(tn, `a"; filename=evil.html; x="`+"\r\nSet-Cookie: x=1", pngBytes)

	rec := r.do(http.MethodGet, "/api/attachments/"+a.ID.String(), token, nil, "")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Empty(t, rec.Header().Get("Set-Cookie"))
	assert.NotContains(t, rec.Header().Get("Content-Disposition"), "\n")
	assert.Contains(t, rec.Header().Get("Content-Disposition"), "inline")
}

func TestHTTPRefusals(t *testing.T) {
	r := newHTTPRig(t, true)
	tn := r.newTenant()
	token := r.signIn(tn)

	assert.Equal(t, http.StatusUnauthorized, r.postFile(t, ticketPath(tn), "", "a.png", pngBytes).Code)
	assert.Equal(t, http.StatusUnprocessableEntity, r.postFile(t, ticketPath(tn), token, "a.png", svgBytes).Code, "SVG")
	assert.Equal(t, http.StatusUnprocessableEntity, r.postFile(t, ticketPath(tn), token, "a.html", htmlBytes).Code, "HTML")

	body, ct := multipartBody(t, "other", "a.png", pngBytes)
	assert.Equal(t, http.StatusUnprocessableEntity, r.do(http.MethodPost, ticketPath(tn), token, body, ct).Code, "no file field")
	assert.Equal(t, http.StatusUnprocessableEntity, r.do(http.MethodPost, ticketPath(tn), token, []byte("{}"), "application/json").Code, "not multipart")

	big := r.postFile(t, ticketPath(tn), token, "big.png", pngOf(int(attachment.MaxImageBytes)+1))
	assert.Equal(t, http.StatusRequestEntityTooLarge, big.Code)

	assert.Equal(t, http.StatusNotFound, r.postFile(t, "/api/tickets/999999/attachments", token, "a.png", pngBytes).Code)
	assert.Equal(t, http.StatusNotFound, r.do(http.MethodGet, "/api/attachments/"+uuid.NewString(), token, nil, "").Code)
	assert.Zero(t, r.count(tn, `SELECT count(*) FROM attachments`))
}

func TestHTTPQuotaAnswers413(t *testing.T) {
	r := newHTTPRig(t, true)
	r.quota = 100
	tn := r.newTenant()
	rec := r.postFile(t, ticketPath(tn), r.signIn(tn), "a.png", pngOf(200))
	assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
	assert.Contains(t, rec.Body.String(), "quota")
}

func TestHTTPAnotherTenantGets404(t *testing.T) {
	r := newHTTPRig(t, true)
	a, b := r.newTenant(), r.newTenant()
	tokenB := r.signIn(b)
	mine := r.mustUpload(a, "a.png", pngBytes)

	assert.Equal(t, http.StatusNotFound, r.do(http.MethodGet, "/api/attachments/"+mine.ID.String(), tokenB, nil, "").Code)
	assert.Equal(t, http.StatusNotFound, r.do(http.MethodDelete, "/api/attachments/"+mine.ID.String(), tokenB, nil, "").Code)
	assert.Equal(t, 1, r.count(a, `SELECT count(*) FROM attachments`))
}

func TestHTTPStagedUploadIsForTheOwner(t *testing.T) {
	r := newHTTPRig(t, true)
	tn := r.newTenant()
	owner := r.signIn(tn)
	other := r.newTenant()
	other.id = tn.id // a colleague: same tenant, another person
	colleague := r.signIn(other)
	conv := r.conversation(tn, tn.user.ID)
	path := "/api/conversations/" + conv.String() + "/attachments"

	rec := r.postFile(t, path, owner, "s.png", pngBytes)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	staged := decode[attachment.AttachmentBody](t, rec)
	assert.Nil(t, staged.TicketID)

	assert.Equal(t, http.StatusNotFound, r.postFile(t, path, colleague, "s.png", pngBytes).Code)
	assert.Equal(t, http.StatusNotFound, r.do(http.MethodGet, "/api/attachments/"+staged.ID.String(), colleague, nil, "").Code)
	assert.Equal(t, http.StatusOK, r.do(http.MethodGet, "/api/attachments/"+staged.ID.String(), owner, nil, "").Code)
}

func TestHTTPWithoutAnObjectStoreAnswers503(t *testing.T) {
	r := newHTTPRig(t, false)
	tn := r.newTenant()
	token := r.signIn(tn)
	assert.Equal(t, http.StatusServiceUnavailable, r.postFile(t, ticketPath(tn), token, "a.png", pngBytes).Code)
	assert.Equal(t, http.StatusServiceUnavailable, r.do(http.MethodGet, ticketPath(tn), token, nil, "").Code)
}

func TestErrTableCoversEverySentinel(t *testing.T) {
	attachment.RequireErrTableCovers(t)
}
