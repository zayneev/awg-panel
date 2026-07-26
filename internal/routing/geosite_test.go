package routing

import (
	"os"
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
)

func TestGeoSiteParsingAndMatching(t *testing.T) {
	domain := protowire.AppendTag(nil, 1, protowire.VarintType)
	domain = protowire.AppendVarint(domain, uint64(geoDomain))
	domain = protowire.AppendTag(domain, 2, protowire.BytesType)
	domain = protowire.AppendString(domain, "example.com")
	full := protowire.AppendTag(nil, 1, protowire.VarintType)
	full = protowire.AppendVarint(full, uint64(geoFull))
	full = protowire.AppendTag(full, 2, protowire.BytesType)
	full = protowire.AppendString(full, "only.example.net")
	site := protowire.AppendTag(nil, 1, protowire.BytesType)
	site = protowire.AppendString(site, "TEST")
	site = protowire.AppendTag(site, 2, protowire.BytesType)
	site = protowire.AppendBytes(site, domain)
	site = protowire.AppendTag(site, 2, protowire.BytesType)
	site = protowire.AppendBytes(site, full)
	list := protowire.AppendTag(nil, 1, protowire.BytesType)
	list = protowire.AppendBytes(list, site)
	path := t.TempDir() + "/geosite.dat"
	if err := os.WriteFile(path, list, 0600); err != nil {
		t.Fatal(err)
	}
	matcher, err := LoadGeoSite(path, []string{"test"})
	if err != nil {
		t.Fatal(err)
	}
	if !matcher.Match("test", "www.example.com") || !matcher.Match("test", "only.example.net") || matcher.Match("test", "www.only.example.net") {
		t.Fatal("geosite matching mismatch")
	}
	if _, err := LoadGeoSite(path, []string{"missing"}); err == nil {
		t.Fatal("missing category must fail")
	}
}
