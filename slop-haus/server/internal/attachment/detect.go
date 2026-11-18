package attachment

import (
	"bytes"
	"net/http"
)

// fileType is an allowed kind of file. The extension is the one the server
// puts on the storage key; the client's is never used.
type fileType struct {
	contentType string
	extension   string
	maxBytes    int64
}

var (
	typePNG  = fileType{"image/png", ".png", MaxImageBytes}
	typeJPEG = fileType{"image/jpeg", ".jpg", MaxImageBytes}
	typeWebP = fileType{"image/webp", ".webp", MaxImageBytes}
	typeGIF  = fileType{"image/gif", ".gif", MaxImageBytes}
	typeMP4  = fileType{"video/mp4", ".mp4", MaxVideoBytes}
	typeWebM = fileType{"video/webm", ".webm", MaxVideoBytes}
	typeMOV  = fileType{"video/quicktime", ".mov", MaxVideoBytes}
)

// sniffLen is how much of a file detect needs.
const sniffLen = 512

// mp4Brands are the ftyp major brands taken for MP4. HEIC and the like share
// the box layout and are deliberately not here.
var mp4Brands = []string{"isom", "iso2", "iso4", "iso5", "iso6", "mp41", "mp42", "avc1", "dash", "M4V "}

// detect decides what a file is from its first bytes, against the allowlist.
// SVG and HTML sniff as text and are refused. Go's sniffer does not know
// QuickTime (macOS screen recordings), so its ftyp box is read here.
func detect(head []byte) (fileType, error) {
	if len(head) >= 12 && bytes.Equal(head[4:8], []byte("ftyp")) {
		brand := string(head[8:12])
		if brand == "qt  " {
			return typeMOV, nil
		}
		for _, b := range mp4Brands {
			if brand == b {
				return typeMP4, nil
			}
		}
	}
	switch http.DetectContentType(head) {
	case "image/png":
		return typePNG, nil
	case "image/jpeg":
		return typeJPEG, nil
	case "image/webp":
		return typeWebP, nil
	case "image/gif":
		return typeGIF, nil
	case "video/mp4":
		return typeMP4, nil
	case "video/webm":
		return typeWebM, nil
	}
	return fileType{}, ErrUnsupportedType
}
