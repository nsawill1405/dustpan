package main

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"

	"github.com/nsawill1405/dustpan/internal/tidy"
)

const agentLabel = "io.github.nsawill1405.dustpan"

func plistPath(home string) string {
	return filepath.Join(home, "Library", "LaunchAgents", agentLabel+".plist")
}

func guiDomain() string {
	return "gui/" + strconv.Itoa(os.Getuid())
}

func launch(args ...string) error {
	out, err := exec.Command("/bin/launchctl", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("launchctl %s: %w: %s", args[0], err, bytes.TrimSpace(out))
	}
	return nil
}

func agentStatus() string {
	if launch("print", guiDomain()+"/"+agentLabel) == nil {
		return "loaded"
	}
	return "not loaded in this login session"
}

func agentPlist(opts options, binary string) []byte {
	b := bytes.NewBufferString(xml.Header + "<!DOCTYPE plist PUBLIC \"-//Apple//DTD PLIST 1.0//EN\" \"http://www.apple.com/DTDs/PropertyList-1.0.dtd\">\n<plist version=\"1.0\"><dict>\n")
	put := func(tag, value string) {
		fmt.Fprintf(b, "<%s>", tag)
		xml.EscapeText(b, []byte(value))
		fmt.Fprintf(b, "</%s>\n", tag)
	}
	put("key", "Label")
	put("string", agentLabel)
	put("key", "ProgramArguments")
	b.WriteString("<array>\n")
	for _, arg := range []string{binary, "run", "--downloads", opts.engine.Downloads, "--config", opts.config, "--state", opts.engine.StateDir} {
		put("string", arg)
	}
	b.WriteString("</array>\n<key>RunAtLoad</key><true/>\n<key>KeepAlive</key><true/>\n")
	put("key", "StandardOutPath")
	put("string", filepath.Join(opts.engine.StateDir, "watch.log"))
	put("key", "StandardErrorPath")
	put("string", filepath.Join(opts.engine.StateDir, "watch-error.log"))
	b.WriteString("</dict></plist>\n")
	return b.Bytes()
}

func install(opts options, out io.Writer) error {
	if _, err := tidy.LoadRules(opts.config); err != nil {
		return err
	}
	if err := opts.engine.Status(io.Discard); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(opts.config), 0700); err != nil {
		return err
	}
	if _, err := os.Lstat(opts.config); errors.Is(err, fs.ErrNotExist) {
		if err := tidy.WriteAtomic(opts.config, []byte(tidy.DefaultRules), 0600); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	binDir := filepath.Join(opts.home, "Library", "Application Support", "Dustpan", "bin")
	plist := plistPath(opts.home)
	for _, dir := range []string{binDir, opts.engine.StateDir, filepath.Dir(plist)} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return err
		}
	}
	src, err := os.Executable()
	if err != nil {
		return err
	}
	binary := filepath.Join(binDir, "dustpan")
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	// Do not replace an already-installed executable with identical bytes.
	if existing, readErr := os.ReadFile(binary); readErr != nil || !bytes.Equal(existing, data) {
		if err := tidy.WriteAtomic(binary, data, 0700); err != nil {
			return err
		}
	}
	if err := tidy.WriteAtomic(plist, agentPlist(opts, binary), 0600); err != nil {
		return err
	}
	if launch("print", guiDomain()+"/"+agentLabel) == nil {
		if err := launch("bootout", guiDomain()+"/"+agentLabel); err != nil {
			return err
		}
	}
	if err := launch("bootstrap", guiDomain(), plist); err != nil {
		return fmt.Errorf("%w (run from a Terminal window in your macOS desktop session; uninstall removes the saved agent)", err)
	}
	fmt.Fprintf(out, "Login agent loaded and set to start at login. Logs: %s\n", opts.engine.StateDir)
	return nil
}

func uninstall(home string, out io.Writer) error {
	if launch("print", guiDomain()+"/"+agentLabel) == nil {
		if err := launch("bootout", guiDomain()+"/"+agentLabel); err != nil {
			return err
		}
	}
	if err := os.Remove(plistPath(home)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	fmt.Fprintln(out, "Login agent removed. Rules, journal, and Downloads were kept.")
	return nil
}
