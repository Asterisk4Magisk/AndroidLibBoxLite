package pluginpool

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func absolute(path string) string {
	result, _ := filepath.Abs(path)
	return result
}

func TestParse(t *testing.T) {
	for _, content := range []string{"", " \n", `{"workingDir":"/w","plugins":[]}`} {
		options, err := Parse(content)
		if err != nil || options != nil {
			t.Fatalf("%q: got %v, %v", content, options, err)
		}
	}
	work, command := absolute("work"), absolute("plugin")
	invalid := []string{
		"{",
		`{"workingDir":"relative","plugins":[{"command":["` + command + `"]}]}`,
		`{"workingDir":"` + work + `","protectPath":"relative","plugins":[{"command":["` + command + `"]}]}`,
		`{"workingDir":"` + work + `","plugins":[{"command":[]}]}`,
		`{"workingDir":"` + work + `","plugins":[{"command":["plugin"]}]}`,
		`{"workingDir":"` + work + `","plugins":[{"command":["` + command + `"],"files":[{"name":"../x"}]}]}`,
		`{"workingDir":"` + work + `","plugins":[{"command":["` + command + `"],"files":[{"name":"a"}]},` +
			`{"command":["` + command + `"],"files":[{"name":"a"}]}]}`,
	}
	for _, content := range invalid {
		// Windows test paths carry backslashes that JSON must escape.
		content = strings.ReplaceAll(content, `\`, `\\`)
		if _, err := Parse(content); err == nil {
			t.Fatalf("accepted %s", content)
		}
	}
	content := strings.ReplaceAll(`{"workingDir":"`+work+`","protectPath":"`+absolute("protect")+`","verifyPeer":true,`+
		`"plugins":[{"command":["`+command+`","run"],"environment":{"CONFIG":"${file:a.json}"},`+
		`"files":[{"name":"a.json","content":"{}"}]}]}`, `\`, `\\`)
	options, err := Parse(content)
	if err != nil {
		t.Fatal(err)
	}
	if !options.VerifyPeer || options.Plugins[0].Name != "plugin-0" || options.Plugins[0].Files[0].Content != "{}" {
		t.Fatalf("envelope not decoded: %+v", options)
	}
}

func TestExpand(t *testing.T) {
	work := absolute("work")
	path := filepath.Join(work, "a.json")
	files := map[string]bool{"a.json": true}
	for input, expected := range map[string]string{
		"plain":                        "plain",
		"${file:a.json}":               path,
		"-c=${file:a.json}x":           "-c=" + path + "x",
		"${file:a.json}${file:a.json}": path + path,
	} {
		actual, err := expand(input, work, files)
		if err != nil || actual != expected {
			t.Fatalf("%q: got %q, %v", input, actual, err)
		}
	}
	for _, input := range []string{"${file:missing}", "${file:a.json"} {
		if _, err := expand(input, work, files); err == nil {
			t.Fatalf("%q accepted", input)
		}
	}
}

func TestReadEnvelope(t *testing.T) {
	empty, configured := t.TempDir(), t.TempDir()
	content := strings.ReplaceAll(`{"workingDir":"`+configured+`","plugins":[{"command":["`+absolute("plugin")+`"]}]}`, `\`, `\\`)
	if err := os.WriteFile(filepath.Join(configured, Envelope), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	options, err := ReadEnvelope([]string{filepath.Join(empty, "config.json")})
	if err != nil || options != nil {
		t.Fatalf("missing envelope: %v, %v", options, err)
	}
	options, err = ReadEnvelope([]string{filepath.Join(empty, "a.json"), filepath.Join(configured, "config.json")})
	if err != nil || options == nil || options.WorkingDir != configured {
		t.Fatalf("envelope beside the config not found: %v, %v", options, err)
	}
}
