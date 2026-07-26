package validate

import "testing"

func TestClientName(t *testing.T) {
	for _, name := range []string{"phone", "my_phone-2", "A1"} {
		if err := ClientName(name); err != nil {
			t.Fatalf("valid name %q rejected: %v", name, err)
		}
	}
	for _, name := range []string{"", "../root", "name space", "кириллица", string(make([]byte, 64))} {
		if err := ClientName(name); err == nil {
			t.Fatalf("invalid name %q accepted", name)
		}
	}
}

func TestExpiryAllowlist(t *testing.T) {
	for _, value := range []string{"", "1h", "12h", "1d", "7d", "30d", "4w"} {
		if err := Expiry(value); err != nil {
			t.Fatalf("valid expiry %q rejected: %v", value, err)
		}
	}
	for _, value := range []string{"0h", "2h", "365d", "1m", "$(id)"} {
		if err := Expiry(value); err == nil {
			t.Fatalf("invalid expiry %q accepted", value)
		}
	}
}

func TestFieldValue(t *testing.T) {
	tests := []struct{ field, value string }{
		{"DNS", "1.1.1.1, dns.example"},
		{"Endpoint", "vpn.example:443"},
		{"Endpoint", "[2001:db8::1]:443"},
		{"AllowedIPs", "0.0.0.0/0, ::/0"},
		{"PersistentKeepalive", "25"},
	}
	for _, tt := range tests {
		if err := FieldValue(tt.field, tt.value); err != nil {
			t.Errorf("%s=%q rejected: %v", tt.field, tt.value, err)
		}
	}
	for _, tt := range []struct{ field, value string }{{"PrivateKey", "secret"}, {"AllowedIPs", "all"}, {"Endpoint", "bad"}, {"DNS", "x\nInjected=1"}} {
		if err := FieldValue(tt.field, tt.value); err == nil {
			t.Errorf("invalid %s=%q accepted", tt.field, tt.value)
		}
	}
}
