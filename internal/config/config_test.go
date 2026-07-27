package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultConfigOmitsLegacyRequiredMinor(t *testing.T) {
	b, err := json.Marshal(Default())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "requiredManageMinor") {
		t.Fatalf("new config contains legacy compatibility policy: %s", b)
	}
}

func TestLoadAcceptsLegacyRequiredMinor(t *testing.T) {
	cfg := Default()
	b, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var values map[string]any
	if err := json.Unmarshal(b, &values); err != nil {
		t.Fatal(err)
	}
	values["requiredManageMinor"] = "5.20"
	b, err = json.Marshal(values)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.RequiredManageMinor != "5.20" {
		t.Fatalf("legacy value was not retained: %+v", loaded)
	}
}
