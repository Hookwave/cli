package cli

import (
	"strings"
	"testing"
)

func TestSupportedDestinationTypes(t *testing.T) {
	want := []string{"http", "n8n", "make", "slack", "discord", "mock"}
	if strings.Join(supportedDestinationTypes, ",") != strings.Join(want, ",") {
		t.Fatalf("destination types drifted from API: %v", supportedDestinationTypes)
	}
}

func TestContainsHelper(t *testing.T) {
	if !contains([]string{"a", "b"}, "a") {
		t.Fatal("contains should match")
	}
	if contains([]string{"a", "b"}, "c") {
		t.Fatal("contains should not match")
	}
}
