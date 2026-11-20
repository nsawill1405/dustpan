package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/nsawill1405/dustpan/internal/tidy"
)

const usage = `Usage: dustpan <command> [options]

  plan             Preview moves without writing anything
  run [--once]     Watch Downloads, or sort once and exit
  undo [--all-today]  Restore the last batch, or today's batches
  status           Show undo history and login-agent status
  rules            Print the default TOML rules
  install          Start Dustpan at login (macOS)
  uninstall        Stop Dustpan and remove the login agent (macOS)

Options (after the command):
  --downloads DIR  Folder to tidy (default ~/Downloads)
  --config FILE    Rules file (default ~/.config/dustpan/rules.toml)
  --state DIR      Journal and logs (default ~/Library/Application Support/Dustpan)
`

type options struct {
	home   string
	config string
	engine tidy.Engine
}

func command(ctx context.Context, args []string, out, errOut io.Writer) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Fprint(out, usage)
		return nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	cmd := args[0]
	switch cmd {
	case "plan", "run", "undo", "status", "rules", "install", "uninstall":
	default:
		return fmt.Errorf("unknown command %q (see dustpan --help)", cmd)
	}
	opts := options{home: home}
	flags := flag.NewFlagSet(cmd, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&opts.engine.Downloads, "downloads", filepath.Join(home, "Downloads"), "folder to tidy")
	flags.StringVar(&opts.config, "config", filepath.Join(home, ".config", "dustpan", "rules.toml"), "rules file")
	flags.StringVar(&opts.engine.StateDir, "state", filepath.Join(home, "Library", "Application Support", "Dustpan"), "journal and log folder")
	var once, allToday bool
	if cmd == "run" {
		flags.BoolVar(&once, "once", false, "sort once and exit")
	}
	if cmd == "undo" {
		flags.BoolVar(&allToday, "all-today", false, "restore all batches started today")
	}
	if err := flags.Parse(args[1:]); errors.Is(err, flag.ErrHelp) {
		fmt.Fprint(out, usage)
		return nil
	} else if err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected argument %q (see dustpan --help)", flags.Arg(0))
	}
	for _, p := range []*string{&opts.config, &opts.engine.Downloads, &opts.engine.StateDir} {
		if *p == "" {
			return fmt.Errorf("paths cannot be empty")
		}
		abs, err := filepath.Abs(*p)
		if err != nil {
			return err
		}
		*p = abs
	}
	switch cmd {
	case "rules":
		fmt.Fprint(out, tidy.DefaultRules)
		return nil
	case "plan", "run":
		rules, err := tidy.LoadRules(opts.config)
		if err != nil {
			return err
		}
		if cmd == "plan" || once {
			return opts.engine.Sort(rules, time.Now(), cmd == "plan", out)
		}
		return watch(ctx, opts, out, errOut)
	case "undo":
		return opts.engine.Undo(time.Now(), allToday, out)
	case "status":
		fmt.Fprintf(out, "Login agent: %s\n", agentStatus())
		return opts.engine.Status(out)
	case "install":
		return install(opts, out)
	case "uninstall":
		return uninstall(opts.home, out)
	}
	panic("unreachable")
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := command(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "dustpan: %v\n", err)
		os.Exit(1)
	}
}
