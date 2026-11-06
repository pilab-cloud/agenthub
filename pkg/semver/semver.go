// Package semver provides Semantic Version 2.0 constraint resolution
// for AgentHub asset version matching and dependency resolution.
package semver

import (
	"fmt"
	"strings"
)

// Version represents a Semantic Version 2.0 version.
type Version struct {
	Major int
	Minor int
	Patch int
	// PreRelease is the pre-release tag (e.g., "alpha", "1.0.0-beta1")
	PreRelease string
	// Build is the build metadata (per SemVer 2.0, ignored for comparison)
	Build string
}

// Constraint represents a SemVer constraint (e.g., ^1.4.0, >=2.0.0, *).
type Constraint struct {
	// Operator is one of: >=, >, <=, <, =, !=, or "" for default
	Operator string
	// Version is the version to compare against, or "" for wildcards like *
	Version *Version
	// IncludePrerelease indicates if pre-releases are considered satisfying
	IncludePrerelease bool
}

// IsSatisfiedBy checks if a version satisfies this constraint.
func (c *Constraint) IsSatisfiedBy(v *Version) bool {
	if c.Version == nil {
		// Wildcard "*" matches any version
		return true
	}

	// Compare versions
	cmp := compare(c.Version, v)

	switch c.Operator {
	case "", "=": // default constraint or explicit equality
		if cmp == 0 {
			return true
		}
		return false
	case ">":
		return cmp > 0
	case ">=":
		return cmp >= 0
	case "<":
		return cmp < 0
	case "<=":
		return cmp <= 0
	case "!=":
		return cmp != 0
	case "^":
		return c.matchCaret(v)
	default:
		return false
	}
}

// matchCaret checks if a version matches a caret constraint (^major.minor.patch).
// ^1.4.0 is compatible with >=1.4.0 <2.0.0
// ^0.4.0 is compatible with >=0.4.0 <0.5.0
// ^0.0.4 is compatible with >=0.0.4 <0.0.5
func (c *Constraint) matchCaret(v *Version) bool {
	if c.Version == nil {
		return false
	}

	// Parse the caret major.minor.patch
	major := c.Version.Major
	minor := c.Version.Minor
	patch := c.Version.Patch

	// Caret rules:
	// - If major > 0: ^major.minor.patch compatible with >=major.minor.patch <(major+1).0.0
	// - If major = 0 and minor > 0: ^0.minor.patch compatible with >=0.minor.patch <0.(minor+1).0
	// - If major = 0 and minor = 0: ^0.0.patch compatible with >=0.0.patch <0.0.(patch+1)
	if major > 0 {
		// >=major.minor.patch AND <(major+1).0.0
		lower := &Version{Major: major, Minor: minor, Patch: patch}
		upper := &Version{Major: major + 1, Minor: 0, Patch: 0}
		if !isGe(v, lower) || isGe(v, upper) {
			return false
		}
		return true
	}

	// major = 0
	if minor > 0 {
		lower := &Version{Major: 0, Minor: minor, Patch: patch}
		upper := &Version{Major: 0, Minor: minor + 1, Patch: 0}
		if !isGe(v, lower) || isGe(v, upper) {
			return false
		}
		return true
	}

	// major = 0, minor = 0
	lower := &Version{Major: 0, Minor: 0, Patch: patch}
	upper := &Version{Major: 0, Minor: 0, Patch: patch + 1}
	if !isGe(v, lower) || isGe(v, upper) {
		return false
	}
	return true
}

// isGe returns true if a >= b.
func isGe(a, b *Version) bool {
	if a.Major != b.Major {
		return a.Major > b.Major
	}
	if a.Minor != b.Minor {
		return a.Minor > b.Minor
	}
	return a.Patch >= b.Patch
}

// Less reports whether a < b.
func Less(a, b *Version) bool {
	if a.Major != b.Major {
		return a.Major < b.Major
	}
	if a.Minor != b.Minor {
		return a.Minor < b.Minor
	}
	if a.Patch != b.Patch {
		return a.Patch < b.Patch
	}
	// Pre-release: a pre-release version is less than the release version
	if a.PreRelease == "" && b.PreRelease != "" {
		return false // release > pre-release
	}
	if a.PreRelease != "" && b.PreRelease == "" {
		return true // pre-release < release
	}
	if a.PreRelease != "" && b.PreRelease != "" {
		return comparePreRelease(a.PreRelease, b.PreRelease) < 0
	}
	return false
}

// compare compares two versions and returns -1, 0, or 1.
func compare(a, b *Version) int {
	if a.Major != b.Major {
		if a.Major < b.Major {
			return -1
		}
		return 1
	}
	if a.Minor != b.Minor {
		if a.Minor < b.Minor {
			return -1
		}
		return 1
	}
	if a.Patch != b.Patch {
		if a.Patch < b.Patch {
			return -1
		}
		return 1
	}
	// Pre-release handling
	if a.PreRelease == "" && b.PreRelease == "" {
		return 0
	}
	if a.PreRelease == "" {
		return 1 // release > pre-release
	}
	if b.PreRelease == "" {
		return -1 // release > pre-release
	}
	return comparePreRelease(a.PreRelease, b.PreRelease)
}

