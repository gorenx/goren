package mcpclient

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Config selects one external server. Command arguments and URLs must not contain secrets.
// Stdio inherits the process environment; credential mutation remains outside this package.
type Config struct {
	ServerName           string   `json:"serverName"`
	Transport            string   `json:"transport"`
	Command              string   `json:"command,omitempty"`
	Args                 []string `json:"args,omitempty"`
	CWD                  string   `json:"cwd,omitempty"`
	URL                  string   `json:"url,omitempty"`
	SkipDiscover         bool     `json:"skipDiscover,omitempty"`
	RequestTimeoutMs     int      `json:"requestTimeoutMs,omitempty"`
	ElicitationTimeoutMs int      `json:"elicitationTimeoutMs,omitempty"`
	MaxInputRoundTrips   int      `json:"maxInputRoundTrips,omitempty"`
	MaxTools             int      `json:"maxTools,omitempty"`
	MaxPages             int      `json:"maxPages,omitempty"`
	ToolResultLimit      int      `json:"toolResultLimit,omitempty"`
}

// Equal compares complete configuration values. Call Validate first when
// omitted defaults should be treated as their effective values.
func (settings Config) Equal(other Config) bool {
	return settings.ServerName == other.ServerName &&
		settings.Transport == other.Transport &&
		settings.Command == other.Command &&
		slices.Equal(settings.Args, other.Args) &&
		settings.CWD == other.CWD &&
		settings.URL == other.URL &&
		settings.SkipDiscover == other.SkipDiscover &&
		settings.RequestTimeoutMs == other.RequestTimeoutMs &&
		settings.ElicitationTimeoutMs == other.ElicitationTimeoutMs &&
		settings.MaxInputRoundTrips == other.MaxInputRoundTrips &&
		settings.MaxTools == other.MaxTools &&
		settings.MaxPages == other.MaxPages &&
		settings.ToolResultLimit == other.ToolResultLimit
}

// DecodeConfig rejects unknown fields and trailing input before any effect.
func DecodeConfig(data []byte) (Config, error) {
	var settings Config
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&settings); err != nil {
		return Config{}, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return Config{}, errors.New("mcpclient: trailing configuration")
	}
	return settings.Validate()
}

// Validate applies explicit defaults and detaches mutable configuration.
func (settings Config) Validate() (Config, error) {
	if settings.ServerName == "" || strings.TrimSpace(settings.ServerName) != settings.ServerName {
		return Config{}, errors.New("mcpclient: serverName is required")
	}
	switch settings.Transport {
	case "stdio":
		if strings.TrimSpace(settings.Command) == "" ||
			settings.URL != "" ||
			(settings.CWD != "" && !filepath.IsAbs(settings.CWD)) {
			return Config{}, errors.New("mcpclient: stdio requires command, absolute optional cwd and no URL")
		}
	case "streamable-http":
		endpoint, err := url.Parse(settings.URL)
		if err != nil ||
			endpoint.Hostname() == "" ||
			endpoint.User != nil ||
			endpoint.Fragment != "" ||
			(endpoint.Scheme != "http" && endpoint.Scheme != "https") ||
			settings.Command != "" ||
			len(settings.Args) != 0 ||
			settings.CWD != "" {
			return Config{}, errors.New("mcpclient: invalid HTTP endpoint or mixed transport configuration")
		}
	default:
		return Config{}, errors.New("mcpclient: transport must be stdio or streamable-http")
	}
	if settings.RequestTimeoutMs == 0 {
		settings.RequestTimeoutMs = 30000
	}
	if settings.ElicitationTimeoutMs == 0 {
		settings.ElicitationTimeoutMs = 600000
	}
	if settings.MaxInputRoundTrips == 0 {
		settings.MaxInputRoundTrips = 16
	}
	if settings.MaxTools == 0 {
		settings.MaxTools = 1000
	}
	if settings.MaxPages == 0 {
		settings.MaxPages = 100
	}
	if settings.ToolResultLimit == 0 {
		settings.ToolResultLimit = 8 << 20
	}
	if settings.RequestTimeoutMs <= 0 ||
		settings.ElicitationTimeoutMs < settings.RequestTimeoutMs ||
		settings.ElicitationTimeoutMs > int((1<<63-1)/time.Millisecond) ||
		settings.MaxInputRoundTrips <= 0 ||
		settings.MaxTools <= 0 ||
		settings.MaxPages <= 0 ||
		settings.ToolResultLimit <= 0 {
		return Config{}, errors.New("mcpclient: invalid timeout or resource limit")
	}
	settings.Args = slices.Clone(settings.Args)
	return settings, nil
}
