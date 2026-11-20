package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/nsawill1405/dustpan/internal/tidy"
)

func TestAgentArgumentsAreLiteral(t *testing.T) {
	file := filepath.Join(t.TempDir(), "launch.plist")
	bin := "/Users/me/Library/Application Support/Dustpan/bin/dustpan"
	opts := options{config: "/tmp/me & friends/rules.toml",
		engine: tidy.Engine{Downloads: "/tmp/Download <folder>", StateDir: "/tmp/a state folder"}}
	if err := os.WriteFile(file, agentPlist(opts, bin), 0600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("/usr/bin/plutil", "-extract", "ProgramArguments", "json", "-o", "-", file).CombinedOutput()
	if err != nil {
		t.Fatalf("plutil: %v: %s", err, out)
	}
	var actual []string
	want := []string{bin, "run", "--downloads", opts.engine.Downloads, "--config", opts.config, "--state", opts.engine.StateDir}
	if err := json.Unmarshal(out, &actual); err != nil || !reflect.DeepEqual(actual, want) {
		t.Fatalf("want %q, got %q (%v)", want, actual, err)
	}
}
