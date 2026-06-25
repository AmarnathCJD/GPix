package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"golang.org/x/crypto/bcrypt"

	"gpix/pkg/bridge"
	"gpix/pkg/cachedb"
	"gpix/pkg/dav"
	"gpix/pkg/gpmc"
	"gpix/pkg/gwcreds"
	"gpix/pkg/library"
	"gpix/pkg/mediacrypt"
	"gpix/pkg/oidc"
	"gpix/pkg/s3"
	"gpix/pkg/share"
	"gpix/pkg/store/gpmcstore"
	"gpix/pkg/users"
	"gpix/pkg/web"
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

	// The Telegram bot is optional: only start it when it's configured. Without
	// TG_BOT_TOKEN, `-mode all` just runs the web + gateways.
	if os.Getenv("TG_BOT_TOKEN") != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := startBot(ctx, log.With("service", "bot"), auth, profileFlag); err != nil {
				errs <- fmt.Errorf("bot: %w", err)
			}
		}()
	} else {
		log.Info("telegram bot disabled (set TG_BOT_TOKEN to enable)")
	}

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

	gp, err := gpmc.New(auth, gpmc.WithDeviceProfile(profile))
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
		dir := cfg.DataDir
		if dir == "" {
			dir = filepath.Dir(cfgPath)
		}
		if dir != "" && dir != "." {
			_ = os.MkdirAll(dir, 0o700)
		}
		secretPath = filepath.Join(dir, "secret.key")
	}
	secret, err := web.LoadOrCreateSecret(secretPath)
	if err != nil {
		return fmt.Errorf("secret: %w", err)
	}
	cfg.SecretKey = secret

	// Gateway credentials (S3 keys + WebDAV app password) live next to secret.key
	// and are managed from the web UI. Seed them from any values in the config.
	gwPath := filepath.Join(filepath.Dir(secretPath), "gateways.json")
	gw, err := gwcreds.Load(gwPath)
	if err != nil {
		return fmt.Errorf("gateway credentials: %w", err)
	}
	if err := gw.Seed(cfg.S3AccessKey, cfg.S3SecretKey, ""); err != nil {
		return fmt.Errorf("seed gateway credentials: %w", err)
	}

	// Media-encryption manager: master key + "encrypt new uploads" toggle, also
	// stored next to secret.key.
	encKeyPath := filepath.Join(filepath.Dir(secretPath), "encryption.key")
	encStatePath := filepath.Join(filepath.Dir(secretPath), "encryption.json")
	crypt, err := mediacrypt.Load(encKeyPath, encStatePath)
	if err != nil {
		return fmt.Errorf("encryption: %w", err)
	}
	if _, statErr := os.Stat(encStatePath); os.IsNotExist(statErr) && cfg.EncryptUploads {
		if err := crypt.SetEnabled(true); err != nil {
			return fmt.Errorf("seed encryption toggle: %w", err)
		}
	}

	profileName := coalesce(profileFlag, cfg.DeviceProfile, "pixel-xl")
	profile, err := parseProfile(profileName)
	if err != nil {
		return err
	}

	gp, err := gpmc.New(auth, gpmc.WithDeviceProfile(profile))
	if err != nil {
		return fmt.Errorf("gpmc.New: %w", err)
	}

	// Shared, background-refreshed listing cache, backed by a SQLite snapshot so
	// S3 ListObjects / WebDAV PROPFIND return filenames instantly on startup
	// (stale-while-revalidate) instead of re-walking the whole Google Photos
	// library each request.
	cacheStore, err := cachedb.Open(filepath.Join(filepath.Dir(secretPath), "cache.db"))
	if err != nil {
		return fmt.Errorf("cache db: %w", err)
	}
	defer cacheStore.Close()
	lib := library.New(gp, 0, cacheStore)
	lib.Start(ctx, 0)

	// SQLite-backed share links, stored next to secret.key.
	sharePath := filepath.Join(filepath.Dir(secretPath), "shares.db")
	shareStore, err := share.Open(sharePath)
	if err != nil {
		return fmt.Errorf("share store: %w", err)
	}
	defer shareStore.Close()

	// Optional Logto / OIDC login with email allowlist + max-users cap.
	var (
		oc *oidc.Client
		us *users.Store
	)
	if cfg.LogtoEnabled() {
		redirect := cfg.LogtoRedirect
		if redirect == "" {
			if cfg.ServerURL == "" {
				return fmt.Errorf("logto: set server_url (or logto_redirect) so the OAuth callback URL is known")
			}
			redirect = cfg.ServerURL + "/auth/logto/callback"
		}
		oc = oidc.New(oidc.Config{
			Issuer:       cfg.LogtoEndpoint,
			ClientID:     cfg.LogtoClientID,
			ClientSecret: cfg.LogtoClientSecret,
			RedirectURL:  redirect,
		})
		us, err = users.Open(filepath.Join(filepath.Dir(secretPath), "users.db"))
		if err != nil {
			return fmt.Errorf("users db: %w", err)
		}
		defer us.Close()
		log.Info("logto login enabled", "redirect", redirect, "max_users", cfg.MaxUsers, "allowlist", len(cfg.SignupAllowlist))
	}

	srv, err := web.New(cfg, gp, lib, gw, crypt, shareStore, oc, us, log)
	if err != nil {
		return fmt.Errorf("web.New: %w", err)
	}

	log.Info("gpix web started",
		"listen", cfg.Listen,
		"username", cfg.Username,
		"profile", profileName,
		"secret_path", secretPath,
	)

	// All gateways share one Google-Photos-backed object store (with the same
	// encryption manager as the web UI, so encrypted items round-trip on every
	// surface).
	be := gpmcstore.New(gp, gpmcstore.Options{TempDir: cfg.TempDir, Crypt: crypt, Library: lib})

	runners := []func(context.Context) error{srv.Run}

	if cfg.S3Listen != "" {
		// Credentials resolve through the shared store, so keys generated in the
		// web UI take effect immediately without a restart.
		s3srv, err := s3.New(s3.Config{
			Listen:      cfg.S3Listen,
			Bucket:      cfg.S3Bucket,
			Region:      cfg.S3Region,
			Credentials: gw,
		}, be, log.With("service", "s3"))
		if err != nil {
			return fmt.Errorf("s3.New: %w", err)
		}
		log.Info("gpix s3 gateway started", "listen", cfg.S3Listen, "bucket", cfg.S3Bucket)
		runners = append(runners, s3srv.Run)
	}

	if cfg.WebDAVListen != "" {
		davsrv, err := dav.New(dav.Config{
			Listen:   cfg.WebDAVListen,
			BasePath: cfg.WebDAVBasePath,
			Realm:    "gpix",
		}, be, basicAuthChecker(cfg.Username, cfg.PasswordHash, gw), log.With("service", "webdav"))
		if err != nil {
			return fmt.Errorf("dav.New: %w", err)
		}
		log.Info("gpix webdav gateway started", "listen", cfg.WebDAVListen, "base", cfg.WebDAVBasePath)
		runners = append(runners, davsrv.Run)
	}

	return runConcurrently(ctx, runners)
}

