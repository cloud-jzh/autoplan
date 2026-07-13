package tools

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"

	domainintake "github.com/lyming99/autoplan/backend/internal/domain/intake"
	"github.com/lyming99/autoplan/backend/internal/mcp"
)

const maximumToolText = 65536

func decodeObject(source json.RawMessage, allowed ...string) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(source))
	decoder.DisallowUnknownFields()
	var value map[string]json.RawMessage
	if err := decoder.Decode(&value); err != nil || value == nil {
		return nil, invalidInput()
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, invalidInput()
	}
	permitted := make(map[string]struct{}, len(allowed))
	for _, key := range allowed {
		permitted[key] = struct{}{}
	}
	for key := range value {
		if _, ok := permitted[key]; !ok {
			return nil, invalidInput()
		}
	}
	return value, nil
}

func requiredInt(value map[string]json.RawMessage, name string) (int64, error) {
	raw, exists := value[name]
	if !exists {
		return 0, invalidInput()
	}
	var result int64
	if json.Unmarshal(raw, &result) != nil || result <= 0 {
		return 0, invalidInput()
	}
	return result, nil
}

func optionalInt(value map[string]json.RawMessage, name string) (*int64, error) {
	raw, exists := value[name]
	if !exists {
		return nil, nil
	}
	var result int64
	if json.Unmarshal(raw, &result) != nil || result <= 0 {
		return nil, invalidInput()
	}
	return &result, nil
}

func optionalString(value map[string]json.RawMessage, name string, maximum int) (*string, error) {
	raw, exists := value[name]
	if !exists {
		return nil, nil
	}
	var result string
	if json.Unmarshal(raw, &result) != nil || len(result) > maximum || strings.ContainsAny(result, "\x00") {
		return nil, invalidInput()
	}
	return &result, nil
}

func requiredString(value map[string]json.RawMessage, name string, maximum int) (string, error) {
	result, err := optionalString(value, name, maximum)
	if err != nil || result == nil || strings.TrimSpace(*result) == "" {
		return "", invalidInput()
	}
	return *result, nil
}

func optionalBool(value map[string]json.RawMessage, name string) (*bool, error) {
	raw, exists := value[name]
	if !exists {
		return nil, nil
	}
	var result bool
	if json.Unmarshal(raw, &result) != nil {
		return nil, invalidInput()
	}
	return &result, nil
}

func optionalLimit(value map[string]json.RawMessage) (int, error) {
	raw, exists := value["limit"]
	if !exists {
		return 100, nil
	}
	var limit int
	if json.Unmarshal(raw, &limit) != nil || limit < 1 || limit > 200 {
		return 0, invalidInput()
	}
	return limit, nil
}

func optionalStatus(value map[string]json.RawMessage, name string) (*domainintake.Status, error) {
	text, err := optionalString(value, name, 32)
	if err != nil || text == nil {
		return nil, err
	}
	status := domainintake.Status(*text)
	if !status.Valid() {
		return nil, invalidInput()
	}
	return &status, nil
}

func caller(value mcp.ToolContext) mcp.ToolContext {
	value.CallerScope = strings.TrimSpace(value.CallerScope)
	if value.CallerScope == "" {
		value.CallerScope = "mcp-local"
	}
	value.RequestID = strings.TrimSpace(value.RequestID)
	if value.RequestID == "" {
		value.RequestID = "mcp-request"
	}
	return value
}

func decodeIntakeCreate(source json.RawMessage, feedback bool) (mcp.CreateRequest, error) {
	allowed := []string{"projectId", "title", "body", "status", "requirementId", "agentCliProvider", "agentCliCommand", "codexReasoningEffort", "planGenerationStrategy", "planGenerationProvider", "planGenerationCommand", "planGenerationModel", "planGenerationCodexReasoningEffort", "attachments", "autoRun"}
	value, err := decodeObject(source, allowed...)
	if err != nil {
		return mcp.CreateRequest{}, err
	}
	if raw, exists := value["attachments"]; exists && string(bytes.TrimSpace(raw)) != "[]" {
		return mcp.CreateRequest{}, invalidInput()
	}
	if _, err := optionalBool(value, "autoRun"); err != nil {
		return mcp.CreateRequest{}, err
	}
	projectID, err := requiredInt(value, "projectId")
	if err != nil {
		return mcp.CreateRequest{}, err
	}
	title, err := requiredString(value, "title", 200)
	if err != nil {
		return mcp.CreateRequest{}, err
	}
	body, err := requiredString(value, "body", 100000)
	if err != nil {
		return mcp.CreateRequest{}, err
	}
	status := domainintake.StatusOpen
	if configured, err := optionalStatus(value, "status"); err != nil {
		return mcp.CreateRequest{}, err
	} else if configured != nil {
		if *configured == domainintake.StatusDraft {
			return mcp.CreateRequest{}, invalidInput()
		}
		status = *configured
	}
	request := mcp.CreateRequest{ProjectID: projectID, Title: title, Body: body, Status: status}
	if feedback {
		request.RequirementID, err = optionalInt(value, "requirementId")
		if err != nil {
			return mcp.CreateRequest{}, err
		}
	}
	request.AgentCLI.Provider, err = optionalString(value, "agentCliProvider", 64)
	if err != nil {
		return mcp.CreateRequest{}, err
	}
	if command, err := optionalString(value, "agentCliCommand", 1000); err != nil {
		return mcp.CreateRequest{}, err
	} else if command != nil {
		request.AgentCLI.Command = *command
	}
	request.AgentCLI.CodexReasoningEffort, err = optionalString(value, "codexReasoningEffort", 16)
	if err != nil {
		return mcp.CreateRequest{}, err
	}
	request.PlanGeneration.Strategy, err = optionalString(value, "planGenerationStrategy", 64)
	if err != nil {
		return mcp.CreateRequest{}, err
	}
	request.PlanGeneration.Provider, err = optionalString(value, "planGenerationProvider", 64)
	if err != nil {
		return mcp.CreateRequest{}, err
	}
	if command, err := optionalString(value, "planGenerationCommand", 1000); err != nil {
		return mcp.CreateRequest{}, err
	} else if command != nil {
		request.PlanGeneration.Command = *command
	}
	if model, err := optionalString(value, "planGenerationModel", 200); err != nil {
		return mcp.CreateRequest{}, err
	} else if model != nil {
		request.PlanGeneration.Model = *model
	}
	request.PlanGeneration.CodexReasoningEffort, err = optionalString(value, "planGenerationCodexReasoningEffort", 16)
	if err != nil {
		return mcp.CreateRequest{}, err
	}
	return request, nil
}

