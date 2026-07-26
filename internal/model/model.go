package model

import "time"

type Compatibility struct {
	OK            bool   `json:"ok"`
	ManageVersion string `json:"manageVersion,omitempty"`
	CommonVersion string `json:"commonVersion,omitempty"`
	RequiredMinor string `json:"requiredMinor"`
	Message       string `json:"message,omitempty"`
}

type ServerStatus struct {
	Healthy       bool          `json:"healthy"`
	ServiceActive bool          `json:"serviceActive"`
	UptimeSeconds uint64        `json:"uptimeSeconds,omitempty"`
	Compatibility Compatibility `json:"compatibility"`
	TotalClients  int           `json:"totalClients"`
	ActiveClients int           `json:"activeClients"`
	RecentClients int           `json:"recentClients"`
	RXBytes       uint64        `json:"rxBytes"`
	TXBytes       uint64        `json:"txBytes"`
	CheckedAt     time.Time     `json:"checkedAt"`
}

type ArtifactAvailability struct {
	Config   bool `json:"config"`
	QR       bool `json:"qr"`
	VPNURI   bool `json:"vpnUri"`
	VPNURIQR bool `json:"vpnUriQr"`
}

type Client struct {
	Name          string               `json:"name"`
	IP            string               `json:"ip"`
	ClientIPv6    string               `json:"clientIpv6,omitempty"`
	StatusCode    string               `json:"statusCode"`
	RXBytes       uint64               `json:"rxBytes"`
	TXBytes       uint64               `json:"txBytes"`
	LastHandshake *time.Time           `json:"lastHandshake,omitempty"`
	ExpiresAt     *time.Time           `json:"expiresAt,omitempty"`
	ExpiryState   string               `json:"expiryState"`
	Artifacts     ArtifactAvailability `json:"artifacts"`
}

type CreateClientRequest struct {
	Name    string `json:"name"`
	Expires string `json:"expires,omitempty"`
	PSK     bool   `json:"psk"`
}

type ModifyClientRequest struct {
	Field string `json:"field"`
	Value string `json:"value"`
}

// RoutingRule describes a domain-to-outbound rule. An empty Clients list is
// valid only for global rules; client-scoped rules always retain an explicit
// scope so deleting the last referenced client can never widen a rule.
type RoutingRule struct {
	ID       string   `json:"id"`
	Enabled  bool     `json:"enabled"`
	Scope    string   `json:"scope"`
	Clients  []string `json:"clients,omitempty"`
	Domains  []string `json:"domains,omitempty"`
	GeoSites []string `json:"geosites,omitempty"`
	Outbound string   `json:"outbound"`
	Priority int      `json:"priority"`
}

type RoutingConfig struct {
	Version         int           `json:"version"`
	Enabled         bool          `json:"enabled"`
	DefaultOutbound string        `json:"defaultOutbound"`
	WarpFailure     string        `json:"warpFailure"`
	DNSUpstreams    []string      `json:"dnsUpstreams"`
	Rules           []RoutingRule `json:"rules"`
}

type WarpStatus struct {
	Configured bool       `json:"configured"`
	Source     string     `json:"source,omitempty"`
	Endpoint   string     `json:"endpoint,omitempty"`
	Addresses  []string   `json:"addresses,omitempty"`
	Healthy    bool       `json:"healthy"`
	EgressIP   string     `json:"egressIp,omitempty"`
	Colo       string     `json:"colo,omitempty"`
	CheckedAt  *time.Time `json:"checkedAt,omitempty"`
	Message    string     `json:"message,omitempty"`
}

type RoutingCheck struct {
	OK       bool     `json:"ok"`
	Warnings []string `json:"warnings,omitempty"`
	Errors   []string `json:"errors,omitempty"`
}

type RoutingStatus struct {
	Installed      bool         `json:"installed"`
	Enabled        bool         `json:"enabled"`
	State          string       `json:"state"`
	DNSActive      bool         `json:"dnsActive"`
	XrayActive     bool         `json:"xrayActive"`
	FirewallActive bool         `json:"firewallActive"`
	Rules          int          `json:"rules"`
	NeedsApply     bool         `json:"needsApply"`
	Warp           WarpStatus   `json:"warp"`
	Check          RoutingCheck `json:"check"`
	LastError      string       `json:"lastError,omitempty"`
	CheckedAt      time.Time    `json:"checkedAt"`
}