// comparePreRelease compares two pre-release identifiers.
// Numeric identifiers are compared as integers.
// Alpha < beta < rc < release.
func comparePreRelease(a, b string) int {
	// Split by dots
	aParts := strings.Split(a, ".")
	bParts := strings.Split(b, ".")

	// Compare part by part
	maxLen := len(aParts)
	if len(bParts) > maxLen {
		maxLen = len(bParts)
	}

	for i := 0; i < maxLen; i++ {
		var aNum, bNum int
		var aStr, bStr string

		if i < len(aParts) {
			ap := aParts[i]
			num, err := atoi(ap)
			if err == nil && num != 0 {
				aNum = num
				aStr = ""
			} else {
				aStr = ap
				aNum = 0
			}
		}

		if i < len(bParts) {
			bp := bParts[i]
			num, err := atoi(bp)
			if err == nil && num != 0 {
				bNum = num
				bStr = ""
			} else {
				bStr = bp
				bNum = 0
			}
		}

		// Numeric < string
		if aStr != "" && bStr == "" {
			return -1 // numeric string is "less than" having no identifier
		}
		if aStr == "" && bStr != "" {
			return 1
		}

		if aStr < bStr {
			return -1
		}
		if aStr > bStr {
			return 1
		}

		if aNum < bNum {
			return -1
		}
		if aNum > bNum {
			return 1
		}
	}

	// Shorter version is "less than" if all compared parts are equal
	if len(aParts) < len(bParts) {
		// Check if remaining b parts are all zeros/empty
		for i := len(aParts); i < len(bParts); i++ {
			num, err := atoi(bParts[i])
			if err == nil && num != 0 {
				return -1 // b has more non-zero identifiers, so a < b
			}
		}
		return 0 // treat as equal
	}
	if len(aParts) > len(bParts) {
		for i := len(bParts); i < len(aParts); i++ {
			num, err := atoi(aParts[i])
			if err == nil && num != 0 {
				return 1 // a has more non-zero identifiers, so a > b
			}
		}
		return 0
	}

	return 0
}

// isNumeric reports whether s consists entirely of digits.
func isNumeric(s string) (bool, error) {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false, nil
		}
	}
	return true, nil
}

// atoi parses a string to int; returns 0 on failure.
func atoi(s string) (int, error) {
	var n int
	for _, c := range s {
		n = n*10 + int(c-'0')
	}
	return n, nil
}

// ParseVersion parses a semver string (MAJOR.MINOR.PATCH) into a Version.
func ParseVersion(s string) (*Version, error) {
	parts := strings.SplitN(s, "-", 2)
	versionPart := parts[0]
	preRelease := ""
	if len(parts) > 1 {
		preRelease = parts[1]
	}

	v := strings.Split(versionPart, ".")
	if len(v) != 3 {
		return nil, fmt.Errorf("invalid semver version: %s", s)
	}

	var major, minor, patch int
	var err error
	major, err = atoi(v[0])
	if err != nil {
		return nil, fmt.Errorf("invalid major version: %s", v[0])
	}
	minor, err = atoi(v[1])
	if err != nil {
		return nil, fmt.Errorf("invalid minor version: %s", v[1])
	}
	patch, err = atoi(v[2])
	if err != nil {
		return nil, fmt.Errorf("invalid patch version: %s", v[2])
	}

	return &Version{
		Major: major,
		Minor: minor,
		Patch: patch,
		PreRelease: preRelease,
	}, nil
}

// ParseConstraint parses a constraint string like ^1.4.0, >=2.0.0, *.
func ParseConstraint(s string) (*Constraint, error) {
	s = strings.TrimSpace(s)

	// Handle wildcard
	if s == "*" {
		return &Constraint{
			Operator:   "",
			IncludePrerelease: true,
		}, nil
	}

	// Parse operator and version
	var op string
	var verStr string

	// Check for known operators (longest first)
	if strings.HasPrefix(s, ">=") {
		op = ">="
		verStr = s[2:]
	} else if strings.HasPrefix(s, "<=") {
		op = "<="
		verStr = s[2:]
	} else if strings.HasPrefix(s, "!=") {
		op = "!="
		verStr = s[2:]
	} else if strings.HasPrefix(s, ">") {
		op = ">"
		verStr = s[1:]
	} else if strings.HasPrefix(s, "<") {
		op = "<"
		verStr = s[1:]
	} else if strings.HasPrefix(s, "=") {
		op = "="
		verStr = s[1:]
	} else {
		// No explicit operator, treat as default (equality or caret)
		// Check for caret
		if strings.HasPrefix(s, "^") {
			verStr = s[1:]
			op = "^"
		} else {
			// Default to equality
			op = ""
			verStr = s
		}
	}

	ver, err := ParseVersion(verStr)
	if err != nil {
		return nil, fmt.Errorf("invalid constraint version: %s", verStr)
	}

	return &Constraint{
		Operator:         op,
		Version:          ver,
		IncludePrerelease: strings.HasPrefix(s, "^") || strings.HasPrefix(s, "="),
	}, nil
}

// SatisfiedBy checks if a version string satisfies a constraint string.
func SatisfiedBy(versionStr, constraintStr string) (bool, error) {
	v, err := ParseVersion(versionStr)
	if err != nil {
		return false, fmt.Errorf("invalid version: %w", err)
	}

	c, err := ParseConstraint(constraintStr)
	if err != nil {
		return false, fmt.Errorf("invalid constraint: %w", err)
	}

	return c.IsSatisfiedBy(v), nil
}

// Max returns the highest version from a list that satisfies the constraint.
// Returns nil if none satisfy.
func Max(constraint *Constraint, versions ...*Version) *Version {
	var result *Version
	for _, v := range versions {
		if constraint.IsSatisfiedBy(v) {
			if result == nil || Less(result, v) {
				result = v
			}
		}
	}
	return result
}

// Min returns the lowest version from a list that satisfies the constraint.
// Returns nil if none satisfy.
func Min(constraint *Constraint, versions ...*Version) *Version {
	var result *Version
	for _, v := range versions {
		if constraint.IsSatisfiedBy(v) {
			if result == nil || Less(v, result) {
				result = v
			}
		}
	}
	return result
}