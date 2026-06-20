package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"golang.org/x/crypto/bcrypt"

	"gpix/pkg/bridge"
	"gpix/pkg/gpmc"
	"gpix/pkg/s3api"
	"gpix/pkg/web"
	"gpix/pkg/webdav"
)

func main() {
	mode := flag.String("mode", "all", "all | bot | web | cli | hashpw")
	cli := flag.Bool("cli", false, "shortcut for -mode cli")
	hashpw := flag.Bool("hashpw", false, "shortcut for -mode hashpw")

	envFlag := flag.String("env", ".env", "path to .env file (skipped if missing)")
	authFlag := flag.String("auth", "", "GP auth_data (defaults to $GP_AUTH_DATA)")
	profileFlag := flag.String("profile", "", "pixel-xl | pixel-5 (default: from web config or pixel-xl)")
	logLevel := flag.String("log", "info", "debug | info | warn | error")
	cfgFlag := flag.String("config", "gpix-web.conf", "path to web config file")
	secretFlag := flag.String("secret", "", "path to secret.key (default: alongside config)")

	qualityFlag := flag.String("quality", "original", "[cli] original | saver | quota")
	conc := flag.Int("concurrency", 1, "[cli] parallel uploads")
	recursive := flag.Bool("recursive", false, "[cli] descend into directories")
	force := flag.Bool("force", false, "[cli] skip dedup")
	deleteAfter := flag.Bool("delete-after", false, "[cli] delete local file after successful upload")

	flag.Parse()

	switch {
	case *cli:
		*mode = "cli"
	case *hashpw:
		*mode = "hashpw"
	}

	if err := loadDotEnv(*envFlag); err != nil {
		fmt.Fprintln(os.Stderr, "warn: load env:", err)
	}

	log := newLogger(*logLevel)

	switch *mode {
	case "hashpw":
		runHashpw()
		return
	case "cli":
		runCLI(*authFlag, *profileFlag, *qualityFlag, *conc, *recursive, *force, *deleteAfter)
		return
	}

	auth := *authFlag
	if auth == "" {
		auth = os.Getenv("GP_AUTH_DATA")
	}
	if auth == "" {
		die("missing GP_AUTH_DATA (in .env, env var, or -auth flag)")
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	switch *mode {
	case "bot":
		runBot(ctx, log, auth, *profileFlag)
	case "web":
		runWeb(ctx, log, auth, *cfgFlag, *secretFlag, *profileFlag)
	case "all":
		runAll(ctx, log, auth, *cfgFlag, *secretFlag, *profileFlag)
	default:
		die("unknown mode: " + *mode + " (want all|bot|web|cli|hashpw)")
	}
}

func runAll(ctx context.Context, log *slog.Logger, auth, cfgPath, secretPath, profileFlag string) {
	var wg sync.WaitGroup
	errs := make(chan error, 2)

	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := startBot(ctx, log.With("service", "bot"), auth, profileFlag); err != nil {
			errs <- fmt.Errorf("bot: %w", err)
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := startWeb(ctx, log.With("service", "web"), auth, cfgPath, secretPath, profileFlag); err != nil {
			errs <- fmt.Errorf("web: %w", err)
		}
	}()

	wg.Wait()
	close(errs)
	var first error
	for e := range errs {
		log.Error("service failed", "err", e)
		if first == nil {
			first = e
		}
	}
	if first != nil {
		os.Exit(1)
	}
}

func runBot(ctx context.Context, log *slog.Logger, auth, profileFlag string) {
	if err := startBot(ctx, log, auth, profileFlag); err != nil {
		log.Error("bot stopped", "err", err)
		os.Exit(1)
	}
}

func albumStorePath(cfgPath string) string {
	dir := filepath.Dir(cfgPath)
	if dir == "" || dir == "." {
		if home, err := os.UserHomeDir(); err == nil {
			dir = home
		} else {
			dir = "."
		}
	}
	return filepath.Join(dir, "gpix-albums.json")
}

func cfgPathForBot() string {
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, "gpix-bot")
	}
	return "gpix-bot"
}

func runWeb(ctx context.Context, log *slog.Logger, auth, cfgPath, secretPath, profileFlag string) {
	if err := startWeb(ctx, log, auth, cfgPath, secretPath, profileFlag); err != nil {
		log.Error("web stopped", "err", err)
		os.Exit(1)
	}
}

func startBot(ctx context.Context, log *slog.Logger, auth, profileFlag string) error {
	cfg, err := bridge.LoadConfigFromEnv()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}

	profile, err := parseProfile(coalesce(profileFlag, "pixel-xl"))
	if err != nil {
		return err
	}

	gp, err := gpmc.New(auth, gpmc.WithDeviceProfile(profile), gpmc.WithAlbumStore(albumStorePath(cfgPathForBot())))
	if err != nil {
		return fmt.Errorf("gpmc.New: %w", err)
	}

	bot, err := bridge.New(cfg, gp, log)
	if err != nil {
		return fmt.Errorf("bridge.New: %w", err)
	}

	log.Info("gpix bot started",
		"owner", cfg.OwnerID,
		"temp_dir", cfg.TempDir,
		"max_concurrent", cfg.MaxConcurrent,
	)
	return bot.Start(ctx)
}

