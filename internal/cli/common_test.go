package cli

import "testing"

func TestParseRepoMaps(t *testing.T) {
	maps, err := parseRepoMaps([]string{
		"https://github.com/example/values.git=/tmp/values",
		"oci://ignored=still-accepted-as-url-string",
	})
	if err != nil {
		t.Fatalf("parseRepoMaps() error = %v", err)
	}
	if len(maps) != 2 {
		t.Fatalf("len(maps) = %d, want 2", len(maps))
	}
	if maps[0].URL != "https://github.com/example/values.git" || maps[0].Path != "/tmp/values" {
		t.Fatalf("maps[0] = %#v", maps[0])
	}
	if maps[1].URL != "oci://ignored" || maps[1].Path != "still-accepted-as-url-string" {
		t.Fatalf("maps[1] = %#v", maps[1])
	}
}

func TestParseRepoMapsRejectsInvalidMapping(t *testing.T) {
	_, err := parseRepoMaps([]string{"https://github.com/example/repo"})
	if err == nil {
		t.Fatal("parseRepoMaps() error = nil, want invalid mapping error")
	}
}
