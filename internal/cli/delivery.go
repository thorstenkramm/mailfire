package cli

import (
	"errors"

	"github.com/thorstenkramm/mailfire/internal/config"
	"github.com/thorstenkramm/mailfire/internal/logging"
	"github.com/thorstenkramm/mailfire/internal/mailgun"
)

// preparedDelivery owns local delivery resources until Close. Both send and
// run use it; run retains it across job execution, including skipped reports.
// It never holds a deduplication lock while a job is running.
type preparedDelivery struct {
	cfg      config.Config
	opts     SendOptions
	provider *preparedProvider
	logger   *logging.Logger
}

func prepareDelivery(cfg *config.Config, opts *SendOptions, rc *runContext) (*preparedDelivery, error) {
	p := &preparedDelivery{cfg: *cfg, opts: *opts}
	var err error
	p.provider, err = prepareProvider(&p.cfg, &p.opts, rc)
	if err != nil {
		return nil, err
	}
	p.logger, err = openLogger(&p.cfg)
	if err != nil {
		return nil, err
	}
	return p, nil
}

// CheckLocal validates resources that normally fail at send time, before run
// starts a job. Send keeps its established classification of Gmail auth errors
// and dedup failures in the delivery pipeline.
func (p *preparedDelivery) CheckLocal(rc *runContext) error {
	if p.provider.checkLocal != nil {
		if err := p.provider.checkLocal(); err != nil {
			return err
		}
	}
	if p.opts.DedupKey != "" && !p.opts.Test {
		msg := &mailgun.Message{To: p.opts.To, Cc: p.opts.Cc, Bcc: p.opts.Bcc}
		return checkNotificationStorage(&p.cfg, &p.opts, msg, rc)
	}
	return nil
}

func (p *preparedDelivery) Close() error {
	if p.logger == nil {
		return nil
	}
	err := p.logger.Close()
	p.logger = nil
	return err
}

// A failed preparation releases everything already acquired.
func prepareRunDelivery(cfg *config.Config, opts *SendOptions, rc *runContext) (*preparedDelivery, error) {
	p, err := prepareDelivery(cfg, opts, rc)
	if err != nil {
		return nil, err
	}
	if err := p.CheckLocal(rc); err != nil {
		return nil, errors.Join(err, p.Close())
	}
	return p, nil
}
