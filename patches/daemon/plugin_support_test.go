// Copyright (C) 2026 AsteriskBOX contributors
// SPDX-License-Identifier: GPL-3.0

package daemon

import "testing"

func TestParsePluginOptions(t *testing.T) {
	options, err := ParsePluginOptions("")
	if err != nil || options != nil {
		t.Fatalf("empty envelope: %v %v", options, err)
	}
	options, err = ParsePluginOptions("  \n")
	if err != nil || options != nil {
		t.Fatalf("blank envelope: %v %v", options, err)
	}
	_, err = ParsePluginOptions("{")
	if err == nil {
		t.Fatal("invalid JSON accepted")
	}
	_, err = ParsePluginOptions(`{"plugins":[{"command":["/x"]}]}`)
	if err == nil {
		t.Fatal("missing working directory accepted")
	}
	options, err = ParsePluginOptions(`{"workingDir":"/tmp/p","protectPath":"/tmp/p/protect_path","verifyPeer":true,` +
		`"plugins":[{"name":"mieru-plugin","command":["/x/libmieru.so","run"],` +
		`"environment":{"MIERU_CONFIG_JSON_FILE":"${file:mieru.json}"},` +
		`"files":[{"name":"mieru.json","content":"{}"}]}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if options.WorkingDir != "/tmp/p" || options.ProtectPath != "/tmp/p/protect_path" || !options.VerifyPeer {
		t.Fatalf("envelope not decoded: %+v", options)
	}
	plugin := options.Plugins[0]
	if plugin.Name != "mieru-plugin" || len(plugin.Command) != 2 || plugin.Files[0].Name != "mieru.json" {
		t.Fatalf("plugin not decoded: %+v", plugin)
	}
}

func TestExpandPluginFileTokens(t *testing.T) {
	files := map[string]string{"mieru.json": "/work/mieru.json"}
	for input, expected := range map[string]string{
		"${file:mieru.json}":       "/work/mieru.json",
		"-c=${file:mieru.json}":    "-c=/work/mieru.json",
		"${file:missing.json}":     "${file:missing.json}",
		"${file:mieru.json}-x":     "/work/mieru.json-x",
		"no-token":                 "no-token",
		"${file:mieru.json}${file:mieru.json}": "/work/mieru.json/work/mieru.json",
	} {
		if actual := expandPluginFileTokens(input, files); actual != expected {
			t.Fatalf("%q: got %q, want %q", input, actual, expected)
		}
	}
}

func TestValidatePluginFileName(t *testing.T) {
	for _, name := range []string{"", ".", "..", "../x", "a/b", `a\b`} {
		if err := validatePluginFileName(name); err == nil {
			t.Fatalf("%q accepted", name)
		}
	}
	for _, name := range []string{"mieru.json", "shadowquic_1080.yaml"} {
		if err := validatePluginFileName(name); err != nil {
			t.Fatalf("%q rejected: %v", name, err)
		}
	}
}
