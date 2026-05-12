// Package savepath maps deploy-time-configured destination aliases to
// filesystem paths.
//
// MCP tools that accept a save-path input (add_download, update_downloads,
// set_subscription) take an alias *name* — "kura-inbox", "general-downloads" —
// not a raw filesystem path. The operator declares the alias→path mapping
// at deploy time via --save-paths / QBITTORRENT_SAVE_PATHS, and the server
// rejects any name not on that list. Untrusted agents cannot redirect
// download storage to arbitrary locations.
package savepath

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

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
// name is malformed or any path is empty.
func New(aliases map[string]string) (*Resolver, error) {
	out := &Resolver{aliases: make(map[string]string, len(aliases))}
	for name, path := range aliases {
		if !aliasName.MatchString(name) {
			return nil, fmt.Errorf("invalid destination name %q (must match [a-z0-9][a-z0-9-]*)", name)
		}
		if path == "" {
			return nil, fmt.Errorf("destination %q has empty path", name)
		}
		out.aliases[name] = path
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
		path := strings.TrimSpace(kv[eq+1:])
		if _, dup := aliases[name]; dup {
			return nil, fmt.Errorf("duplicate destination name %q", name)
		}
		aliases[name] = path
	}
	return New(aliases)
}

// Resolve returns the filesystem path for the given alias. Empty input is
// allowed and returns ("", nil) — callers should treat that as "leave the
// upstream save_path unset". Unknown names return an error; tool handlers
// wrap that as a *ToolError with code invalid_argument.
func (r *Resolver) Resolve(name string) (string, error) {
	if name == "" {
		return "", nil
	}
	path, ok := r.aliases[name]
	if !ok {
		if len(r.names) == 0 {
			return "", fmt.Errorf("unknown destination %q (no destinations configured on this deployment)", name)
		}
		return "", fmt.Errorf("unknown destination %q (configured: %s)", name, strings.Join(r.names, ", "))
	}
	return path, nil
}

// Names returns the configured alias names in deterministic order.
// Suitable for embedding in tool descriptions so agents know which values
// are valid.
func (r *Resolver) Names() []string { return r.names }

// NameForPath reverse-resolves a filesystem path to the alias name that
// maps to it, returning "" when no alias matches. Used by output
// projections that surface qBittorrent's raw save_path but also want to
// echo back the deploy-time alias name when one applies.
//
// Returns the alphabetically-first match when two aliases share a path
// (a misconfiguration the operator would normally not create).
func (r *Resolver) NameForPath(path string) string {
	if path == "" {
		return ""
	}
	for _, name := range r.names {
		if r.aliases[name] == path {
			return name
		}
	}
	return ""
}

// DescriptionHint formats the configured aliases for inclusion in an MCP
// tool description.
func (r *Resolver) DescriptionHint() string {
	if len(r.names) == 0 {
		return "No destinations are configured on this deployment; leave empty to use qBittorrent's account default."
	}
	return "Valid destinations: " + strings.Join(r.names, ", ") + ". Leave empty to use qBittorrent's account default."
}
