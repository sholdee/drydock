package cli

import (
	"fmt"

	"github.com/sholdee/drydock/internal/profile"
	"github.com/spf13/cobra"
)

type profileFlags struct {
	mode   string
	outDir string
}

func defaultProfileFlags() profileFlags {
	return profileFlags{outDir: profile.DefaultOutDir}
}

func bindProfileFlags(cmd *cobra.Command, flags *profileFlags) {
	cmd.PersistentFlags().StringVar(&flags.mode, "profile", flags.mode, "write a Go runtime profile: cpu, mem, block, mutex, or trace")
	cmd.PersistentFlags().StringVar(&flags.outDir, "profile-out", flags.outDir, "directory for profile artifacts")
}

func installProfileWrapper(cmd *cobra.Command, flags *profileFlags) {
	if cmd.RunE != nil {
		runE := cmd.RunE
		cmd.RunE = func(cmd *cobra.Command, args []string) error {
			return runWithProfile(cmd, flags, func() error {
				return runE(cmd, args)
			})
		}
	}
	for _, child := range cmd.Commands() {
		installProfileWrapper(child, flags)
	}
}

func runWithProfile(cmd *cobra.Command, flags *profileFlags, run func() error) error {
	if flags == nil || flags.mode == "" {
		return run()
	}
	session, err := profile.Start(profile.Options{
		Mode:        flags.mode,
		OutDir:      flags.outDir,
		CommandPath: cmd.CommandPath(),
		Stderr:      cmd.ErrOrStderr(),
	})
	if err != nil {
		return err
	}
	runErr := run()
	if stopErr := session.Stop(); stopErr != nil {
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "warning profile: %v\n", stopErr)
		if runErr != nil {
			return runErr
		}
		return stopErr
	}
	return runErr
}
