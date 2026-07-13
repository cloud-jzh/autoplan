package filesystem

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	domainfiles "github.com/lyming99/autoplan/backend/internal/domain/files"
)

var controlledNames = map[domainfiles.Operation]*regexp.Regexp{
	domainfiles.OperationPlanMarkdown: regexp.MustCompile(`^plan_(?:requirement|feedback)_[0-9]+_[0-9]{8}-[0-9]{6}\.md$`),
	domainfiles.OperationPlanSpec:     regexp.MustCompile(`^plan_spec_(?:requirement|feedback)_[0-9]+_[0-9]{8}-[0-9]{6}\.json$`),
	domainfiles.OperationPlanManifest: regexp.MustCompile(`^(?:manifest|plan_manifest(?:_[A-Za-z0-9._-]+)?)\.json$`),
	domainfiles.OperationProgressLog:  regexp.MustCompile(`^[0-9]{8}-[0-9]{6}_[A-Za-z0-9._-]+\.log$`),
}

type ReparseDetector func(string, fs.FileInfo) bool

type Resolver struct {
	ReparseDetector ReparseDetector
}

func (resolver Resolver) NormalizeRoots(roots []string) ([]string, error) {
	result := make([]string, 0, len(roots))
	for _, candidate := range roots {
		if err := resolver.detectReparse(candidate); err != nil {
			return nil, err
		}
		root, err := ResolveExistingRoot(candidate)
		if err != nil {
			return nil, err
		}
		duplicate := false
		for _, existing := range result {
			if SameOrWithin(existing, root.Real) && SameOrWithin(root.Real, existing) {
				duplicate = true
				break
			}
		}
		if !duplicate {
			result = append(result, root.Real)
		}
	}
	sort.Strings(result)
	return result, nil
}

func (resolver Resolver) Authorize(request domainfiles.AuthorizationRequest) (domainfiles.Decision, error) {
	if request.Policy.Validate() != nil || request.Operation.Validate() != nil {
		return domainfiles.Decision{}, domainfiles.NewError(domainfiles.CodeInvalidPolicy, domainfiles.ErrInvalidPolicy)
	}
	if err := resolver.detectReparse(request.WorkspaceRoot); err != nil {
		return domainfiles.Decision{}, err
	}
	if err := resolver.detectReparse(request.TargetPath); err != nil {
		return domainfiles.Decision{}, err
	}
	workspace, err := ResolveExistingRoot(request.WorkspaceRoot)
	if err != nil {
		return domainfiles.Decision{}, err
	}
	if request.Operation.IsControlledWrite() {
		return authorizeControlled(request, workspace)
	}
	target, err := ResolveTarget(request.TargetPath, request.AllowMissing)
	if err != nil {
		return domainfiles.Decision{}, err
	}
	if request.Policy.UnrestrictedRead() && request.Operation.IsReadLike() {
		return domainfiles.Decision{
			Allowed: true, HighRisk: true, ResolvedTarget: target.Real,
			DisplayPath: "<all-path>", RootLabel: "<all>",
		}, nil
	}
	roots, err := effectiveRoots(request.Policy, workspace)
	if err != nil {
		return domainfiles.Decision{}, err
	}
	for _, root := range roots {
		if SameOrWithin(root.Real, target.Real) {
			return domainfiles.Decision{
				Allowed: true, ResolvedTarget: target.Real, DisplayPath: safeDisplay(root.Real, target.Real),
				RootLabel: root.label,
			}, nil
		}
	}
	for _, root := range roots {
		if SameOrWithin(root.Lexical, target.Lexical) {
			return domainfiles.Decision{}, domainfiles.NewError(domainfiles.CodeSymlinkEscape, domainfiles.ErrSymlinkEscape)
		}
	}
	return domainfiles.Decision{}, domainfiles.NewError(domainfiles.CodeOutsideScope, domainfiles.ErrOutsideScope)
}

func (resolver Resolver) detectReparse(value string) error {
	if resolver.ReparseDetector == nil {
		return nil
	}
	current, err := validateAbsolutePath(value)
	if err != nil {
		return err
	}
	for {
		info, statErr := os.Lstat(current)
		if statErr == nil && resolver.ReparseDetector(current, info) {
			return domainfiles.NewError(domainfiles.CodeSymlinkEscape, domainfiles.ErrSymlinkEscape)
		}
		parent := filepath.Dir(current)
		if parent == current {
			return nil
		}
		current = parent
	}
}

type labeledRoot struct {
	ResolvedPath
	label string
}

func effectiveRoots(policy domainfiles.Policy, workspace ResolvedPath) ([]labeledRoot, error) {
	result := []labeledRoot{{ResolvedPath: workspace, label: "<workspace>"}}
	if !policy.UsesCustomRoots() {
		return result, nil
	}
	for _, candidate := range policy.AllowedRoots {
		root, err := ResolveExistingRoot(candidate)
		if err != nil {
			return nil, err
		}
		duplicate := false
		for _, existing := range result {
			if SameOrWithin(existing.Real, root.Real) && SameOrWithin(root.Real, existing.Real) {
				duplicate = true
				break
			}
		}
		if !duplicate {
			result = append(result, labeledRoot{ResolvedPath: root, label: "<allowed-root>"})
		}
	}
	sort.SliceStable(result[1:], func(left, right int) bool { return result[left+1].Real < result[right+1].Real })
	return result, nil
}

func authorizeControlled(request domainfiles.AuthorizationRequest, workspace ResolvedPath) (domainfiles.Decision, error) {
	matcher := controlledNames[request.Operation]
	if matcher == nil || !matcher.MatchString(filepath.Base(request.TargetPath)) {
		return domainfiles.Decision{}, domainfiles.NewError(domainfiles.CodeControlledTarget, domainfiles.ErrControlledTarget)
	}
	directory := filepath.Join(workspace.Lexical, "docs", "plan")
	rootLabel := "<workspace>/docs/plan"
	if request.Operation == domainfiles.OperationProgressLog {
		directory = filepath.Join(workspace.Lexical, "docs", "progress", "logs")
		rootLabel = "<workspace>/docs/progress/logs"
	}
	if !SameOrWithin(directory, request.TargetPath) || filepath.Dir(filepath.Clean(request.TargetPath)) != filepath.Clean(directory) {
		return domainfiles.Decision{}, domainfiles.NewError(domainfiles.CodeControlledTarget, domainfiles.ErrControlledTarget)
	}
	target, err := ResolveTarget(request.TargetPath, true)
	if err != nil {
		return domainfiles.Decision{}, err
	}
	if !SameOrWithin(workspace.Real, target.Real) {
		return domainfiles.Decision{}, domainfiles.NewError(domainfiles.CodeSymlinkEscape, domainfiles.ErrSymlinkEscape)
	}
	relative, err := filepath.Rel(workspace.Real, target.Real)
	if err != nil || strings.HasPrefix(relative, "..") {
		return domainfiles.Decision{}, domainfiles.NewError(domainfiles.CodeControlledTarget, domainfiles.ErrControlledTarget)
	}
	return domainfiles.Decision{
		Allowed: true, Controlled: true, ResolvedTarget: target.Real,
		DisplayPath: filepath.ToSlash(relative), RootLabel: rootLabel,
	}, nil
}

func safeDisplay(root, target string) string {
	relative, err := filepath.Rel(root, target)
	if err != nil || relative == "." {
		return "."
	}
	return filepath.ToSlash(relative)
}
