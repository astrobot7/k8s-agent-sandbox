// Package cli holds what the binaries' cobra commands share: flags that fall
// back to environment variables, so the same binary is configured by flags on
// a laptop and by env in a Deployment.
package cli

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// EnvName is the environment variable that backs a flag: --dedup-window reads
// DEDUP_WINDOW.
func EnvName(flag string) string {
	return strings.ToUpper(strings.ReplaceAll(flag, "-", "_"))
}

// BindEnv makes every flag of cmd that was not set on the command line take
// its value from its environment variable, when that is set. Call it from
// PreRunE, after flags are parsed. It also names the variable in each flag's
// help text.
func BindEnv(fs *pflag.FlagSet, lookup func(string) (string, bool)) error {
	var err error
	fs.VisitAll(func(f *pflag.Flag) {
		if err != nil || f.Changed {
			return
		}
		v, ok := lookup(EnvName(f.Name))
		if !ok || v == "" {
			return
		}
		if setErr := fs.Set(f.Name, v); setErr != nil {
			err = fmt.Errorf("%s: %w", EnvName(f.Name), setErr)
		}
	})
	return err
}

// errLogged marks a runtime error the command has already logged.
var errLogged = errors.New("logged")

// Command wraps a root command for one binary: flags fall back to env, usage
// is not printed for runtime errors, and runtime errors are logged as JSON once.
func Command(use, short string, run func(cmd *cobra.Command, log *slog.Logger) error) *cobra.Command {
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	cmd := &cobra.Command{
		Use:          use,
		Short:        short,
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		// Errors are logged as structured JSON below, not printed by cobra.
		SilenceErrors: true,
		PreRunE: func(cmd *cobra.Command, _ []string) error {
			return BindEnv(cmd.Flags(), os.LookupEnv)
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := run(cmd, log); err != nil {
				log.Error(use+" failed", "err", err)
				return fmt.Errorf("%w: %w", errLogged, err)
			}
			return nil
		},
	}
	cmd.SetFlagErrorFunc(func(c *cobra.Command, err error) error {
		return fmt.Errorf("%w\n%s", err, c.UsageString())
	})
	return cmd
}

// AnnotateEnv appends "(env NAME)" to every flag's usage. Call it after all
// flags are defined.
func AnnotateEnv(fs *pflag.FlagSet) {
	fs.VisitAll(func(f *pflag.Flag) {
		f.Usage += " (env " + EnvName(f.Name) + ")"
	})
}

// Main runs a root command. Runtime errors (already logged) exit 1; bad flags
// or env values exit 2 with the message on stderr.
func Main(cmd *cobra.Command) {
	AnnotateEnv(cmd.Flags())
	err := cmd.Execute()
	switch {
	case err == nil:
	case errors.Is(err, errLogged):
		os.Exit(1)
	default:
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(2)
	}
}
