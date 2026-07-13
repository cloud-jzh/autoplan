package httpapi

import (
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"

	"github.com/lyming99/autoplan/backend/internal/config"
)

var errMultipartExtraPart = errors.New("multipart request contains more than one file part")

type multipartUpload struct {
	DisplayName string
	MIMEType    string
	Content     io.Reader
}

// parseMultipartUpload accepts exactly one file field named "file". The
// returned reader verifies the end boundary while the attachment service
// stages bytes, so trailing parts cannot be silently ignored.
func parseMultipartUpload(writer http.ResponseWriter, request *http.Request, limit int64) (multipartUpload, *APIError) {
	if request == nil || request.Body == nil || limit <= 0 || limit > config.MaximumBodyLimit {
		failure := NewAPIError(CodeInvalidAttachment, nil)
		return multipartUpload{}, &failure
	}
	mediaType, parameters, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/form-data" || strings.TrimSpace(parameters["boundary"]) == "" {
		failure := NewAPIError(CodeUnsupportedMediaType, nil)
		return multipartUpload{}, &failure
	}
	request.Body = http.MaxBytesReader(writer, request.Body, limit)
	reader := multipart.NewReader(request.Body, parameters["boundary"])
	part, err := reader.NextPart()
	if err != nil {
		failure := classifyMultipartError(err, limit)
		return multipartUpload{}, &failure
	}
	if part.FormName() != "file" || part.FileName() == "" || part.Header.Get("Content-Transfer-Encoding") != "" {
		_ = part.Close()
		failure := NewAPIError(CodeInvalidAttachment, nil)
		return multipartUpload{}, &failure
	}
	mimeType := ""
	if contentType := strings.TrimSpace(part.Header.Get("Content-Type")); contentType != "" {
		parsed, _, parseErr := mime.ParseMediaType(contentType)
		if parseErr != nil || parsed == "" {
			_ = part.Close()
			failure := NewAPIError(CodeInvalidAttachment, nil)
			return multipartUpload{}, &failure
		}
		// Browser defaults provide no useful type signal; the domain service will
		// use the extension and then verify the staged content signature.
		if parsed != "application/octet-stream" {
			mimeType = parsed
		}
	}
	return multipartUpload{
		DisplayName: part.FileName(), MIMEType: mimeType,
		Content: &singleMultipartReader{reader: reader, part: part},
	}, nil
}

func classifyMultipartError(err error, limit int64) APIError {
	var maximum *http.MaxBytesError
	if errors.As(err, &maximum) {
		return NewAPIError(CodeBodyTooLarge, &ErrorDetails{LimitBytes: limit})
	}
	return NewAPIError(CodeInvalidAttachment, nil)
}

type singleMultipartReader struct {
	reader *multipart.Reader
	part   *multipart.Part
	done   bool
}

func (reader *singleMultipartReader) Read(buffer []byte) (int, error) {
	if reader == nil || reader.part == nil || reader.reader == nil {
		return 0, io.ErrUnexpectedEOF
	}
	if reader.done {
		return 0, io.EOF
	}
	count, err := reader.part.Read(buffer)
	if err != io.EOF {
		return count, err
	}
	next, nextErr := reader.reader.NextPart()
	if nextErr == io.EOF {
		reader.done = true
		return count, io.EOF
	}
	if next != nil {
		_ = next.Close()
	}
	if nextErr != nil {
		return count, nextErr
	}
	return count, errMultipartExtraPart
}