func decodeIntakeUpdate(source json.RawMessage, feedback bool) (mcp.UpdateRequest, error) {
	allowed := []string{"projectId", "id", "expectedUpdatedAt", "title", "body", "status", "requirementId", "agentCliProvider", "agentCliCommand", "codexReasoningEffort", "planGenerationStrategy", "planGenerationProvider", "planGenerationCommand", "planGenerationModel", "planGenerationCodexReasoningEffort"}
	value, err := decodeObject(source, allowed...)
	if err != nil {
		return mcp.UpdateRequest{}, err
	}
	projectID, err := requiredInt(value, "projectId")
	if err != nil {
		return mcp.UpdateRequest{}, err
	}
	id, err := requiredInt(value, "id")
	if err != nil {
		return mcp.UpdateRequest{}, err
	}
	request := mcp.UpdateRequest{ProjectID: projectID, ID: id}
	if expected, err := optionalString(value, "expectedUpdatedAt", 64); err != nil {
		return mcp.UpdateRequest{}, err
	} else if expected != nil {
		request.ExpectedUpdatedAt = *expected
	}
	if title, err := optionalString(value, "title", 200); err != nil {
		return mcp.UpdateRequest{}, err
	} else {
		request.Title = title
	}
	if body, err := optionalString(value, "body", 100000); err != nil {
		return mcp.UpdateRequest{}, err
	} else {
		request.Body = body
	}
	if status, err := optionalStatus(value, "status"); err != nil {
		return mcp.UpdateRequest{}, err
	} else {
		request.Status = status
	}
	if feedback {
		if raw, exists := value["requirementId"]; exists {
			request.RequirementID.Set = true
			if string(bytes.TrimSpace(raw)) != "null" {
				request.RequirementID.Value, err = optionalInt(value, "requirementId")
				if err != nil {
					return mcp.UpdateRequest{}, err
				}
			}
		}
	}
	return request, nil
}

func decodeAttachment(source json.RawMessage) (mcp.AttachmentInput, error) {
	value, err := decodeObject(source, "projectId", "id", "name", "type", "path", "base64", "dataBase64", "dataUrl", "bytes")
	if err != nil {
		return mcp.AttachmentInput{}, err
	}
	name, err := requiredString(value, "name", 120)
	if err != nil {
		return mcp.AttachmentInput{}, err
	}
	input := mcp.AttachmentInput{Name: name}
	if mime, err := optionalString(value, "type", 200); err != nil {
		return mcp.AttachmentInput{}, err
	} else if mime != nil {
		input.MIMEType = *mime
	}
	if path, err := optionalString(value, "path", 2000); err != nil {
		return mcp.AttachmentInput{}, err
	} else if path != nil {
		input.Path = *path
	}
	if base64, err := optionalString(value, "base64", 40<<20); err != nil {
		return mcp.AttachmentInput{}, err
	} else if base64 != nil {
		input.Base64 = *base64
	}
	if base64, err := optionalString(value, "dataBase64", 40<<20); err != nil {
		return mcp.AttachmentInput{}, err
	} else if base64 != nil {
		input.Base64 = *base64
	}
	if dataURL, err := optionalString(value, "dataUrl", 40<<20); err != nil {
		return mcp.AttachmentInput{}, err
	} else if dataURL != nil {
		input.DataURL = *dataURL
	}
	if raw, exists := value["bytes"]; exists {
		if json.Unmarshal(raw, &input.Bytes) != nil || len(input.Bytes) == 0 {
			return mcp.AttachmentInput{}, invalidInput()
		}
	}
	return input, nil
}

func toolResult(value any) (mcp.ToolResult, error) {
	encoded, err := json.Marshal(value)
	if err != nil || len(encoded) == 0 || len(encoded) > maximumToolText {
		return mcp.ToolResult{}, mcp.ToolError{Code: "mcp_tool_internal"}
	}
	return mcp.ToolResult{Content: []mcp.ToolTextContent{{Type: "text", Text: string(encoded)}}, StructuredContent: value}, nil
}

func invalidInput() error { return mcp.ToolError{Code: "mcp_tool_invalid"} }
