package tidy

import (
	_ "embed"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

//go:embed rules.toml
var DefaultRules string

type rule struct {
	Extensions []string
	Folder     string
}

type Rules struct {
	QuietFor   string `toml:"quiet_for"`
	AgeDays    int    `toml:"age_days"`
	LargeBytes int64  `toml:"large_bytes"`
	Rules      []rule

	quiet time.Duration
	byExt map[string]string
}

func LoadRules(filename string) (Rules, error) {
	var r Rules
	if _, err := toml.Decode(DefaultRules, &r); err != nil {
		return r, err
	}
	if filename != "" {
		custom := r
		custom.Rules = nil
		md, err := toml.DecodeFile(filename, &custom)
		if err == nil {
			if unknown := md.Undecoded(); len(unknown) > 0 {
				return r, fmt.Errorf("%s: unknown setting %s", filename, unknown[0])
			}
			if !md.IsDefined("rules") {
				custom.Rules = r.Rules
			}
			r = custom
		} else if !errors.Is(err, fs.ErrNotExist) {
			return r, err
		}
	}
	quiet, err := time.ParseDuration(r.QuietFor)
	if err != nil || quiet <= 0 {
		return r, fmt.Errorf("quiet_for must be a positive duration (e.g. 5m)")
	}
	r.quiet = quiet
	if r.AgeDays < 0 || r.AgeDays > 36500 || r.LargeBytes < 0 {
		return r, fmt.Errorf("age_days must be 0–36500 and large_bytes cannot be negative")
	}
	r.byExt = make(map[string]string)
	for _, rule := range r.Rules {
		if !validPath(rule.Folder) || len(rule.Extensions) == 0 {
			return r, fmt.Errorf("a rule needs extensions and a visible folder relative to Downloads: %q", rule.Folder)
		}
		for _, ext := range rule.Extensions {
			ext = strings.TrimPrefix(strings.ToLower(ext), ".")
			if ext == "" || strings.ContainsAny(ext, "./\\ \t") {
				return r, fmt.Errorf("invalid extension %q", ext)
			}
			if _, exists := r.byExt[ext]; exists {
				return r, fmt.Errorf("duplicate extension %q", ext)
			}
			r.byExt[ext] = rule.Folder
		}
	}
	return r, nil
}

func validPath(p string) bool {
	if p == "." || !fs.ValidPath(p) || strings.ContainsRune(p, 0) {
		return false
	}
	for _, part := range strings.Split(p, "/") {
		if part[0] == '.' {
			return false
		}
	}
	return true
}

func ignored(name string) bool {
	if !validPath(name) {
		return true
	}
	switch strings.ToLower(filepath.Ext(name)) {
	case ".crdownload", ".part", ".download", ".partial", ".tmp":
		return true
	}
	return false
}

func (r Rules) destination(name string, modified time.Time, now time.Time) string {
	if r.AgeDays > 0 && !modified.After(now.AddDate(0, 0, -r.AgeDays)) {
		return path.Join("Old", modified.In(now.Location()).Format("2006-01"))
	}
	return r.byExt[strings.TrimPrefix(strings.ToLower(filepath.Ext(name)), ".")]
}
