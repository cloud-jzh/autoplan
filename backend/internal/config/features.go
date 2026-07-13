package config

import "strings"

// RuntimeFeature names form the renderer/sidecar migration contract. They
// intentionally describe a family rather than an endpoint so one family can
// move without changing the owner of another accepted Operation.
type RuntimeFeature string

const (
	FeatureGoLoopActions            RuntimeFeature = "go_loop_actions"
	FeatureGoPlanActions            RuntimeFeature = "go_plan_actions"
	FeatureGoTaskActions            RuntimeFeature = "go_task_actions"
	FeatureGoAcceptanceRetryActions RuntimeFeature = "go_acceptance_retry_actions"
	FeatureGoScriptsAPI             RuntimeFeature = "go_scripts_api"
	FeatureGoExecutorsAPI           RuntimeFeature = "go_executors_api"
	// FeatureGoChatAPI gates only the P13A Chat REST/SSE adapter. It must
	// never imply that the MCP transport is enabled, that a legacy adapter is
	// removable, or that another runtime may take over an accepted Chat turn.
	FeatureGoChatAPI RuntimeFeature = "go_chat_api"
	// FeatureGoMCPAPI gates only the P13B MCP HTTP/stdio transport. It is
	// intentionally independent from Chat so a failed MCP rollout cannot
	// change Chat routing, authorize legacy removal, or take over in-flight
	// MCP work (and vice versa).
	FeatureGoMCPAPI RuntimeFeature = "go_mcp_api"
	// FeatureGoTerminalAPI gates P14 Terminal REST control and WebSocket data
	// together. It remains default-off until the per-platform packaged PTY
	// evidence is accepted; it never transfers an existing Node session or
	// authorizes removal of the independently gated Electron Node PTY path.
	FeatureGoTerminalAPI RuntimeFeature = "go_terminal_api"
	// FeatureGoAgentCLIRuntime is retained only to parse older launch
	// environments. It no longer authorizes Script or Executor routing.
	FeatureGoAgentCLIRuntime RuntimeFeature = "go_agent_cli_runtime"
)

const (
	EnvironmentGoLoopActions            = EnvironmentPrefix + "GO_LOOP_ACTIONS"
	EnvironmentGoPlanActions            = EnvironmentPrefix + "GO_PLAN_ACTIONS"
	EnvironmentGoTaskActions            = EnvironmentPrefix + "GO_TASK_ACTIONS"
	EnvironmentGoAcceptanceRetryActions = EnvironmentPrefix + "GO_ACCEPTANCE_RETRY_ACTIONS"
	EnvironmentGoScriptsAPI             = EnvironmentPrefix + "GO_SCRIPTS_API"
	EnvironmentGoExecutorsAPI           = EnvironmentPrefix + "GO_EXECUTORS_API"
	EnvironmentGoChatAPI                = EnvironmentPrefix + "GO_CHAT_API"
	EnvironmentGoMCPAPI                 = EnvironmentPrefix + "GO_MCP_API"
	EnvironmentGoTerminalAPI            = EnvironmentPrefix + "GO_TERMINAL_API"
	EnvironmentGoAgentCLIRuntime        = EnvironmentPrefix + "GO_AGENT_CLI_RUNTIME"
)

// RuntimeFeatures is fail-closed: every migration family remains Node-owned
// until it is explicitly enabled. It contains no per-request override, so an
// accepted Operation keeps the owner chosen at submission time.
type RuntimeFeatures struct {
	GoLoopActions            bool `json:"go_loop_actions"`
	GoPlanActions            bool `json:"go_plan_actions"`
	GoTaskActions            bool `json:"go_task_actions"`
	GoAcceptanceRetryActions bool `json:"go_acceptance_retry_actions"`
	GoScriptsAPI             bool `json:"go_scripts_api"`
	GoExecutorsAPI           bool `json:"go_executors_api"`
	GoChatAPI                bool `json:"go_chat_api"`
	GoMCPAPI                 bool `json:"go_mcp_api"`
	GoTerminalAPI            bool `json:"go_terminal_api"`
	GoAgentCLIRuntime        bool `json:"go_agent_cli_runtime"`
}

func DefaultRuntimeFeatures() RuntimeFeatures { return RuntimeFeatures{} }

func (value RuntimeFeatures) Enabled(feature RuntimeFeature) bool {
	switch feature {
	case FeatureGoLoopActions:
		return value.GoLoopActions
	case FeatureGoPlanActions:
		return value.GoPlanActions
	case FeatureGoTaskActions:
		return value.GoTaskActions
	case FeatureGoAcceptanceRetryActions:
		return value.GoAcceptanceRetryActions
	case FeatureGoScriptsAPI:
		return value.GoScriptsAPI
	case FeatureGoExecutorsAPI:
		return value.GoExecutorsAPI
	case FeatureGoChatAPI:
		return value.GoChatAPI
	case FeatureGoMCPAPI:
		return value.GoMCPAPI
	case FeatureGoTerminalAPI:
		return value.GoTerminalAPI
	case FeatureGoAgentCLIRuntime:
		return value.GoAgentCLIRuntime
	default:
		return false
	}
}

// RuntimeFeaturesFromEnvironment parses only the migration gates. It is
// kept separate from process configuration so an older sidecar rejects no
// unrelated environment while callers can still share one strict contract.
func RuntimeFeaturesFromEnvironment(environ []string) (RuntimeFeatures, error) {
	result := DefaultRuntimeFeatures()
	seen := make(map[string]struct{}, 10)
	for _, entry := range environ {
		name, raw, found := strings.Cut(entry, "=")
		if !found {
			continue
		}
		var target *bool
		switch name {
		case EnvironmentGoLoopActions:
			target = &result.GoLoopActions
		case EnvironmentGoPlanActions:
			target = &result.GoPlanActions
		case EnvironmentGoTaskActions:
			target = &result.GoTaskActions
		case EnvironmentGoAcceptanceRetryActions:
			target = &result.GoAcceptanceRetryActions
		case EnvironmentGoScriptsAPI:
			target = &result.GoScriptsAPI
		case EnvironmentGoExecutorsAPI:
			target = &result.GoExecutorsAPI
		case EnvironmentGoChatAPI:
			target = &result.GoChatAPI
		case EnvironmentGoMCPAPI:
			target = &result.GoMCPAPI
		case EnvironmentGoTerminalAPI:
			target = &result.GoTerminalAPI
		case EnvironmentGoAgentCLIRuntime:
			target = &result.GoAgentCLIRuntime
		default:
			continue
		}
		if _, duplicate := seen[name]; duplicate {
			return RuntimeFeatures{}, newError("runtime_feature_duplicate")
		}
		if raw == "true" {
			*target = true
		} else if raw == "false" {
			*target = false
		} else {
			return RuntimeFeatures{}, newError("runtime_feature_invalid")
		}
		seen[name] = struct{}{}
	}
	return result, nil
}
