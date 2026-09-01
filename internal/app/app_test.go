package app

import (
	"reflect"
	"testing"
)

func TestExtractConfigPathSupportsGlobalAndInlineForms(t *testing.T) {
	for _, test := range []struct {
		name     string
		args     []string
		wantArgs []string
		wantPath string
	}{
		{
			name:     "before command",
			args:     []string{"--config", "proxy.toml", "serve", "--port", "9000"},
			wantArgs: []string{"serve", "--port", "9000"},
			wantPath: "proxy.toml",
		},
		{
			name:     "after command",
			args:     []string{"serve", "--config=proxy.toml"},
			wantArgs: []string{"serve"},
			wantPath: "proxy.toml",
		},
		{
			name:     "short form",
			args:     []string{"-c", "proxy.toml", "auth", "status"},
			wantArgs: []string{"auth", "status"},
			wantPath: "proxy.toml",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			args, path, err := extractConfigPath(test.args)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(args, test.wantArgs) || path != test.wantPath {
				t.Fatalf("extractConfigPath() = %#v, %q; want %#v, %q", args, path, test.wantArgs, test.wantPath)
			}
		})
	}
}

func TestExtractConfigPathRejectsDuplicatesAndMissingValues(t *testing.T) {
	for _, args := range [][]string{
		{"--config", "one.toml", "--config", "two.toml", "serve"},
		{"--config"},
		{"--config="},
	} {
		if _, _, err := extractConfigPath(args); err == nil {
			t.Fatalf("extractConfigPath(%#v) accepted invalid arguments", args)
		}
	}
}
