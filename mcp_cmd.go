package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
)

func cmdMCP(args []string, out io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: code-switch mcp <list|add|remove|test>")
	}

	switch args[0] {
	case "list":
		return cmdMCPList(args[1:], out)
	case "add":
		return cmdMCPAdd(args[1:], out)
	case "remove":
		return cmdMCPRemove(args[1:], out)
	case "test":
		return cmdMCPTest(args[1:], out)
	default:
		return fmt.Errorf("usage: code-switch mcp <list|add|remove|test>")
	}
}

func cmdMCPList(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("mcp list", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("usage: code-switch mcp list")
	}
	cfg, _, err := loadAppConfig()
	if err != nil {
		return err
	}
	mgr := newMCPManager(cfg)
	fmt.Fprintln(out, "NAME\tTRANSPORT\tCOMMAND/URL\tDISABLED")
	for _, server := range mgr.list() {
		location := server.Command
		if server.Transport == "sse" {
			location = server.URL
		}
		fmt.Fprintf(out, "%s\t%s\t%s\t%t\n", server.Name, server.Transport, location, server.Disabled)
	}
	return nil
}

func cmdMCPAdd(args []string, out io.Writer) error {
	name, flagArgs, passthrough := splitMCPAddArgs(args)
	fs := flag.NewFlagSet("mcp add", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	transport := fs.String("transport", "", "transport: stdio or sse")
	command := fs.String("command", "", "stdio command")
	urlFlag := fs.String("url", "", "sse url")
	disabled := fs.Bool("disabled", false, "disable server")
	if err := fs.Parse(flagArgs); err != nil {
		return err
	}
	if name == "" || strings.TrimSpace(*transport) == "" {
		return fmt.Errorf("usage: code-switch mcp add <name> --transport stdio --command <cmd> [-- args...] | code-switch mcp add <name> --transport sse --url <url>")
	}
	server := MCPServerConfig{Name: name, Transport: strings.TrimSpace(*transport), Command: strings.TrimSpace(*command), URL: strings.TrimSpace(*urlFlag), Disabled: *disabled, Args: passthrough}
	cfg, path, err := loadAppConfig()
	if err != nil {
		return err
	}
	mgr := newMCPManager(cfg)
	if err := mgr.add(server); err != nil {
		return err
	}
	if err := writeJSONAtomic(path, cfg); err != nil {
		return err
	}
	fmt.Fprintf(out, "added mcp server %s\n", canonicalMCPServerName(name))
	return nil
}

func cmdMCPRemove(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("mcp remove", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: code-switch mcp remove <name>")
	}
	cfg, path, err := loadAppConfig()
	if err != nil {
		return err
	}
	mgr := newMCPManager(cfg)
	if err := mgr.remove(fs.Arg(0)); err != nil {
		return err
	}
	if err := writeJSONAtomic(path, cfg); err != nil {
		return err
	}
	fmt.Fprintf(out, "removed mcp server %s\n", canonicalMCPServerName(fs.Arg(0)))
	return nil
}

func cmdMCPTest(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("mcp test", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: code-switch mcp test <name>")
	}
	cfg, _, err := loadAppConfig()
	if err != nil {
		return err
	}
	mgr := newMCPManager(cfg)
	if _, ok := mgr.get(fs.Arg(0)); !ok {
		return fmt.Errorf("mcp server %q not found", canonicalMCPServerName(fs.Arg(0)))
	}
	server := mgr.cfg.MCPServers[canonicalMCPServerName(fs.Arg(0))]
	if err := testMCPServer(context.Background(), server); err != nil {
		return err
	}
	fmt.Fprintf(out, "mcp server %s is ok\n", canonicalMCPServerName(fs.Arg(0)))
	return nil
}

func splitMCPAddArgs(args []string) (string, []string, []string) {
	name := ""
	flagArgs := make([]string, 0, len(args))
	passthrough := []string{}
	seenDashDash := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if seenDashDash {
			passthrough = append(passthrough, arg)
			continue
		}
		if arg == "--" {
			seenDashDash = true
			continue
		}
		if name == "" && !strings.HasPrefix(arg, "-") {
			name = arg
			continue
		}
		flagArgs = append(flagArgs, arg)
		if mcpFlagNeedsValue(arg) && i+1 < len(args) {
			i++
			flagArgs = append(flagArgs, args[i])
		}
	}
	return name, flagArgs, passthrough
}

func mcpFlagNeedsValue(arg string) bool {
	if strings.Contains(arg, "=") {
		return false
	}
	switch arg {
	case "-transport", "--transport", "-command", "--command", "-url", "--url":
		return true
	default:
		return false
	}
}