// runConcurrently runs every runner until ctx is cancelled or one of them
// returns an error, in which case the others are signalled to stop.
func runConcurrently(ctx context.Context, runners []func(context.Context) error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	errCh := make(chan error, len(runners))
	var wg sync.WaitGroup
	for _, run := range runners {
		run := run
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := run(ctx); err != nil {
				errCh <- err
				cancel()
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for e := range errCh {
		if e != nil {
			return e
		}
	}
	return nil
}

// basicAuthChecker returns a dav.Authenticator validating the WebDAV username
// against either the main bcrypt login password or the optional, revocable app
// password from the gateway store. Successful (username,password) pairs are
// cached by a SHA-256 fingerprint so bcrypt runs at most once per distinct
// password — WebDAV clients issue many requests per session.
func basicAuthChecker(username, passwordHash string, gw *gwcreds.Store) dav.Authenticator {
	var (
		mu        sync.RWMutex
		goodPrint [32]byte
		haveGood  bool
	)
	fingerprint := func(user, pass string) [32]byte {
		return sha256.Sum256([]byte(user + "\x00" + pass))
	}
	return func(user, pass string) bool {
		if subtle.ConstantTimeCompare([]byte(user), []byte(username)) != 1 {
			return false
		}
		// App password (cheap, constant-time) takes the fast path.
		if gw != nil && gw.CheckWebDAVPassword(pass) {
			return true
		}
		fp := fingerprint(user, pass)
		mu.RLock()
		cached := haveGood && subtle.ConstantTimeCompare(goodPrint[:], fp[:]) == 1
		mu.RUnlock()
		if cached {
			return true
		}
		if bcrypt.CompareHashAndPassword([]byte(passwordHash), []byte(pass)) != nil {
			return false
		}
		mu.Lock()
		goodPrint = fp
		haveGood = true
		mu.Unlock()
		return true
	}
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
