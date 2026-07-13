// Package tools provides the P13B MCP adapters. It owns protocol decoding and
// result projection only; all authorization, transactions, idempotency,
// Files policy, Operations, and audit decisions stay in application services.
package tools

import "github.com/lyming99/autoplan/backend/internal/mcp"

const (
	ListProjects                = "list_projects"
	GetProject                  = "get_project"
	CreateProject               = "create_project"
	ListRequirements            = "list_requirements"
	CreateRequirement           = "create_requirement"
	GetRequirement              = "get_requirement"
	UpdateRequirement           = "update_requirement"
	DeleteRequirement           = "delete_requirement"
	ListRequirementPlanLinks    = "list_requirement_plan_links"
	ReplaceRequirementPlanLinks = "replace_requirement_plan_links"
	UploadRequirementAttachment = "upload_requirement_attachment"
	ListFeedback                = "list_feedback"
	CreateFeedback              = "create_feedback"
	GetFeedback                 = "get_feedback"
	UpdateFeedback              = "update_feedback"
	DeleteFeedback              = "delete_feedback"
	ListFeedbackPlanLinks       = "list_feedback_plan_links"
	ReplaceFeedbackPlanLinks    = "replace_feedback_plan_links"
	UploadFeedbackAttachment    = "upload_feedback_attachment"
	DeleteAttachment            = "delete_attachment"
	ListPlans                   = "list_plans"
	GetPlan                     = "get_plan"
	ListTasks                   = "list_tasks"
	ListExecutors               = "list_executors"
	RunExecutor                 = "run_executor"
	StopExecutor                = "stop_executor"
	StartLoop                   = "start_loop"
	StopLoop                    = "stop_loop"
)

// Catalog returns a fresh copy of P007's immutable transport catalog. The
// descriptors are shared by HTTP and stdio; this package only supplies their
// common handler factory.
func Catalog() []mcp.ToolDescriptor { return mcp.FrozenToolDescriptors() }

func knownTool(name string) bool {
	switch name {
	case ListProjects, GetProject, CreateProject,
		ListRequirements, CreateRequirement, GetRequirement, UpdateRequirement, DeleteRequirement,
		ListRequirementPlanLinks, ReplaceRequirementPlanLinks, UploadRequirementAttachment,
		ListFeedback, CreateFeedback, GetFeedback, UpdateFeedback, DeleteFeedback,
		ListFeedbackPlanLinks, ReplaceFeedbackPlanLinks, UploadFeedbackAttachment, DeleteAttachment,
		ListPlans, GetPlan, ListTasks, ListExecutors, RunExecutor, StopExecutor, StartLoop, StopLoop:
		return true
	default:
		return false
	}
}
