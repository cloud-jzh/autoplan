package files

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	domainfiles "github.com/lyming99/autoplan/backend/internal/domain/files"
)

var (
	ErrControlledWriterUnavailable = errors.New("controlled writer is unavailable")
	ErrControlledWrite             = errors.New("controlled file write was denied")
	ErrControlledContent           = errors.New("controlled file content is invalid")
)

const (
	OpenCodePlanAgentName = "autoplan-plan"
	maximumPromptBytes    = 512 << 10
)

// WriteAuthorizer is the write-only policy facet required by Agent CLI. It is
// intentionally narrower than Service so runtime code cannot alter policies.
type WriteAuthorizer interface {
	Authorize(context.Context, domainfiles.Operation, string, string, bool) (domainfiles.Decision, error)
}

// ControlledWriter owns the tiny set of runtime-created workspace artifacts.
// It uses Files Policy before and after directory creation and never exposes
// content or absolute paths in its errors.
type ControlledWriter struct{ authorizer WriteAuthorizer }

func NewControlledWriter(authorizer WriteAuthorizer) *ControlledWriter {
	return &ControlledWriter{authorizer: authorizer}
}

// PromptAttachment is internal runtime state, not a transport DTO. Path is
// passed directly to OpenCode as a separate argument and must be removed with
// RemoveOpenCodePrompt once the launch completes.
type PromptAttachment struct {
	Workspace string
	Path      string
}

// EnsureOpenCodePlanAgent writes the fixed, constrained plan agent below the
// project workspace. Only its stable agent name is returned; callers never
// need to persist or publish the filesystem path.
func (writer *ControlledWriter) EnsureOpenCodePlanAgent(ctx context.Context, workspace string) (string, error) {
	target := filepath.Join(workspace, ".opencode", "agents", OpenCodePlanAgentName+".md")
	if err := writer.write(ctx, workspace, target, []byte(openCodePlanAgentContent())); err != nil {
		return "", err
	}
	return OpenCodePlanAgentName, nil
}

// AuthorizeAgentOutput reserves the Codex last-output location. The Agent CLI
// writes this file itself, so it is authorized before launch and never exposed
// as part of an ordinary runtime result.
func (writer *ControlledWriter) AuthorizeAgentOutput(ctx context.Context, workspace, target string) error {
	if writer == nil || writer.authorizer == nil {
		return ErrControlledWriterUnavailable
	}
	decision, err := writer.authorizer.Authorize(ctx, domainfiles.OperationProgressLog, workspace, target, true)
	if err != nil || !decision.Allowed || !decision.Controlled || decision.ResolvedTarget == "" {
		return ErrControlledWrite
	}
	workspaceReal, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		return ErrControlledWrite
	}
	workspaceReal, err = filepath.Abs(workspaceReal)
	if err != nil {
		return ErrControlledWrite
	}
	relative, err := filepath.Rel(filepath.Clean(workspaceReal), filepath.Clean(decision.ResolvedTarget))
	if err != nil || filepath.ToSlash(filepath.Dir(relative)) != "docs/progress/logs" {
		return ErrControlledWrite
	}
	if err := ensureControlledDirectory(workspaceReal, filepath.Dir(relative)); err != nil {
		return ErrControlledWrite
	}
	decision, err = writer.authorizer.Authorize(ctx, domainfiles.OperationProgressLog, workspace, target, true)
	if err != nil || !decision.Allowed || !decision.Controlled || decision.ResolvedTarget == "" {
		return ErrControlledWrite
	}
	return nil
}

// WriteOpenCodePrompt stores only a prompt that cannot safely fit a positional
// argument (newlines or a long value). The caller must remove the attachment
// after the child has been reaped.
func (writer *ControlledWriter) WriteOpenCodePrompt(ctx context.Context, workspace, prompt string) (PromptAttachment, error) {
	if prompt == "" || len(prompt) > maximumPromptBytes || !utf8.ValidString(prompt) || strings.ContainsRune(prompt, 0) {
		return PromptAttachment{}, ErrControlledContent
	}
	name, err := randomName()
	if err != nil {
		return PromptAttachment{}, ErrControlledWrite
	}
	target := filepath.Join(workspace, ".autoplan-agentcli", "prompts", "prompt-"+name+".md")
	if err := writer.write(ctx, workspace, target, []byte(prompt)); err != nil {
		return PromptAttachment{}, err
	}
	return PromptAttachment{Workspace: workspace, Path: target}, nil
}

