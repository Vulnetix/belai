package main

import (
	"context"
	"os"

	"github.com/vulnetix/belai/internal/avatar"
	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/httpclient"
	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/run"
)

// rcDrawAvatar is the drawer `belai rc` answers an avatar request with: this
// host's main model, behind the security classifier that checks the website's
// text first. The model and the classifier are resolved for each request, so a
// settings change or a new login is picked up without restarting the daemon,
// and a request never holds a model or a key longer than it runs. Every failure
// is a reason for the website (avatar.Reason*); none carries provider text.
func rcDrawAvatar(wd string) func(context.Context, avatar.Request) ([]byte, string) {
	return func(ctx context.Context, r avatar.Request) ([]byte, string) {
		repo := repoRoot(wd)
		settings, pol, err := repoPolicy(repo)
		if err != nil {
			return nil, avatar.ReasonNoModel
		}
		resolver, err := newResolver(repo)
		if err != nil {
			return nil, avatar.ReasonNoModel
		}
		wantProvider, wantModel := workerModel("", "", settings, config.LoadState)
		cfg, err := run.ResolveWithSource(wantModel, wantProvider, os.Getenv, resolver)
		if err != nil {
			return nil, avatar.ReasonNoModel
		}
		if cfg, err = withClassifier(cfg, settings, resolver); err != nil {
			return nil, avatar.ReasonNoModel
		}
		if err := run.PreloadClassifier(run.ResolveSecurityClassifier(settings.Classifier)); err != nil {
			return nil, avatar.ReasonUnchecked
		}
		client := httpclient.Default()
		cache, _ := rolemanager.LoadCache(rolemanager.DefaultCachePath())
		pipe := run.NewPipeline(cfg, client, cache)
		d := avatar.Drawer{
			Classifier: run.NewRoleClassifier(cfg, client, nil),
			Admit: func(ctx context.Context, text string) error {
				_, err := pipe.Admit(ctx, text, "web avatar", pol)
				return err
			},
		}
		return d.Draw(ctx, r)
	}
}
