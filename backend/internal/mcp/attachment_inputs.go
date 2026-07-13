// Package mcp adapts MCP-safe inputs to the shared application boundary.
package mcp

import (
	"bytes"
	"encoding/base64"
	"errors"
	"io"
	"strings"

	domainfiles "github.com/lyming99/autoplan/backend/internal/domain/files"
)

var (
	ErrInvalidAttachmentInput = errors.New("mcp attachment input is invalid")
	ErrAttachmentTooLarge     = errors.New("mcp attachment input exceeds limit")
	ErrAttachmentPathDenied   = errors.New("mcp attachment path input is denied")
)

// AttachmentInput is the transport-neutral MCP representation. A caller may
// provide exactly one byte source. Path is intentionally retained as opaque
// text here; only the attachment application service may authorize and open
// it through the P05 policy.
type AttachmentInput struct {
	Name     string
	MIMEType string
	Bytes    []byte
	Base64   string
	DataURL  string
	Path     string
}

type decodedAttachmentInput struct {
	Name     string
	MIMEType string
	Content  io.Reader
	Path     string
}

func decodeAttachmentInput(input AttachmentInput, localCaller bool) (decodedAttachmentInput, error) {
	result := decodedAttachmentInput{
		Name: strings.TrimSpace(input.Name), MIMEType: strings.TrimSpace(input.MIMEType), Path: strings.TrimSpace(input.Path),
	}
	if result.Name == "" || len(result.Name) > 120 {
		return decodedAttachmentInput{}, ErrInvalidAttachmentInput
	}
	sources := 0
	if input.Bytes != nil {
		sources++
	}
	if strings.TrimSpace(input.Base64) != "" {
		sources++
	}
	if strings.TrimSpace(input.DataURL) != "" {
		sources++
	}
	if result.Path != "" {
		sources++
	}
	if sources != 1 {
		return decodedAttachmentInput{}, ErrInvalidAttachmentInput
	}
	if result.Path != "" {
		if !localCaller {
			return decodedAttachmentInput{}, ErrAttachmentPathDenied
		}
		return result, nil
	}
	var content []byte
	var err error
	switch {
	case input.Bytes != nil:
		content = append([]byte(nil), input.Bytes...)
	case strings.TrimSpace(input.Base64) != "":
		content, err = decodeBase64(strings.TrimSpace(input.Base64))
	case strings.TrimSpace(input.DataURL) != "":
		result.MIMEType, content, err = decodeDataURL(strings.TrimSpace(input.DataURL), result.MIMEType)
	}
	if err != nil {
		return decodedAttachmentInput{}, err
	}
	if len(content) == 0 {
		return decodedAttachmentInput{}, ErrInvalidAttachmentInput
	}
	if int64(len(content)) > domainfiles.MaximumAttachmentBytes {
		return decodedAttachmentInput{}, ErrAttachmentTooLarge
	}
	result.Content = bytes.NewReader(content)
	return result, nil
}

func decodeBase64(value string) ([]byte, error) {
	if len(value) > base64.StdEncoding.EncodedLen(int(domainfiles.MaximumAttachmentBytes)) {
		return nil, ErrAttachmentTooLarge
	}
	decoded, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		decoded, err = base64.RawStdEncoding.DecodeString(value)
	}
	if err != nil {
		return nil, ErrInvalidAttachmentInput
	}
	if int64(len(decoded)) > domainfiles.MaximumAttachmentBytes {
		return nil, ErrAttachmentTooLarge
	}
	return decoded, nil
}

func decodeDataURL(value, declaredMIME string) (string, []byte, error) {
	if len(value) > base64.StdEncoding.EncodedLen(int(domainfiles.MaximumAttachmentBytes))+512 {
		return "", nil, ErrAttachmentTooLarge
	}
	if !strings.HasPrefix(value, "data:") {
		return "", nil, ErrInvalidAttachmentInput
	}
	parts := strings.SplitN(strings.TrimPrefix(value, "data:"), ",", 2)
	if len(parts) != 2 {
		return "", nil, ErrInvalidAttachmentInput
	}
	metadata := strings.Split(parts[0], ";")
	if len(metadata) < 2 || !strings.EqualFold(metadata[len(metadata)-1], "base64") {
		return "", nil, ErrInvalidAttachmentInput
	}
	mimeType := strings.TrimSpace(metadata[0])
	if mimeType == "" {
		return "", nil, ErrInvalidAttachmentInput
	}
	if declared := domainfiles.NormalizeMIMEType(declaredMIME); declared != "" && declared != domainfiles.NormalizeMIMEType(mimeType) {
		return "", nil, ErrInvalidAttachmentInput
	}
	decoded, err := decodeBase64(parts[1])
	if err != nil {
		return "", nil, err
	}
	return mimeType, decoded, nil
}
