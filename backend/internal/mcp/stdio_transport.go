package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
)

// serveStdioTransport writes protocol frames only to output. Diagnostics use
// one bounded, constant message on stderr so malformed input can never echo a
// token, request body, path, or implementation error.
func serveStdioTransport(ctx context.Context, server *Server, input io.Reader, output, diagnostic io.Writer) error {
	if server == nil || input == nil || output == nil {
		return ErrTransportInvalid
	}
	scanner := bufio.NewScanner(input)
	maximum := int(server.config.BodyLimitBytes)
	scanner.Buffer(make([]byte, 0, minInt(maximum, 4096)), maximum+1)
	writer := bufio.NewWriterSize(output, minInt(maximum, 64<<10))
	defer writer.Flush()
	for scanner.Scan() {
		if err := contextError(ctx); err != nil {
			return err
		}
		frame := bytes.TrimSpace(scanner.Bytes())
		if len(frame) == 0 {
			continue
		}
		requestContext, cancel := context.WithTimeout(ctx, server.config.RequestTimeout)
		response, respond := server.processFrame(requestContext, append([]byte(nil), frame...), TransportStdio, ToolContext{CallerScope: "mcp-stdio"})
		cancel()
		if !respond || response == nil {
			continue
		}
		if err := json.NewEncoder(writer).Encode(response); err != nil {
			return ErrTransportInvalid
		}
		if err := writer.Flush(); err != nil {
			return ErrTransportInvalid
		}
	}
	if err := scanner.Err(); err != nil {
		if diagnostic != nil {
			_, _ = io.WriteString(diagnostic, "mcp stdio transport stopped\n")
		}
		return ErrTransportInvalid
	}
	return nil
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}
