package fetch

import (
	"mime"
	"unicode/utf8"

	"golang.org/x/net/html/charset"
)

// toUTF8 decodes a text body to UTF-8, the only encoding the HTML parser and
// the Markdown converter read. A charset from a byte order mark or the
// Content-Type header wins; a <meta> declaration or the fallback guess only
// counts when the body is not valid UTF-8 already, as many pages declare a
// legacy charset and send UTF-8. The content type comes back with its
// charset set to utf-8 when the body changed.
func toUTF8(body []byte, contentType string) ([]byte, string) {
	enc, name, certain := charset.DetermineEncoding(body, contentType)
	if name == "utf-8" || (!certain && utf8.Valid(body)) {
		return body, contentType
	}
	out, err := enc.NewDecoder().Bytes(body)
	if err != nil {
		return body, contentType
	}
	mt, params, err := mime.ParseMediaType(contentType)
	if err != nil { // no usable type to label; the body is valid UTF-8 now, which a later guess keeps
		return out, contentType
	}
	params["charset"] = "utf-8"
	return out, mime.FormatMediaType(mt, params)
}
