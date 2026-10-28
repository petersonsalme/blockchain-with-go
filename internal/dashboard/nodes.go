// Package dashboard is the read-only network dashboard behind `bwg dashboard` (ADR-0023).
// It imports only the standard library: it reaches nodes over HTTP like any outside client.
package dashboard

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// Node is one node the dashboard may read: a name used in page URLs and the base URL of its API.
type Node struct {
	Name string
	Base *url.URL
}

// ParseNodes parses NODES, "name=http://host:port[,name=...]". Names are [a-z0-9-]+ and unique;
// a base URL is http or https with a host and no userinfo, path, query or fragment. Order is kept.
// An error names the bad entry.
func ParseNodes(s string) ([]Node, error) {
	if strings.TrimSpace(s) == "" {
		return nil, errors.New("NODES is empty: want name=http://host:port[,name=...]")
	}

	var nodes []Node

	seen := map[string]bool{}

	for entry := range strings.SplitSeq(s, ",") {
		entry = strings.TrimSpace(entry)

		node, err := parseNode(entry)
		if err != nil {
			return nil, fmt.Errorf("NODES entry %q: %w", entry, err)
		}

		if seen[node.Name] {
			return nil, fmt.Errorf("NODES entry %q: duplicate node name %q", entry, node.Name)
		}

		seen[node.Name] = true
		nodes = append(nodes, node)
	}

	return nodes, nil
}

func parseNode(entry string) (Node, error) {
	name, raw, ok := strings.Cut(entry, "=")
	if !ok {
		return Node{}, errors.New("want name=http://host:port")
	}

	if name == "" || strings.Trim(name, "abcdefghijklmnopqrstuvwxyz0123456789-") != "" {
		return Node{}, errors.New("the name must be non-empty and use only [a-z0-9-]")
	}

	// url.Parse drops an empty query or fragment, so look at the text too.
	if strings.ContainsAny(raw, "?#") {
		return Node{}, errors.New("the URL must have no query or fragment")
	}

	base, err := url.Parse(raw)
	if err != nil {
		return Node{}, fmt.Errorf("invalid URL: %w", err)
	}

	if base.Scheme != "http" && base.Scheme != "https" {
		return Node{}, errors.New("the URL scheme must be http or https")
	}

	if base.Host == "" {
		return Node{}, errors.New("the URL has no host")
	}

	if base.User != nil {
		return Node{}, errors.New("the URL must have no userinfo")
	}

	if base.Path != "" && base.Path != "/" {
		return Node{}, errors.New("the URL must have no path")
	}

	base.Path = ""
	base.RawPath = ""

	return Node{Name: name, Base: base}, nil
}
