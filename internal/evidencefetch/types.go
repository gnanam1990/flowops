package evidencefetch

import (
	"errors"
	"fmt"
	"regexp"
	"time"
)

const (
	ModeAuto       = "auto"
	ModeText       = "text"
	minOutputBytes = 4
)

var digestPattern = regexp.MustCompile(`^0x[0-9a-f]{64}$`)

type Request struct {
	URL            string `json:"url"`
	Mode           string `json:"mode,omitempty"`
	MaxOutputBytes int64  `json:"maxOutputBytes,omitempty"`
	RequestDigest  string `json:"requestDigest"`
}

func (r Request) validate(maxOutputBytes int64) error {
	if !digestPattern.MatchString(r.RequestDigest) {
		return newError(CodeInvalidRequest, "requestDigest must be a canonical lowercase 32-byte hex digest", nil)
	}
	if r.Mode != "" && r.Mode != ModeAuto && r.Mode != ModeText {
		return newError(CodeInvalidRequest, "mode must be auto or text", nil)
	}
	if r.MaxOutputBytes < 0 || r.MaxOutputBytes > maxOutputBytes || r.MaxOutputBytes > 0 && r.MaxOutputBytes < minOutputBytes {
		return newError(CodeInvalidRequest, fmt.Sprintf("maxOutputBytes must be zero or between %d and %d", minOutputBytes, maxOutputBytes), nil)
	}
	return nil
}

type HTTPMetadata struct {
	StatusCode    int    `json:"statusCode"`
	ContentType   string `json:"contentType"`
	ContentLength int64  `json:"contentLength,omitempty"`
	ETag          string `json:"etag,omitempty"`
	LastModified  string `json:"lastModified,omitempty"`
}

type Result struct {
	SourceURL     string       `json:"sourceUrl"`
	FinalURL      string       `json:"finalUrl"`
	FetchedAt     time.Time    `json:"fetchedAt"`
	RequestDigest string       `json:"requestDigest"`
	FetchDigest   string       `json:"fetchDigest"`
	Text          string       `json:"text"`
	TextTruncated bool         `json:"textTruncated"`
	ContentSHA256 string       `json:"contentSha256"`
	SourceSHA256  string       `json:"sourceSha256"`
	HTTP          HTTPMetadata `json:"http"`
}

type Code string

const (
	CodeInvalidRequest     Code = "INVALID_REQUEST"
	CodeUnsafeURL          Code = "UNSAFE_URL"
	CodeResolutionFailed   Code = "RESOLUTION_FAILED"
	CodeUpstreamFailure    Code = "UPSTREAM_FAILURE"
	CodeUnsupportedContent Code = "UNSUPPORTED_CONTENT"
	CodeResponseTooLarge   Code = "RESPONSE_TOO_LARGE"
	CodeEmptyEvidence      Code = "EMPTY_EVIDENCE"
)

type Error struct {
	Code    Code   `json:"code"`
	Message string `json:"message"`
	cause   error
}

func (e *Error) Error() string {
	return string(e.Code) + ": " + e.Message
}

func (e *Error) Unwrap() error { return e.cause }

func newError(code Code, message string, cause error) *Error {
	return &Error{Code: code, Message: message, cause: cause}
}

func ErrorCode(err error) Code {
	var fetchErr *Error
	if errors.As(err, &fetchErr) {
		return fetchErr.Code
	}
	return CodeUpstreamFailure
}
