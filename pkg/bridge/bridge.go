package bridge

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/amarnathcjd/gogram/telegram"

	"gpix/pkg/gpmc"
)

type Bot struct {
	tg      *telegram.Client
	gp      *gpmc.Client
	owner   int64
	xfer    *Transfer
	log     *slog.Logger
	ctx     context.Context
	tempDir string
}

func New(cfg Config, gp *gpmc.Client, log *slog.Logger) (*Bot, error) {
	if cfg.BotToken == "" || cfg.APIID == 0 || cfg.APIHash == "" {
		return nil, errors.New("bridge: incomplete config (need BotToken, APIID, APIHash)")
	}
	if log == nil {
		log = slog.Default()
	}
	tgCfg := telegram.ClientConfig{
		AppID:    cfg.APIID,
		AppHash:  cfg.APIHash,
		Session:  cfg.SessionFile,
		LogLevel: telegram.LogInfo,
		FloodHandler: func(err error) bool {
			log.Warn("FLOOD_WAIT", "err", err)
			return true
		},
	}
	if cfg.Proxy != nil {
		tgCfg.Proxy = cfg.Proxy
		log.Info("telegram proxy configured",
			"type", cfg.Proxy.Type(),
			"host", cfg.Proxy.GetHost(),
			"port", cfg.Proxy.GetPort(),
		)
	}
	tg, err := telegram.NewClient(tgCfg)
	if err != nil {
		return nil, fmt.Errorf("bridge: telegram.NewClient: %w", err)
	}
	if err := tg.LoginBot(cfg.BotToken); err != nil {
		return nil, fmt.Errorf("bridge: LoginBot: %w", err)
	}

	b := &Bot{
		tg:      tg,
		gp:      gp,
		owner:   cfg.OwnerID,
		xfer:    NewTransfer(cfg.TempDir, cfg.MaxConcurrent),
		log:     log,
		tempDir: cfg.TempDir,
	}

	tg.On("message:/upload", b.wrap(b.handleUpload))
	tg.On("message:/get", b.wrap(b.handleGet))
	tg.On("message:/list", b.wrap(b.handleList))
	tg.On("message:/info", b.wrap(b.handleInfo))
	tg.On("message:/start", b.wrap(b.handleInfo))

	return b, nil
}

func (b *Bot) Start(ctx context.Context) error {
	b.ctx = ctx
	go func() {
		<-ctx.Done()
		_ = b.tg.Stop()
	}()
	b.tg.Idle()
	return nil
}

func (b *Bot) Stop() error { return b.tg.Stop() }

func (b *Bot) wrap(fn func(*telegram.NewMessage) error) func(*telegram.NewMessage) error {
	return func(m *telegram.NewMessage) (rerr error) {
		if m.SenderID() != b.owner {
			b.log.Debug("ignoring non-owner message", "sender", m.SenderID(), "owner", b.owner)
			return nil
		}
		defer func() {
			if r := recover(); r != nil {
				b.log.Error("handler panic", "recover", r)
				_, _ = m.Reply(fmt.Sprintf("internal error: %v", r))
				rerr = fmt.Errorf("panic: %v", r)
			}
		}()
		if err := fn(m); err != nil {
			b.log.Error("handler error", "err", err)
			return err
		}
		return nil
	}
}
