// Package savepath maps deploy-time-configured destination aliases to
// filesystem paths.
//
// MCP tools that accept a save-path input (qbit_add_download,
// qbit_subscribe) take an alias name — "kura-inbox", "downloads" — or
// an alias-prefixed form "<alias>:<relpath>" to target a subdirectory
// under the alias root. The operator declares the alias→path mapping
// at deploy time via --save-paths / QBITTORRENT_SAVE_PATHS, and the
// server rejects any name not on that list. Untrusted agents cannot
// redirect download storage to arbitrary locations.
package savepath

import (
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
)

// ReservedUnspecified is the sentinel prefix used in OUTPUT projections
// when a qBittorrent path does not fall under any configured alias.
// Output form is "unspecified:<path-sans-leading-slash>".
//
// It is REJECTED on input (Resolve) so agents cannot bypass the
// destination-alias security boundary. The same name is rejected at
// boot so an operator cannot accidentally shadow the sentinel.
const ReservedUnspecified = "unspecified"

// Resolver maps alias names to filesystem paths. Construct with New or
// Parse; pass by reference into the MCP server so tool handlers can
// translate caller-supplied destination names into upstream save_path
// values.
type Resolver struct {
	aliases map[string]string
	names   []string
}

// aliasName allows lowercase letters, digits, and hyphens; must start with
// a letter or digit. Chosen to keep names URL-safe, log-safe, and obvious
// to type. Reject anything else loudly so misconfigurations surface at
// boot rather than at first tool call.
var aliasName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// New constructs a Resolver from a name→path map. Returns an error if any
// name is malformed, collides with the reserved "unspecified" sentinel,
// or any path is empty.
func New(aliases map[string]string) (*Resolver, error) {
	out := &Resolver{aliases: make(map[string]string, len(aliases))}
	for name, p := range aliases {
		if !aliasName.MatchString(name) {
			return nil, fmt.Errorf("invalid destination name %q (must match [a-z0-9][a-z0-9-]*)", name)
		}
		if name == ReservedUnspecified {
			return nil, fmt.Errorf("destination name %q is reserved as the no-alias output sentinel", name)
		}
		if p == "" {
			return nil, fmt.Errorf("destination %q has empty path", name)
		}
		out.aliases[name] = p
	}
	out.names = make([]string, 0, len(out.aliases))
	for name := range out.aliases {
		out.names = append(out.names, name)
	}
	sort.Strings(out.names)
	return out, nil
}

// Parse decodes a flag-style "name=path,name=path" string into a Resolver.
// Empty input is allowed and yields a Resolver with no aliases — callers
// can still pass an empty destination to leave the upstream save_path
// unset (qBittorrent's account default applies).
func Parse(s string) (*Resolver, error) {
	aliases := map[string]string{}
	if strings.TrimSpace(s) == "" {
		return New(aliases)
	}
	for _, kv := range strings.Split(s, ",") {
		kv = strings.TrimSpace(kv)
		if kv == "" {
			continue
		}
		eq := strings.IndexByte(kv, '=')
		if eq <= 0 {
			return nil, fmt.Errorf("destination entry %q must be name=path", kv)
		}
		name := strings.TrimSpace(kv[:eq])
		p := strings.TrimSpace(kv[eq+1:])
		if _, dup := aliases[name]; dup {
			return nil, fmt.Errorf("duplicate destination name %q", name)
		}
		aliases[name] = p
	}
	return New(aliases)
}

// Resolve returns the filesystem path for a destination input, which is
// either a bare alias name ("kura-inbox") or the prefixed form
// "<alias>:<relpath>" pointing at a subdirectory under the alias root.
// Empty input returns ("", nil) — callers should treat that as "leave
// the upstream save_path unset".
//
// Relpath validation: must not be absolute, must not contain ".."
// segments that would escape the alias root. Inputs that fail these
// checks return an error rather than silently joining to an
// outside-the-alias path.
//
// The reserved "unspecified" prefix is rejected; it is an output-only
// sentinel and accepting it on input would bypass the destination
// boundary.
func (r *Resolver) Resolve(input string) (string, error) {
	if input == "" {
		return "", nil
	}
	name, rel, err := splitDestination(input)
	if err != nil {
		return "", err
	}
	if name == ReservedUnspecified {
		return "", fmt.Errorf("destination %q is reserved as an output-only sentinel and cannot be used on input", input)
	}
	root, ok := r.aliases[name]
	if !ok {
		if len(r.names) == 0 {
			return "", fmt.Errorf("unknown destination %q (no destinations configured on this deployment)", name)
		}
		return "", fmt.Errorf("unknown destination %q (configured: %s)", name, strings.Join(r.names, ", "))
	}
	if rel == "" {
		return root, nil
	}
	return strings.TrimRight(root, "/") + "/" + rel, nil
}

