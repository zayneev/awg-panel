package routing

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/zayneev/awg-panel/internal/model"
	"google.golang.org/protobuf/encoding/protowire"
)

type geoDomainKind int

const (
	geoSubstr geoDomainKind = iota
	geoRegex
	geoDomain
	geoFull
)

type geoPattern struct {
	kind  geoDomainKind
	value string
	re    *regexp.Regexp
}

type GeoSiteMatcher struct {
	categories map[string][]geoPattern
}

func LoadGeoSite(path string, wanted []string) (*GeoSiteMatcher, error) {
	matcher := &GeoSiteMatcher{categories: make(map[string][]geoPattern)}
	if len(wanted) == 0 {
		return matcher, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("прочитать geosite.dat: %w", err)
	}
	need := make(map[string]struct{}, len(wanted))
	for _, category := range wanted {
		need[strings.ToLower(category)] = struct{}{}
	}
	sites, err := parseGeoSiteList(b)
	if err != nil {
		return nil, fmt.Errorf("разобрать geosite.dat: %w", err)
	}
	for _, site := range sites {
		name := strings.ToLower(site.code)
		if _, ok := need[name]; !ok {
			continue
		}
		patterns := make([]geoPattern, 0, len(site.domains))
		for _, domain := range site.domains {
			value := strings.ToLower(strings.TrimSuffix(domain.value, "."))
			pattern := geoPattern{kind: domain.kind, value: value}
			if domain.kind == geoRegex {
				pattern.re, err = regexp.Compile("(?i)" + domain.value)
				if err != nil {
					return nil, fmt.Errorf("geosite %s содержит некорректное regexp: %w", name, err)
				}
			}
			patterns = append(patterns, pattern)
		}
		matcher.categories[name] = patterns
	}
	for name := range need {
		if _, ok := matcher.categories[name]; !ok {
			return nil, fmt.Errorf("категория geosite:%s отсутствует", name)
		}
	}
	return matcher, nil
}

func (m *GeoSiteMatcher) Match(category, domain string) bool {
	if m == nil {
		return false
	}
	domain = strings.ToLower(strings.TrimSuffix(domain, "."))
	for _, pattern := range m.categories[strings.ToLower(category)] {
		switch pattern.kind {
		case geoFull:
			if domain == pattern.value {
				return true
			}
		case geoDomain:
			if domain == pattern.value || strings.HasSuffix(domain, "."+pattern.value) {
				return true
			}
		case geoSubstr:
			if strings.Contains(domain, pattern.value) {
				return true
			}
		case geoRegex:
			if pattern.re != nil && pattern.re.MatchString(domain) {
				return true
			}
		}
	}
	return false
}

type rawGeoSite struct {
	code    string
	domains []rawGeoDomain
}

type rawGeoDomain struct {
	kind  geoDomainKind
	value string
}

func parseGeoSiteList(data []byte) ([]rawGeoSite, error) {
	var result []rawGeoSite
	for len(data) > 0 {
		number, wireType, n := protowire.ConsumeTag(data)
		if n < 0 {
			return nil, protowire.ParseError(n)
		}
		data = data[n:]
		if number == 1 && wireType == protowire.BytesType {
			message, size := protowire.ConsumeBytes(data)
			if size < 0 {
				return nil, protowire.ParseError(size)
			}
			site, err := parseGeoSite(message)
			if err != nil {
				return nil, err
			}
			result = append(result, site)
			data = data[size:]
			continue
		}
		size := protowire.ConsumeFieldValue(number, wireType, data)
		if size < 0 {
			return nil, protowire.ParseError(size)
		}
		data = data[size:]
	}
	return result, nil
}

func parseGeoSite(data []byte) (rawGeoSite, error) {
	var result rawGeoSite
	for len(data) > 0 {
		number, wireType, n := protowire.ConsumeTag(data)
		if n < 0 {
			return result, protowire.ParseError(n)
		}
		data = data[n:]
		if wireType == protowire.BytesType && (number == 1 || number == 2) {
			value, size := protowire.ConsumeBytes(data)
			if size < 0 {
				return result, protowire.ParseError(size)
			}
			if number == 1 {
				result.code = string(value)
			} else {
				domain, err := parseGeoDomain(value)
				if err != nil {
					return result, err
				}
				result.domains = append(result.domains, domain)
			}
			data = data[size:]
			continue
		}
		size := protowire.ConsumeFieldValue(number, wireType, data)
		if size < 0 {
			return result, protowire.ParseError(size)
		}
		data = data[size:]
	}
	return result, nil
}

func parseGeoDomain(data []byte) (rawGeoDomain, error) {
	var result rawGeoDomain
	for len(data) > 0 {
		number, wireType, n := protowire.ConsumeTag(data)
		if n < 0 {
			return result, protowire.ParseError(n)
		}
		data = data[n:]
		switch {
		case number == 1 && wireType == protowire.VarintType:
			value, size := protowire.ConsumeVarint(data)
			if size < 0 {
				return result, protowire.ParseError(size)
			}
			result.kind = geoDomainKind(value)
			data = data[size:]
		case number == 2 && wireType == protowire.BytesType:
			value, size := protowire.ConsumeBytes(data)
			if size < 0 {
				return result, protowire.ParseError(size)
			}
			result.value = string(value)
			data = data[size:]
		default:
			size := protowire.ConsumeFieldValue(number, wireType, data)
			if size < 0 {
				return result, protowire.ParseError(size)
			}
			data = data[size:]
		}
	}
	if result.kind < geoSubstr || result.kind > geoFull || result.value == "" {
		return result, errors.New("некорректная domain-запись")
	}
	return result, nil
}

func requestedGeoSites(value model.RoutingConfig) []string {
	var result []string
	for _, rule := range value.Rules {
		if rule.Enabled {
			result = append(result, rule.GeoSites...)
		}
	}
	return uniqueSorted(result)
}
