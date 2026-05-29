package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/auracp/auracp/pkg/dbadmin/standalone"
)

func runUserCreate(g globalFlags, args []string) error {
	fs := newFlagSet("user-create", os.Stderr)
	username := fs.String("username", "", "username (required)")
	role := fs.String("role", "", "initial role (viewer|analyst|writer|dba|owner)")
	allowPwned := fs.Bool("allow-pwned", false, "skip HIBP check")
	reason := fs.String("reason", "", "reason required when --allow-pwned is set")
	enrollTOTP := fs.Bool("enroll-totp", false, "generate TOTP secret + print provisioning URI")
	passwordStdin := fs.Bool("password-stdin", false, "read password from stdin")
	printRecovery := fs.Bool("print-recovery-codes", true, "print recovery codes to stdout when TOTP is enrolled")
	help := fs.Bool("help", false, "show help")
	fs.Usage = func() { fmt.Fprint(fs.Output(), helpUserCreate) }
	if err := fs.Parse(args); err != nil {
		return userErr(err.Error())
	}
	if *help {
		fmt.Fprint(os.Stdout, helpUserCreate)
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

	var password string
	if *passwordStdin {
		password, err = readLineFromStdin()
	} else {
		password, err = readPassword("New password: ")
		if err == nil {
			var confirm string
			confirm, err = readPassword("Confirm password: ")
			if err == nil && password != confirm {
				return userErr("passwords do not match")
			}
		}
	}
	if err != nil {
		return err
	}
	if len(password) < cfg.Auth.PasswordMinLength {
		return userErr(fmt.Sprintf("password must be at least %d characters", cfg.Auth.PasswordMinLength))
	}

	if cfg.Auth.HIBPCheck && !*allowPwned {
		if err := standalone.DefaultHIBPClient().Check(ctx, password); err != nil {
			if errors.Is(err, standalone.ErrPasswordPwned) {
				return userErr("password appears in haveibeenpwned corpus; rerun with --allow-pwned --reason \"...\" to override")
			}
			fmt.Fprintf(os.Stderr, "warning: HIBP check failed (%v); proceeding\n", err)
		}
	}

	policy := cfg.PasswordPolicy()
	user, err := app.Store.CreateUser(ctx, *username, password, policy)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "created user id=%s username=%s\n", user.ID, user.Username)

	if *role != "" {
		fmt.Fprintf(os.Stderr, "note: --role accepted but per-connection grants are managed via the API in PR #9\n")
	}

	if *enrollTOTP {
		secret, err := standalone.GenerateTOTPSecret()
		if err != nil {
			return err
		}
		if err := app.Store.EnrollTOTP(ctx, app.KEK, user.ID, secret); err != nil {
			return err
		}
		uri := standalone.TOTPProvisioningURI(secret, "aura-db", user.Username)
		fmt.Fprintf(os.Stdout, "totp_uri=%s\n", uri)

		codes, err := standalone.GenerateRecoveryCodes()
		if err != nil {
			return err
		}
		if err := app.Store.StoreRecoveryCodes(ctx, user.ID, codes, policy); err != nil {
			return err
		}
		if *printRecovery {
			fmt.Fprintln(os.Stdout, "recovery_codes:")
			for _, c := range codes {
				fmt.Fprintln(os.Stdout, "  "+c)
			}
		}
	}
	return nil
}