// splitDestination parses a destination input of the form "<name>" or
// "<name>:<relpath>" and returns the components, validating the relpath
// against absolute paths and ".." escapes.
func splitDestination(input string) (name, rel string, err error) {
	i := strings.IndexByte(input, ':')
	if i < 0 {
		return input, "", nil
	}
	name = input[:i]
	rel = input[i+1:]
	if rel == "" || rel == "." {
		return name, "", nil
	}
	if path.IsAbs(rel) {
		return "", "", fmt.Errorf("destination relpath %q cannot be absolute (no leading '/')", rel)
	}
	if strings.ContainsRune(rel, 0) {
		return "", "", fmt.Errorf("destination relpath contains a null byte")
	}
	cleaned := path.Clean(rel)
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", "", fmt.Errorf("destination relpath %q cannot escape the alias root with '..'", rel)
	}
	if cleaned == "." {
		return name, "", nil
	}
	return name, cleaned, nil
}

// Names returns the configured alias names in deterministic order.
// Suitable for embedding in tool descriptions so agents know which values
// are valid.
func (r *Resolver) Names() []string { return r.names }

// NameForPath reverse-resolves a filesystem path to the alias name that
// maps to it, returning "" when no alias matches. Used by output
// projections that need just the alias name without the prefixed form.
//
// Returns the alphabetically-first match when two aliases share a path
// (a misconfiguration the operator would normally not create).
func (r *Resolver) NameForPath(p string) string {
	if p == "" {
		return ""
	}
	for _, name := range r.names {
		if r.aliases[name] == p {
			return name
		}
	}
	return ""
}

// NameForPathPrefixed reverse-resolves a filesystem path into the
// destination-prefixed wire form used by every tool output projection:
//
//	"<alias>"                exact root match
//	"<alias>:<relpath>"      nested under an alias root
//	"unspecified:<abs-sans-leading-slash>"   path covered by no alias
//
// Empty input returns the empty string (no transformation). Every
// non-empty input maps to one of the three forms; callers never need a
// raw-absolute-path fallback.
//
// When multiple aliases nest (e.g. "a" -> "/x" and "b" -> "/x/y") the
// longest-matching alias wins. Path-component boundary is enforced:
// "/mnt/kura-other/foo" does NOT match an alias rooted at "/mnt/kura".
// Trailing slashes on either side are tolerated.
func (r *Resolver) NameForPathPrefixed(p string) string {
	if p == "" {
		return ""
	}
	trimmed := strings.TrimRight(p, "/")
	bestName := ""
	bestRoot := ""
	for _, name := range r.names {
		root := strings.TrimRight(r.aliases[name], "/")
		if root == "" {
			continue
		}
		if trimmed == root || strings.HasPrefix(trimmed, root+"/") {
			if len(root) > len(bestRoot) {
				bestName = name
				bestRoot = root
			}
		}
	}
	if bestName != "" {
		if trimmed == bestRoot {
			return bestName
		}
		return bestName + ":" + strings.TrimPrefix(trimmed, bestRoot+"/")
	}
	// No alias covers this path. Emit the sentinel form so every wire
	// path is parseable as "<prefix>:<rest>" with no raw-absolute escape
	// hatch on the agent side. Leading slash is stripped for format
	// symmetry; the path is unambiguously absolute when the prefix is
	// "unspecified".
	return ReservedUnspecified + ":" + strings.TrimPrefix(trimmed, "/")
}

// DescriptionHint formats the configured aliases for inclusion in an MCP
// tool description.
func (r *Resolver) DescriptionHint() string {
	if len(r.names) == 0 {
		return "No destinations are configured on this deployment; leave empty to use qBittorrent's account default."
	}
	return "Valid destinations: " + strings.Join(r.names, ", ") + ". Pass '<name>' for the alias root or '<name>:<relpath>' to target a subdirectory (relpath must not start with '/' or contain '..'). Leave empty to use qBittorrent's account default."
}
