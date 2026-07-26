package validate

import (
	"errors"
	"net"
	"regexp"
	"strconv"
	"strings"
)

var clientName = regexp.MustCompile(`^[A-Za-z0-9_-]{1,63}$`)

var allowedExpiries = map[string]struct{}{
	"": {}, "1h": {}, "12h": {}, "1d": {}, "7d": {}, "30d": {}, "4w": {},
}

var fields = map[string]struct{}{
	"DNS": {}, "Endpoint": {}, "AllowedIPs": {}, "PersistentKeepalive": {},
}

func ClientName(v string) error {
	if !clientName.MatchString(v) {
		return errors.New("имя должно содержать 1–63 символа: латинские буквы, цифры, _ или -")
	}
	return nil
}

func Expiry(v string) error {
	if _, ok := allowedExpiries[v]; !ok {
		return errors.New("допустимые сроки: 1h, 12h, 1d, 7d, 30d или 4w")
	}
	return nil
}

func FieldValue(field, value string) error {
	if _, ok := fields[field]; !ok {
		return errors.New("изменение этого параметра не разрешено")
	}
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 4096 || strings.ContainsAny(value, "\r\n\x00") {
		return errors.New("некорректное значение")
	}
	switch field {
	case "PersistentKeepalive":
		n, err := strconv.Atoi(value)
		if err != nil || n < 0 || n > 65535 {
			return errors.New("PersistentKeepalive должен быть числом 0–65535")
		}
	case "Endpoint":
		if !validEndpoint(value) {
			return errors.New("Endpoint должен иметь вид host:port или [IPv6]:port")
		}
	case "DNS":
		for _, item := range strings.Split(value, ",") {
			item = strings.TrimSpace(item)
			if net.ParseIP(item) == nil && !validHostname(item) {
				return errors.New("DNS должен быть списком IP-адресов или доменных имён")
			}
		}
	case "AllowedIPs":
		for _, item := range strings.Split(value, ",") {
			item = strings.TrimSpace(item)
			if _, _, err := net.ParseCIDR(item); err != nil {
				return errors.New("AllowedIPs должен быть списком CIDR")
			}
		}
	}
	return nil
}

func validEndpoint(v string) bool {
	host, port, err := net.SplitHostPort(v)
	if err != nil || host == "" {
		return false
	}
	n, err := strconv.Atoi(port)
	return err == nil && n >= 1 && n <= 65535 && (net.ParseIP(host) != nil || validHostname(host))
}

func validHostname(v string) bool {
	if len(v) == 0 || len(v) > 253 {
		return false
	}
	for _, label := range strings.Split(strings.TrimSuffix(v, "."), ".") {
		if len(label) == 0 || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return false
		}
		for _, r := range label {
			if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '-' {
				return false
			}
		}
	}
	return true
}
