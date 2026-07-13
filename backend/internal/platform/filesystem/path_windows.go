package filesystem

import (
	"errors"
	"regexp"
	"strings"
)

var (
	errInvalidWindowsPath = errors.New("invalid Windows path")
	windowsDrivePattern   = regexp.MustCompile(`^[A-Za-z]:\\`)
	windowsReservedName   = regexp.MustCompile(`(?i)^(?:CON|PRN|AUX|NUL|COM[1-9]|LPT[1-9])(?:\..*)?$`)
)

// NormalizeWindowsPath is lexical validation used even by non-Windows tests.
// It rejects device namespaces, ADS, reserved names and incomplete UNC roots.
func NormalizeWindowsPath(value string) (string, error) {
	if value == "" || strings.ContainsRune(value, 0) {
		return "", errInvalidWindowsPath
	}
	value = strings.ReplaceAll(value, "/", `\`)
	if strings.HasPrefix(value, `\\.\`) || strings.HasPrefix(value, `\??\`) {
		return "", errInvalidWindowsPath
	}
	if strings.HasPrefix(strings.ToLower(value), `\\?\unc\`) {
		value = `\\` + value[len(`\\?\UNC\`):]
	} else if strings.HasPrefix(value, `\\?\`) {
		value = value[len(`\\?\`):]
	}
	if !windowsDrivePattern.MatchString(value) && !strings.HasPrefix(value, `\\`) {
		return "", errInvalidWindowsPath
	}
	volumeEnd := 2
	if strings.HasPrefix(value, `\\`) {
		parts := nonEmptyWindowsParts(value)
		if len(parts) < 2 {
			return "", errInvalidWindowsPath
		}
		volumeEnd = len(`\\` + parts[0] + `\` + parts[1])
	}
	for index, component := range strings.Split(value, `\`) {
		if component == "" || component == "." {
			continue
		}
		if component == ".." || strings.HasSuffix(component, " ") || strings.HasSuffix(component, ".") ||
			windowsReservedName.MatchString(component) {
			return "", errInvalidWindowsPath
		}
		if strings.Contains(component, ":") && !(index == 0 && len(component) == 2) {
			return "", errInvalidWindowsPath
		}
	}
	cleaned := cleanWindowsComponents(value)
	if len(cleaned) < volumeEnd {
		return "", errInvalidWindowsPath
	}
	return cleaned, nil
}

func WindowsPathWithin(root, target string) bool {
	root, rootErr := NormalizeWindowsPath(root)
	target, targetErr := NormalizeWindowsPath(target)
	if rootErr != nil || targetErr != nil {
		return false
	}
	root = strings.TrimRight(strings.ToLower(root), `\`)
	target = strings.TrimRight(strings.ToLower(target), `\`)
	return target == root || strings.HasPrefix(target, root+`\`)
}

func nonEmptyWindowsParts(value string) []string {
	parts := make([]string, 0)
	for _, part := range strings.Split(value, `\`) {
		if part != "" {
			parts = append(parts, part)
		}
	}
	return parts
}

func cleanWindowsComponents(value string) string {
	prefix := ""
	parts := nonEmptyWindowsParts(value)
	if strings.HasPrefix(value, `\\`) {
		prefix = `\\`
	}
	return prefix + strings.Join(parts, `\`)
}
