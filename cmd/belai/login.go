package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/vulnetix/belai/internal/credentials"
	"github.com/vulnetix/belai/internal/kiroauth"
	"github.com/vulnetix/belai/internal/run"
)

const loginUsage = `usage: belai login kiro [flags]

Sign in to Kiro with an AWS Builder ID (the default) or an IAM Identity
Center start URL. Belai shows a code to confirm in the browser, then stores
the sign-in as the kiro provider's login credential.

  -start-url URL    IAM Identity Center start URL (default: AWS Builder ID)
  -region R         SSO region of the start URL, any AWS region
                    (default us-east-1)
  -api-region R     Kiro API region (default: the profile's region, else
                    the SSO region)
  -profile-arn ARN  CodeWhisperer profile ARN (default: looked up after
                    sign-in; asked for when the account has several)
  -backend B        keychain or user-file (default: keychain when available)
  -import           import the sign-in Kiro already made (~/.aws/sso/cache)
`

// newResolver builds the credential resolver and installs the Kiro rotation
// hook, so a refresh token the service rotates is written back where the
// user's login is stored.
func newResolver(workdir string) (*credentials.Resolver, error) {
	r, err := credentials.NewResolver(workdir)
	if err != nil {
		return nil, err
	}
	if forceFirewall {
		on := true
		r.SetFirewallEnabled(&on)
	}
	run.SetKiroRotationHook(func(oldLogin, newLogin string) {
		_ = r.Replace("kiro", "login", oldLogin, newLogin)
	})
	return r, nil
}

// runLoginCLI implements `belai login …` and returns the exit code.
func runLoginCLI(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer, isTTY bool) int {
	if len(args) == 0 || args[0] != "kiro" {
		fmt.Fprint(stderr, loginUsage)
		return 2
	}
	fs := flag.NewFlagSet("login kiro", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, loginUsage) }
	startURL := fs.String("start-url", "", "IAM Identity Center start URL")
	region := fs.String("region", "", "SSO region")
	apiRegion := fs.String("api-region", "", "Kiro API region")
	profileARN := fs.String("profile-arn", "", "CodeWhisperer profile ARN")
	backend := fs.String("backend", "", "keychain or user-file")
	importCache := fs.Bool("import", false, "import Kiro's own sign-in")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	workdir, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(stderr, "belai:", err)
		return 1
	}
	resolver, err := credentials.NewResolver(workdir)
	if err != nil {
		fmt.Fprintln(stderr, "belai:", err)
		return 1
	}
	dest := resolver.PreferredBackend()
	switch strings.TrimSpace(*backend) {
	case "":
	case "keychain":
		dest = credentials.SourceKeychain
	case "user-file":
		dest = credentials.SourceUserFile
	default:
		fmt.Fprintf(stderr, "belai: unknown backend %q (want keychain or user-file)\n", *backend)
		return 2
	}

	var login kiroauth.Login
	if *importCache {
		home, err := os.UserHomeDir()
		if err == nil {
			login, err = kiroauth.ImportKiroCache(home)
		}
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				err = errors.New("Kiro has not signed in on this machine")
			}
			fmt.Fprintln(stderr, "belai:", err)
			return 1
		}
	} else {
		d := kiroauth.DeviceLogin{StartURL: *startURL, Region: *region}
		g, err := d.Start(ctx)
		if err != nil {
			fmt.Fprintln(stderr, "belai:", err)
			return 1
		}
		fmt.Fprintf(stdout, "Open %s\nand confirm the code %s\nWaiting for the sign-in…\n", g.BrowseURL(), g.UserCode)
		login, err = d.Poll(ctx, g)
		if err != nil {
			fmt.Fprintln(stderr, "belai:", err)
			return 1
		}
	}
	login, ok := chooseKiroProfile(ctx, login, *profileARN, stdin, stdout, stderr, isTTY)
	if !ok {
		return 1
	}
	if *apiRegion != "" {
		login.APIRegion = *apiRegion // an explicit region outranks the profile's
	}
	if _, err := kiroauth.ParseLogin(login.Encode()); err != nil {
		fmt.Fprintln(stderr, "belai:", err)
		return 1
	}
	if err := resolver.Store("kiro", "login", login.Encode(), dest); err != nil {
		fmt.Fprintln(stderr, "belai: could not store the Kiro sign-in:", err)
		return 1
	}
	fmt.Fprintf(stdout, "Signed in to Kiro; the login is stored in the %s. Use -provider kiro.\n", dest)
	return 0
}

// chooseKiroProfile settles the login's CodeWhisperer profile: an explicit
// ARN, the account's only profile, or the user's pick when there are
// several. A lookup failure keeps the login without a profile and warns; a
// headless run with several profiles fails and names them.
func chooseKiroProfile(ctx context.Context, login kiroauth.Login, explicit string, stdin io.Reader, stdout, stderr io.Writer, isTTY bool) (kiroauth.Login, bool) {
	login, profiles, err := kiroauth.ResolveProfile(ctx, nil, kiroauth.Shared, login, explicit, nil)
	if err != nil {
		if explicit != "" {
			fmt.Fprintln(stderr, "belai:", err)
			return login, false
		}
		fmt.Fprintln(stderr, "belai: warning: could not look up Kiro profiles:", err)
		return login, true
	}
	if len(profiles) == 0 {
		return login, true
	}
	fmt.Fprintln(stdout, "This account has several Kiro profiles:")
	for i, p := range profiles {
		fmt.Fprintf(stdout, "  %d) %s  %s\n", i+1, p.Name, p.ARN)
	}
	if !isTTY {
		fmt.Fprintln(stderr, "belai: no terminal to choose on; pass -profile-arn with one of the ARNs above")
		return login, false
	}
	fmt.Fprintf(stdout, "Profile [1-%d]: ", len(profiles))
	line, _ := bufio.NewReader(stdin).ReadString('\n')
	n, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil || n < 1 || n > len(profiles) {
		fmt.Fprintln(stderr, "belai: no profile chosen")
		return login, false
	}
	return login.WithProfile(profiles[n-1]), true
}
