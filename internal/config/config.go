package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const DefaultPath = "/etc/awgpanel/config.json"

type Config struct {
	ManageScript        string `json:"manageScript"`
	CommonScript        string `json:"commonScript"`
	AWGDir              string `json:"awgDir"`
	ServerConfig        string `json:"serverConfig"`
	RequiredManageMinor string `json:"requiredManageMinor"`
	RoutingDir          string `json:"routingDir"`
	RoutingConfig       string `json:"routingConfig"`
	WarpSecrets         string `json:"warpSecrets"`
	XrayBinary          string `json:"xrayBinary"`
	XrayAssets          string `json:"xrayAssets"`
	XrayConfig          string `json:"xrayConfig"`
	GeoSiteData         string `json:"geoSiteData"`
	RoutingInterface    string `json:"routingInterface"`
	DNSListen           string `json:"dnsListen"`
	DNSPort             int    `json:"dnsPort"`
	TProxyPort          int    `json:"tproxyPort"`
	HealthPort          int    `json:"healthPort"`
	FWMark              uint32 `json:"fwMark"`
	RouteTable          int    `json:"routeTable"`
}

func Default() Config {
	return Config{
		ManageScript:        "/root/awg/manage_amneziawg.sh",
		CommonScript:        "/root/awg/awg_common.sh",
		AWGDir:              "/root/awg",
		ServerConfig:        "/etc/amnezia/amneziawg/awg0.conf",
		RequiredManageMinor: "5.20",
		RoutingDir:          "/etc/awgpanel/routing",
		RoutingConfig:       "/etc/awgpanel/routing/routing.json",
		WarpSecrets:         "/etc/awgpanel/routing/warp.json",
		XrayBinary:          "/usr/local/lib/awgpanel/xray",
		XrayAssets:          "/usr/local/share/awgpanel/xray",
		XrayConfig:          "/etc/awgpanel/routing/xray.json",
		GeoSiteData:         "/usr/local/share/awgpanel/xray/geosite.dat",
		RoutingInterface:    "awg0",
		DNSListen:           "0.0.0.0",
		DNSPort:             1053,
		TProxyPort:          17890,
		HealthPort:          17891,
		FWMark:              0xA61,
		RouteTable:          1061,
	}
}

func Load(path string) (Config, error) {
	cfg := Default()
	if path == "" {
		path = DefaultPath
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) && path == DefaultPath {
			return cfg, cfg.Validate()
		}
		return Config{}, fmt.Errorf("read config %s: %w", path, err)
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse config %s: %w", path, err)
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c Config) Validate() error {
	for name, path := range map[string]string{
		"manageScript": c.ManageScript, "commonScript": c.CommonScript,
		"awgDir": c.AWGDir, "serverConfig": c.ServerConfig,
		"routingDir": c.RoutingDir, "routingConfig": c.RoutingConfig,
		"warpSecrets": c.WarpSecrets, "xrayBinary": c.XrayBinary,
		"xrayAssets": c.XrayAssets, "xrayConfig": c.XrayConfig,
		"geoSiteData": c.GeoSiteData,
	} {
		if path == "" {
			return fmt.Errorf("%s is required", name)
		}
		if !filepath.IsAbs(path) {
			return fmt.Errorf("%s must be an absolute path", name)
		}
	}
	if c.RequiredManageMinor == "" {
		return errors.New("requiredManageMinor is required")
	}
	if c.RoutingInterface == "" {
		return errors.New("routingInterface is required")
	}
	if c.DNSListen == "" {
		return errors.New("dnsListen is required")
	}
	for name, port := range map[string]int{"dnsPort": c.DNSPort, "tproxyPort": c.TProxyPort, "healthPort": c.HealthPort} {
		if port < 1 || port > 65535 {
			return fmt.Errorf("%s must be 1..65535", name)
		}
	}
	if c.FWMark == 0 || c.RouteTable < 1 {
		return errors.New("fwMark and routeTable must be positive")
	}
	return nil
}
