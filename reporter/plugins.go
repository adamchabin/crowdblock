package main

import (
	"fmt"
	"net/netip"
	"regexp"
	"sort"
	"strings"
)

// The name is sent to the server as the report's source; same rule as the
// server's sourcePattern.
var pluginName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

// Plugin is a source of attacks: a log file and how to recognise an attack in
// one of its lines. Each plugin lives in its own file and registers itself in
// init(); the user picks the active ones with -plugins.
type Plugin struct {
	Name        string
	Description string
	DefaultLog  string // overridable with -<name>-log

	// Attempts within -window before an address is reported; 0 means
	// -threshold. 1 for sources that already decided it's an attack.
	Threshold int

	// Parse returns the attacking address and a short detail for the log
	// ("jail sshd"), or ok=false for lines that are not an attack.
	Parse func(line string) (ip netip.Addr, detail string, ok bool)
}

var registry = map[string]*Plugin{}

func register(p *Plugin) {
	if !pluginName.MatchString(p.Name) {
		panic("invalid plugin name " + p.Name)
	}
	if _, dup := registry[p.Name]; dup {
		panic("duplicate plugin " + p.Name)
	}
	registry[p.Name] = p
}

// pluginNames returns the registered plugin names, sorted.
func pluginNames() []string {
	names := make([]string, 0, len(registry))
	for name := range registry {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// selectPlugins parses the -plugins value ("auth,fail2ban").
func selectPlugins(list string) ([]*Plugin, error) {
	var selected []*Plugin
	seen := map[string]bool{}

	for _, name := range strings.Split(list, ",") {
		name = strings.TrimSpace(name)
		if name == "" || seen[name] {
			continue
		}
		p, ok := registry[name]
		if !ok {
			return nil, fmt.Errorf("unknown plugin %q (available: %s)", name, strings.Join(pluginNames(), ", "))
		}
		seen[name] = true
		selected = append(selected, p)
	}

	if len(selected) == 0 {
		return nil, fmt.Errorf("no plugins selected (available: %s)", strings.Join(pluginNames(), ", "))
	}
	return selected, nil
}