func startWeb(ctx context.Context, log *slog.Logger, auth, cfgPath, secretPath, profileFlag string) error {
	cfg, err := web.LoadConfig(cfgPath)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}

	if secretPath == "" {
		secretPath = filepath.Join(filepath.Dir(cfgPath), "secret.key")
	}
	secret, err := web.LoadOrCreateSecret(secretPath)
	if err != nil {
		return fmt.Errorf("secret: %w", err)
	}
	cfg.SecretKey = secret

	profileName := coalesce(profileFlag, cfg.DeviceProfile, "pixel-xl")
	profile, err := parseProfile(profileName)
	if err != nil {
		return err
	}

	gp, err := gpmc.New(auth, gpmc.WithDeviceProfile(profile), gpmc.WithAlbumStore(albumStorePath(cfgPath)))
	if err != nil {
		return fmt.Errorf("gpmc.New: %w", err)
	}

	srv, err := web.New(cfg, gp, log)
	if err != nil {
		return fmt.Errorf("web.New: %w", err)
	}

	mountWebDAV(srv, gp, log)
	mountS3(srv, gp, log)

	log.Info("gpix web started",
		"listen", cfg.Listen,
		"username", cfg.Username,
		"profile", profileName,
		"secret_path", secretPath,
	)
	return srv.Run(ctx)
}

func mountWebDAV(srv *web.Server, gp *gpmc.Client, log *slog.Logger) {
	cfg, err := webdav.LoadFromEnv()
	if err != nil {
		log.Debug("webdav disabled", "reason", err.Error())
		return
	}
	dav, err := webdav.NewServer(cfg, gp, log.With("service", "webdav"))
	if err != nil {
		log.Warn("webdav init failed", "err", err)
		return
	}
	srv.Mount("/dav/", dav.Handler())
	log.Info("webdav mounted", "path", "/dav/")
}

func mountS3(srv *web.Server, gp *gpmc.Client, log *slog.Logger) {
	cfg, err := s3api.LoadFromEnv()
	if err != nil {
		log.Debug("s3 disabled", "reason", err.Error())
		return
	}
	s3srv, err := s3api.NewServer(cfg, gp, log.With("service", "s3"))
	if err != nil {
		log.Warn("s3 init failed", "err", err)
		return
	}
	srv.Mount("/s3/", http.StripPrefix("/s3", s3srv.Handler()))
	log.Info("s3 mounted", "path", "/s3/", "bucket", cfg.Bucket)
}

func runCLI(authFlag, profileFlag, qualityFlag string, conc int, recursive, force, deleteAfter bool) {
	auth := authFlag
	if auth == "" {
		auth = os.Getenv("GP_AUTH_DATA")
	}
	if auth == "" {
		die("missing GP auth: pass -auth or set GP_AUTH_DATA")
	}
	if flag.NArg() == 0 {
		die("missing positional path argument")
	}

	q, err := parseQuality(qualityFlag)
	if err != nil {
		die(err.Error())
	}
	profile, err := parseProfile(coalesce(profileFlag, "pixel-xl"))
	if err != nil {
		die(err.Error())
	}

	client, err := gpmc.New(auth, gpmc.WithDeviceProfile(profile))
	if err != nil {
		die(err.Error())
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	opts := gpmc.UploadOpts{
		Quality:     q,
		Force:       force,
		Concurrency: conc,
		Recursive:   recursive,
		DeleteAfter: deleteAfter,
	}

	results, err := client.UploadFiles(ctx, flag.Args(), opts, func(ev gpmc.UploadEvent) {
		if ev.Err != nil {
			fmt.Fprintf(os.Stderr, "[%s] %s: %v\n", ev.Stage, ev.Path, ev.Err)
			return
		}
		fmt.Fprintf(os.Stderr, "[%s] %s\n", ev.Stage, ev.Path)
	})
	if err != nil {
		die(err.Error())
	}

	var failures int
	for _, r := range results {
		if r.Err != nil {
			fmt.Fprintf(os.Stderr, "FAIL %s: %v\n", r.Path, r.Err)
			failures++
			continue
		}
		marker := "OK"
		if r.Skipped {
			marker = "SKIP"
		}
		fmt.Printf("%s\t%s\t%s\n", marker, r.Path, r.MediaKey)
	}
	if failures > 0 {
		os.Exit(2)
	}
}

func runHashpw() {
	fmt.Fprint(os.Stderr, "password: ")
	r := bufio.NewReader(os.Stdin)
	pw, _ := r.ReadString('\n')
	pw = strings.TrimRight(pw, "\r\n")
	if pw == "" {
		die("empty password")
	}
	h, err := bcrypt.GenerateFromPassword([]byte(pw), 12)
	if err != nil {
		die(err.Error())
	}
	fmt.Println(string(h))
}

func parseQuality(s string) (gpmc.Quality, error) {
	switch s {
	case "original":
		return gpmc.QualityOriginal, nil
	case "saver":
		return gpmc.QualitySaver, nil
	case "quota":
		return gpmc.QualityUseQuota, nil
	}
	return 0, fmt.Errorf("unknown quality %q (want original|saver|quota)", s)
}

func parseProfile(s string) (gpmc.DeviceProfile, error) {
	switch s {
	case "", "pixel-xl":
		return gpmc.DefaultPixelXL(), nil
	case "pixel-5":
		return gpmc.DefaultPixel5(), nil
	}
	return gpmc.DeviceProfile{}, fmt.Errorf("unknown profile %q", s)
}

func newLogger(level string) *slog.Logger {
	var lvl slog.Level
	switch level {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lvl}))
}

func coalesce(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}

func die(msg string) {
	fmt.Fprintln(os.Stderr, "error:", msg)
	os.Exit(1)
}

func loadDotEnv(path string) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:eq])
		val := strings.TrimSpace(line[eq+1:])
		if len(val) >= 2 && (val[0] == '"' && val[len(val)-1] == '"' || val[0] == '\'' && val[len(val)-1] == '\'') {
			val = val[1 : len(val)-1]
		}
		if _, set := os.LookupEnv(key); set {
			continue
		}
		_ = os.Setenv(key, val)
	}
	return sc.Err()
}

var _ = errors.New
