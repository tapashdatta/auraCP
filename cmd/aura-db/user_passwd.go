package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/auracp/auracp/pkg/dbadmin/standalone"
)

func runUserPasswd(g globalFlags, args []string) error {
	fs := newFlagSet("user-passwd", os.Stderr)
	username := fs.String("username", "", "username (required)")
	self := fs.Bool("self", false, "change own password (requires current password)")
	passwordStdin := fs.Bool("password-stdin", false, "new password from stdin")
	allowPwned := fs.Bool("allow-pwned", false, "skip HIBP check")
	reason := fs.String("reason", "", "reason required when --allow-pwned is set")
	help := fs.Bool("help", false, "show help")
	fs.Usage = func() { fmt.Fprint(fs.Output(), helpUserPasswd) }
	if err := fs.Parse(args); err != nil {
		return userErr(err.Error())
	}
	if *help {
		fmt.Fprint(os.Stdout, helpUserPasswd)
		return nil
	}
	if *username == "" {
		return userErr("--username required")
	}
	if *allowPwned && strings.TrimSpace(*reason) == "" {
		return userErr("--allow-pwned requires --reason")
	}

	cfg, err := standalone.LoadConfig(g.configPath)
	if err != nil {
		return err
	}
	ctx := context.Background()
	app, err := standalone.Bootstrap(ctx, cfg)
	if err != nil {
		return err
	}
	defer app.Close()

	user, err := app.Store.GetUserByUsername(ctx, *username)
	if err != nil {
		return err
	}

	if *self {
		current, err := readPassword("Current password: ")
		if err != nil {
			return err
		}
		ok, _, verr := standalone.VerifyPassword(current, user.PasswordHash, cfg.PasswordPolicy())
		if verr != nil {
			return verr
		}
		if !ok {
			return userErr("current password incorrect")
		}
	}

	var newPassword string
	if *passwordStdin {
		newPassword, err = readLineFromStdin()
	} else {
		newPassword, err = readPassword("New password: ")
		if err == nil {
			var confirm string
			confirm, err = readPassword("Confirm password: ")
			if err == nil && newPassword != confirm {
				return userErr("passwords do not match")
			}
		}
	}
	if err != nil {
		return err
	}

	if len(newPassword) < cfg.Auth.PasswordMinLength {
		return userErr(fmt.Sprintf("password must be at least %d characters", cfg.Auth.PasswordMinLength))
	}

	if cfg.Auth.HIBPCheck && !*allowPwned {
		if cerr := standalone.DefaultHIBPClient().Check(ctx, newPassword); cerr != nil {
			if errors.Is(cerr, standalone.ErrPasswordPwned) {
				return userErr("password appears in haveibeenpwned corpus; rerun with --allow-pwned --reason \"...\" to override")
			}
			fmt.Fprintf(os.Stderr, "warning: HIBP check failed (%v); proceeding\n", cerr)
		}
	}

	if err := app.Store.SetPassword(ctx, user.ID, newPassword, cfg.PasswordPolicy()); err != nil {
		return err
	}
	if err := app.Auth.RevokeAllSessionsForUser(ctx, user.ID); err != nil {
		fmt.Fprintf(os.Stderr, "warning: revoke sessions failed: %v\n", err)
	}
	fmt.Fprintf(os.Stderr, "password updated for user %s\n", user.Username)
	return nil
}
