package attachment_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/attachment"
)

// What is stored is decided from the bytes, never the name or declared type.
func TestTypeComesFromTheBytes(t *testing.T) {
	f := newFixture(t)
	r := f.newTenant()
	jpeg := append([]byte{0xff, 0xd8, 0xff, 0xe0}, bytes.Repeat([]byte{0}, 40)...)
	gif := append([]byte("GIF89a"), bytes.Repeat([]byte{0}, 40)...)
	webp := append([]byte("RIFF\x24\x00\x00\x00WEBPVP8 "), bytes.Repeat([]byte{0}, 40)...)

	for _, tc := range []struct {
		name, contentType, ext string
		content                []byte
	}{
		{"a.txt", "image/png", ".png", pngBytes},
		{"b.png", "image/jpeg", ".jpg", jpeg},
		{"c.png", "image/gif", ".gif", gif},
		{"d.png", "image/webp", ".webp", webp},
		{"e.png", "video/mp4", ".mp4", mp4Bytes(64)},
		{"f.png", "video/webm", ".webm", webmBytes},
		// macOS screen recordings: Go's sniffer calls these octet-stream.
		{"g.mov", "video/quicktime", ".mov", movBytes(64)},
	} {
		a := f.mustUpload(r, tc.name, tc.content)
		assert.Equal(t, tc.contentType, a.ContentType, tc.name)
		assert.True(t, strings.HasSuffix(a.StorageKey, tc.ext), "%s: key %s", tc.name, a.StorageKey)
		assert.Equal(t, a.ID.String()+tc.ext, a.StorageKey, "the key is the id and the server's extension, nothing of the filename")
		assert.Equal(t, tc.name, a.Filename)
	}
}

func TestRefusesWhatIsNotAnImageOrVideo(t *testing.T) {
	f := newFixture(t)
	r := f.newTenant()
	for name, content := range map[string][]byte{
		"svg":         svgBytes,
		"html":        htmlBytes,
		"text":        []byte("just some text, long enough to sniff as plain text"),
		"heic":        ftyp("heic", 64),
		"pdf":         []byte("%PDF-1.7 not an image"),
		"zip":         []byte("PK\x03\x04 not an image"),
		"svg as .png": svgBytes,
	} {
		_, err := f.upload(r, "x.png", content)
		assert.ErrorIs(t, err, attachment.ErrUnsupportedType, name)
	}
	_, err := f.upload(r, "x.png", nil)
	assert.ErrorIs(t, err, attachment.ErrEmpty)
	assert.Zero(t, f.count(r, `SELECT count(*) FROM attachments`))
	assert.Zero(t, f.count(r, `SELECT count(*) FROM attachment_reservations`))
}

func TestSizeLimitsDependOnTheDetectedType(t *testing.T) {
	f := newFixture(t)
	r := f.newTenant()

	_, err := f.upload(r, "big.png", pngOf(int(attachment.MaxImageBytes)+1))
	require.ErrorIs(t, err, attachment.ErrTooLarge)
	f.mustUpload(r, "ok.png", pngOf(int(attachment.MaxImageBytes)))

	// A video may be bigger than an image may.
	f.mustUpload(r, "clip.mov", movBytes(int(attachment.MaxImageBytes)+1))
	_, err = f.upload(r, "huge.mov", movBytes(int(attachment.MaxVideoBytes)+1))
	require.ErrorIs(t, err, attachment.ErrTooLarge)

	assert.Zero(t, f.count(r, `SELECT count(*) FROM attachment_reservations`), "a refused upload reserves nothing")
	assert.Equal(t, 2, f.count(r, `SELECT count(*) FROM attachments`))
}
