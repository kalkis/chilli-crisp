// Package cli defines the cobra command tree. The root command opens the
// TUI; subcommands cover init, headless runs, imports, and curl export.
package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/kalkis/chilli-crisp/internal/curlgen"
	"github.com/kalkis/chilli-crisp/internal/httpclient"
	"github.com/kalkis/chilli-crisp/internal/importer/openapi"
	"github.com/kalkis/chilli-crisp/internal/importer/postman"
	"github.com/kalkis/chilli-crisp/internal/model"
	"github.com/kalkis/chilli-crisp/internal/runner"
	"github.com/kalkis/chilli-crisp/internal/store"
	"github.com/kalkis/chilli-crisp/internal/tui"
)

var workspaceFlag string

// Execute runs the CLI and exits non-zero on error or failed assertions.
// The command context is cancelled on SIGINT or SIGTERM, which is what lets a
// headless run stop cleanly at the next request instead of being killed
// mid-flight. The TUI is unaffected: Bubble Tea puts the terminal in raw mode,
// so ^C arrives there as a key press and never becomes a signal.
func Execute() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := newRoot().ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		stop() // the deferred call will not run past os.Exit
		os.Exit(1)
	}
}

func newRoot() *cobra.Command {
	root := &cobra.Command{
		Use:   "chilli-crisp",
		Short: "A fast, minimal API client for the terminal",
		Long: "chilli-crisp is a keyboard-driven HTTP client for testing backend APIs.\n" +
			"Run without arguments to open the TUI; use subcommands for headless runs,\n" +
			"imports, and curl export.",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			w, err := openWorkspace()
			if err != nil {
				return err
			}
			return tui.Run(w)
		},
	}
	root.PersistentFlags().StringVarP(&workspaceFlag, "workspace", "w", "", "workspace directory, or a directory containing one (default: $CRISP_WORKSPACE, a .crisp at or above the cwd, then the home workspace)")
	root.AddCommand(newInit(), newRun(), newImport(), newCurl(), newList(), newWhere())
	return root
}

// openWorkspace resolves the workspace every command acts on. The rules live
// in store.Resolve, the one place workspace lookup is decided; only `where`
// needs to know which of them fired.
func openWorkspace() (*store.Workspace, error) {
	w, _, err := store.Resolve(workspaceFlag)
	return w, err
}

func newInit() *cobra.Command {
	var home bool
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Create a workspace in the current directory, or the home workspace",
		Long: "Create a .crisp workspace in the current directory.\n\n" +
			"With --home, create the workspace under your configuration directory\n" +
			"instead. That one is found from anywhere, so collections can be kept\n" +
			"apart from the code they test — for testing an API you do not check out.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			w, err := initWorkspace(home)
			if err != nil {
				return err
			}
			abs, _ := filepath.Abs(w.Root)
			fmt.Printf("initialized workspace at %s\n", abs)
			return nil
		},
	}
	cmd.Flags().BoolVar(&home, "home", false, "create the home workspace, usable from any directory")
	return cmd
}

// initWorkspace creates the workspace init was asked for. --home names an
// exact path rather than a parent to put a .crisp in, which is why it goes
// through InitAt.
func initWorkspace(home bool) (*store.Workspace, error) {
	if !home {
		return store.Init(".")
	}
	path, err := store.HomeWorkspacePath()
	if err != nil {
		return nil, err
	}
	return store.InitAt(path)
}

// newWhere reports the paths a workspace resolves to. Request history lives
// in a per-user state directory rather than the workspace, so without this
// there is no way to find it from the binary itself.
func newWhere() *cobra.Command {
	return &cobra.Command{
		Use:   "where",
		Short: "Print where this workspace and its history are stored",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			w, src, err := store.Resolve(workspaceFlag)
			if err != nil {
				return err
			}
			p, err := w.Paths()
			if err != nil {
				return err
			}
			timeout, err := w.RequestTimeout()
			if err != nil {
				return err
			}
			mode, err := w.HistoryMode()
			if err != nil {
				return err
			}
			root := p.Root
			if abs, err := filepath.Abs(root); err == nil {
				root = abs
			}
			fmt.Printf("workspace  %s  (%s)\n", root, src)
			if p.ID == "" {
				// Reporting where the log would go must not be what decides
				// it, so a workspace that has never been sent from has no
				// directory to name yet.
				fmt.Printf("id         (none yet)\n")
			} else {
				fmt.Printf("id         %s\n", p.ID)
			}
			fmt.Printf("history    %s\n", historyLocation(p, mode))
			fmt.Printf("config     timeout %s, history %s\n", timeout, mode)
			return nil
		},
	}
}

// historyLocation describes where this workspace's log is, or why there is
// not one. A workspace that records nothing must not be told a path that will
// never exist, and one that is only half recording should say so where the
// path is read rather than only in the config line.
func historyLocation(p store.Paths, mode store.HistoryMode) string {
	switch {
	case mode == store.HistoryOff:
		return "(off; nothing is recorded)"
	case p.ID == "":
		return "(none yet, created on the first send)"
	case mode == store.HistoryMetadata:
		return tildePath(p.HistoryDir) + "  (response bodies are not recorded)"
	default:
		return tildePath(p.HistoryDir)
	}
}