// RemoveOpenCodePrompt is idempotent and validates the generated path again
// through Files Policy before deleting it.
func (writer *ControlledWriter) RemoveOpenCodePrompt(ctx context.Context, attachment PromptAttachment) error {
	if writer == nil || writer.authorizer == nil || !validPromptAttachment(attachment) {
		return ErrControlledWrite
	}
	if _, err := os.Lstat(attachment.Path); errors.Is(err, fs.ErrNotExist) {
		return nil
	} else if err != nil {
		return ErrControlledWrite
	}
	if _, err := writer.authorize(ctx, attachment.Workspace, attachment.Path, false); err != nil {
		return ErrControlledWrite
	}
	if err := os.Remove(attachment.Path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return ErrControlledWrite
	}
	return nil
}

func (writer *ControlledWriter) write(ctx context.Context, workspace, target string, content []byte) error {
	if writer == nil || writer.authorizer == nil || len(content) == 0 || len(content) > maximumPromptBytes ||
		!utf8.Valid(content) || strings.ContainsRune(string(content), 0) {
		return ErrControlledContent
	}
	decision, err := writer.authorize(ctx, workspace, target, true)
	if err != nil {
		return ErrControlledWrite
	}
	workspaceReal, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		return ErrControlledWrite
	}
	workspaceReal, err = filepath.Abs(workspaceReal)
	if err != nil {
		return ErrControlledWrite
	}
	relative, err := filepath.Rel(filepath.Clean(workspaceReal), filepath.Clean(decision.ResolvedTarget))
	if err != nil || !validControlledRelative(relative) {
		return ErrControlledWrite
	}
	if err := ensureControlledDirectory(workspaceReal, filepath.Dir(relative)); err != nil {
		return ErrControlledWrite
	}
	decision, err = writer.authorize(ctx, workspace, target, true)
	if err != nil {
		return ErrControlledWrite
	}
	if err := atomicWrite(decision.ResolvedTarget, content); err != nil {
		return ErrControlledWrite
	}
	return nil
}

func (writer *ControlledWriter) authorize(ctx context.Context, workspace, target string, allowMissing bool) (domainfiles.Decision, error) {
	decision, err := writer.authorizer.Authorize(ctx, domainfiles.OperationGenericWrite, workspace, target, allowMissing)
	if err != nil || !decision.Allowed || decision.ResolvedTarget == "" {
		return domainfiles.Decision{}, ErrControlledWrite
	}
	return decision, nil
}

func ensureControlledDirectory(workspace, relative string) error {
	if relative == "." || relative == "" || strings.HasPrefix(relative, "..") {
		return ErrControlledWrite
	}
	current := workspace
	for _, part := range strings.Split(filepath.Clean(relative), string(filepath.Separator)) {
		if part == "" || part == "." || part == ".." {
			return ErrControlledWrite
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, fs.ErrNotExist) {
			if err := os.Mkdir(current, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
				return err
			}
			info, err = os.Lstat(current)
		}
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return ErrControlledWrite
		}
	}
	return nil
}

func atomicWrite(target string, content []byte) error {
	if info, err := os.Lstat(target); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return ErrControlledWrite
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	temporary := target + ".autoplan-tmp-"
	name, err := randomName()
	if err != nil {
		return err
	}
	temporary += name
	file, err := os.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer os.Remove(temporary)
	if _, err := file.Write(content); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(temporary, target)
}

func validControlledRelative(relative string) bool {
	normalized := filepath.ToSlash(filepath.Clean(relative))
	return normalized == ".opencode/agents/autoplan-plan.md" ||
		strings.HasPrefix(normalized, ".autoplan-agentcli/prompts/prompt-") && strings.HasSuffix(normalized, ".md")
}

func validPromptAttachment(attachment PromptAttachment) bool {
	if attachment.Workspace == "" || attachment.Path == "" || strings.ContainsRune(attachment.Workspace, 0) || strings.ContainsRune(attachment.Path, 0) {
		return false
	}
	relative, err := filepath.Rel(attachment.Workspace, attachment.Path)
	return err == nil && validControlledRelative(relative) && strings.HasPrefix(filepath.Base(attachment.Path), "prompt-")
}

func randomName() (string, error) {
	buffer := make([]byte, 12)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return hex.EncodeToString(buffer), nil
}

func openCodePlanAgentContent() string {
	return `---
description: "AutoPlan constrained plan generation agent"
mode: primary
temperature: 0.2
permission:
  read: allow
  edit: allow
  glob: allow
  grep: allow
  list: deny
  bash: deny
  task: deny
  webfetch: deny
  websearch: deny
  question: deny
  external_directory: deny
---

You generate only the requested plan artifact. Follow the user-provided task
scope, write only the explicitly named output file, do not modify business
code, do not run commands, do not browse the network, and do not ask follow-up
questions. Use the smallest necessary workspace context.
`
}

var _ WriteAuthorizer = (*Service)(nil)