// tildePath shortens a path under the home directory the way a shell writes
// it, so a state path stays readable and still pastes back into a command.
func tildePath(path string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	rel, err := filepath.Rel(home, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return path
	}
	return filepath.Join("~", rel)
}

func newRun() *cobra.Command {
	var opts runner.Options
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "run <collection>",
		Short: "Run a collection (or one request) headlessly; non-zero exit on failure",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			w, err := openWorkspace()
			if err != nil {
				return err
			}
			opts.Timeout = timeout
			ok, err := runner.Run(cmd.Context(), w, args[0], opts, os.Stdout)
			if err != nil {
				return err
			}
			if !ok {
				return errors.New("one or more requests failed")
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&opts.Env, "env", "e", "", "environment to use")
	cmd.Flags().StringVarP(&opts.Request, "request", "r", "", "run only the named request")
	cmd.Flags().StringVar(&opts.AssertStatus, "assert-status", "", "require status to match (e.g. 200 or 2xx)")
	cmd.Flags().BoolVarP(&opts.Verbose, "verbose", "v", false, "print response bodies")
	cmd.Flags().BoolVar(&opts.NoHistory, "no-history", false, "do not record this run in the request log")
	// Zero means unset, so the workspace config supplies the default. Cobra
	// prints no "(default ...)" for a zero duration, which keeps the help
	// text from contradicting config.yaml.
	cmd.Flags().DurationVar(&timeout, "timeout", 0, "per-request timeout (default: config.yaml timeout, else 30s)")
	return cmd
}

func newImport() *cobra.Command {
	var name string
	imp := &cobra.Command{
		Use:   "import",
		Short: "Import collections from other formats",
	}
	imp.PersistentFlags().StringVar(&name, "name", "", "override the imported collection name")

	runImport := func(parse func([]byte) (*model.Collection, error), file string) error {
		w, err := openWorkspace()
		if err != nil {
			return err
		}
		data, err := os.ReadFile(file) // #nosec G304 -- importing a user-specified spec file is the command's purpose
		if err != nil {
			return err
		}
		col, err := parse(data)
		if err != nil {
			return err
		}
		if name != "" {
			col.Name = name
		}
		if err := w.ImportCollection(col); err != nil {
			return err
		}
		fmt.Printf("imported %q: %d request(s) -> %s\n", col.Name, len(col.Requests), col.Path)
		return nil
	}

	imp.AddCommand(&cobra.Command{
		Use:   "postman <file.json>",
		Short: "Import a Postman Collection v2.x export",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runImport(postman.Import, args[0])
		},
	}, &cobra.Command{
		Use:   "openapi <spec.yaml|json>",
		Short: "Import an OpenAPI 3.x or Swagger 2.0 spec",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runImport(openapi.Import, args[0])
		},
	})
	return imp
}

func newCurl() *cobra.Command {
	var env string
	cmd := &cobra.Command{
		Use:   "curl <collection> <request>",
		Short: "Print a request as a curl command",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			w, err := openWorkspace()
			if err != nil {
				return err
			}
			col, err := w.Collection(args[0])
			if err != nil {
				return err
			}
			var envVars map[string]string
			if env != "" {
				e, err := w.Environment(env)
				if err != nil {
					return err
				}
				envVars = e.AllVars()
			}
			idxs := store.MatchRequests(col, args[1])
			switch {
			case len(idxs) == 0:
				return fmt.Errorf("request %q not found in collection %q", args[1], col.Name)
			case len(idxs) > 1:
				var names []string
				for _, i := range idxs {
					r := &col.Requests[i]
					names = append(names, fmt.Sprintf("  %s %s", r.Method, r.QualifiedName()))
				}
				return fmt.Errorf("%q matches %d requests; qualify with a folder or method prefix (e.g. %q):\n%s",
					args[1], len(idxs), col.Requests[idxs[0]].Method+" "+col.Requests[idxs[0]].QualifiedName(), strings.Join(names, "\n"))
			}
			req := &col.Requests[idxs[0]]
			resolved, err := httpclient.Resolve(req, col, httpclient.ResolverFor(req, col, envVars))
			if err != nil {
				return err
			}
			fmt.Println(curlgen.Command(resolved))
			return nil
		},
	}
	cmd.Flags().StringVarP(&env, "env", "e", "", "environment to use")
	return cmd
}

func newList() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List collections, requests, and environments in the workspace",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			w, err := openWorkspace()
			if err != nil {
				return err
			}
			cols, err := w.Collections()
			if err != nil {
				return err
			}
			envs, err := w.Environments()
			if err != nil {
				return err
			}
			for _, c := range cols {
				fmt.Printf("%s  (%s)\n", c.Name, strings.TrimSuffix(filepath.Base(c.Path), ".yaml"))
				lastFolder := ""
				for _, r := range c.Requests {
					if r.Folder != lastFolder {
						if r.Folder != "" {
							fmt.Printf("  %s/\n", r.Folder)
						}
						lastFolder = r.Folder
					}
					indent := "  "
					if r.Folder != "" {
						indent = "    "
					}
					fmt.Printf("%s%-7s %s\n", indent, r.Method, r.Name)
				}
			}
			if len(envs) > 0 {
				fmt.Println("environments:")
				for _, e := range envs {
					fmt.Printf("  %s\n", e.Name)
				}
			}
			return nil
		},
	}
}
