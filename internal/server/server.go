package server

import (
	"context"
	"crypto/rsa"
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/polds/factory/internal/api"
	"github.com/polds/factory/internal/artifacts"
	"github.com/polds/factory/internal/console"
	"github.com/polds/factory/internal/defsync"
	"github.com/polds/factory/internal/events"
	"github.com/polds/factory/internal/gateway"
	"github.com/polds/factory/internal/inference"
	"github.com/polds/factory/internal/scheduler"
	"github.com/polds/factory/internal/store"
)

// Config is the process configuration. Secret values live only in Secrets
// and in task env at assignment time; they are not written to the log.
type Config struct {
	DatabaseURL      string
	APIKey           string
	WorkerAPIKey     string
	DefinitionPath   string
	ExternalURL      string
	TaskServerURL    string
	HTTPAddr         string
	InferenceBaseURL string
	InferenceAPIKey  string
	// InferenceExtraHeaders come from FACTORY_INFERENCE_OPENAI_COMPATIBLE_EXTRA_HEADERS.
	// Values redact. A name that matches Authorization or Content-Type
	// replaces that default on the model request. An empty API key omits
	// Authorization unless one of these lines sets it.
	InferenceExtraHeaders []inference.Header
	Secrets               map[string]string
	WorkerPool            string
	WorkerPlatform        string
	WorkerMaxConcurrent   int
	S3                    artifacts.S3
	CancelGrace           time.Duration
	LeaseTTL              time.Duration
	Now                   func() time.Time
	Log                   *slog.Logger
	// SchemasPath is the directory of schemas/<version>/*.json. Empty
	// searches upward from the working directory and the executable.
	// When that search misses, the handler serves the embedded schemas.
	SchemasPath string
	// DefinitionGitURL enables the defsync poller. The local
	// DefinitionPath is still loaded at boot and remains the published tree.
	DefinitionGitURL           string
	DefinitionGitRef           string
	DefinitionGitSubdir        string
	DefinitionGitProductionRef string
	DefinitionGitInterval      time.Duration
	// DefinitionGitCloner overrides the git cloner. Tests set it.
	DefinitionGitCloner defsync.Cloner

	// M4 ingress and outbound. Empty secrets leave that integration off.
	// GitHubTokenMode is app or pat. pat is the default so a planted
	// GH_TOKEN still reaches the task. app mints an installation token
	// at assignment time.
	GitHubAppID         int64
	GitHubAppKey        *rsa.PrivateKey
	GitHubWebhookSecret string
	GitHubTokenMode     string
	GitHubAPIBase       string
	SlackBotToken       string
	SlackSigningSecret  string
	SlackTeamID         string
	SlackAPIBase        string
	LinearAPIKey        string
	LinearWebhookSecret string
	LinearAPIBase       string
	DeliveryRetention   time.Duration
	ScheduleCatchUp     time.Duration
	// HTTPClient, when set, is the client for GitHub, Slack, and Linear.
	HTTPClient *http.Client
	// OutboundSleep overrides notifier backoff and coalesce waits.
	OutboundSleep func(context.Context, time.Duration) error
	// TokenRetrySleep overrides the wait between installation-token retries
	// during assignment. Tests advance a clock here instead of sleeping.
	TokenRetrySleep func(context.Context, time.Duration) error
	// ScheduleGate blocks the scheduler loop until it is closed. Nil starts
	// the loop immediately. Tests close it after a claim hook and a worker
	// are ready, so the first tick cannot park a seeded run before assignOne.
	ScheduleGate <-chan struct{}
	// Console is "on" or "off". Empty means on. Anything else is a startup error.
	Console string
}

// Server is one factoryd process: REST, SSE, the worker gateway, and the
// scheduler leader loop.
type Server struct {
	cfg         Config
	log         *slog.Logger
	store       *store.Store
	db          *pgxpool.Pool
	leader      *pgx.Conn
	leaderMu    sync.Mutex
	session     *pgx.Conn
	sessionID   string
	sessionHeld bool
	sessionMu   sync.Mutex
	sessionCfg  *pgx.ConnConfig
	gw          *gateway.Gateway
	hub         *events.Hub
	api         http.Handler
	schemas     http.Handler

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	once   sync.Once

	pings      chan beat
	wake       chan struct{}
	steerWake  chan struct{}
	gitSHA     string
	mu         sync.Mutex
	leaseMu    sync.Mutex
	scheduleMu sync.Mutex
	countersMu sync.Mutex
	counters   scheduler.Counters

	loaded    loaded
	live      map[string]execRec
	byRun     map[string]string
	idleSince map[string]time.Time
	grace     map[string]*time.Timer
	// suspended runs still occupy an execution, but not a worker slot.
	suspended map[string]struct{}
	orchMu    sync.Mutex

	// beforeTerminalEvent runs inside the transaction that publishes a
	// terminal status, after that status is written and before run.terminal
	// is inserted. Nil in production. Tests block here so a reader must not
	// observe the new status until the event commits with it.
	beforeTerminalEvent func()

	// beforeClaim runs after the scheduler has snapshotted a queued run and
	// before claim leases it. Nil in production. A test commits a new prompt
	// in that gap. The hook runs without leaseMu and must not call assignOne.
	beforeClaim func(runID string)

	// afterWorkerConnect runs at the end of WorkerConnected, after the
	// gateway has registered the socket. Nil in production. A test waits
	// on it so a reconnect is visible before the sweep.
	afterWorkerConnect func()

	// afterSessionRecover runs after a replacement session lock is held
	// and tracked rows have been restamped. Nil in production.
	afterSessionRecover func()

	// scheduleGate blocks loop until closed. Nil in production.
	scheduleGate <-chan struct{}

	// integ is the M4 ingress, automation, and outbound runtime.
	integ m4State
}

var (
	_ gateway.Sessions  = (*Server)(nil)
	_ api.Backend       = (*Server)(nil)
	_ api.WorkBackend   = (*Server)(nil)
	_ api.SchemaCatalog = (*Server)(nil)
	_ events.Source     = logSource{}
)

// Open migrates the database, applies the local definition, and starts the
// scheduler. The caller serves Handler and calls Close.
func Open(ctx context.Context, cfg Config) (*Server, error) {
	cfg, err := normalize(cfg)
	if err != nil {
		return nil, err
	}
	st, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return nil, err
	}
	if err := st.Migrate(ctx); err != nil {
		st.Close()
		return nil, err
	}
	poolCfg, err := pgxpool.ParseConfig(cfg.DatabaseURL)
	if err != nil {
		st.Close()
		return nil, fmt.Errorf("server: db: %w", err)
	}
	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		st.Close()
		return nil, fmt.Errorf("server: db: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		st.Close()
		return nil, fmt.Errorf("server: db: %w", err)
	}
	// pgx.Connect treats pool_max_conns as a server parameter. The pool parser
	// already removed it from ConnConfig.
	leader, err := pgx.ConnectConfig(ctx, poolCfg.ConnConfig.Copy())
	if err != nil {
		pool.Close()
		st.Close()
		return nil, fmt.Errorf("server: leader: %w", err)
	}
	sessionID, err := newServerSessionID()
	if err != nil {
		_ = leader.Close(ctx)
		pool.Close()
		st.Close()
		return nil, fmt.Errorf("server: session: %w", err)
	}
	// The session lock lives on its own connection. Closing the leader
	// connection drops leadership and leaves this lock held. watchSession
	// replaces the connection when this one dies.
	sessionCfg := poolCfg.ConnConfig.Copy()
	session, err := pgx.ConnectConfig(ctx, sessionCfg.Copy())
	if err != nil {
		_ = leader.Close(ctx)
		pool.Close()
		st.Close()
		return nil, fmt.Errorf("server: session: %w", err)
	}
	if err := lockServerSession(ctx, session, sessionID); err != nil {
		_ = session.Close(ctx)
		_ = leader.Close(ctx)
		pool.Close()
		st.Close()
		return nil, fmt.Errorf("server: session: %w", err)
	}

	runCtx, cancel := context.WithCancel(ctx)
	s := &Server{
		cfg:          cfg,
		log:          cfg.Log,
		store:        st,
		db:           pool,
		leader:       leader,
		session:      session,
		sessionID:    sessionID,
		sessionHeld:  true,
		sessionCfg:   sessionCfg,
		hub:          events.NewHub(),
		ctx:          runCtx,
		cancel:       cancel,
		pings:        make(chan beat, 32),
		wake:         make(chan struct{}, 1),
		steerWake:    make(chan struct{}, 1),
		live:         map[string]execRec{},
		byRun:        map[string]string{},
		idleSince:    map[string]time.Time{},
		grace:        map[string]*time.Timer{},
		suspended:    map[string]struct{}{},
		schemas:      schemaHandler(cfg.SchemasPath),
		scheduleGate: cfg.ScheduleGate,
	}
	s.gw = gateway.New(s.authenticate, s, s.log)
	if err := s.wire(); err != nil {
		_ = s.Close()
		return nil, err
	}
	s.api = api.NewHandler(apiFacade{s}, cfg.APIKey, problemSSE(events.Handler(logSource{s: s}, s.hub)))
	if err := s.applyBoot(runCtx); err != nil {
		_ = s.Close()
		return nil, err
	}
	if err := s.sweepFailureClasses(runCtx); err != nil {
		_ = s.Close()
		return nil, err
	}
	// Scheduler loops, the session watch, the dispatcher, and the cron ticker.
	loops := 6
	if cfg.DefinitionGitURL != "" {
		loops++
	}
	s.wg.Add(loops)
	go s.loop()
	go s.listenQueue()
	go s.pingLoop()
	go s.watchSession()
	go s.dispatchLoop()
	go s.cronLoop()
	if cfg.DefinitionGitURL != "" {
		go s.defsyncLoop()
	}
	return s, nil
}

func normalize(cfg Config) (Config, error) {
	if strings.TrimSpace(cfg.DatabaseURL) == "" {
		return Config{}, errors.New("server: FACTORY_DATABASE_URL is required")
	}
	if cfg.APIKey == "" {
		return Config{}, errors.New("server: FACTORY_API_KEY is required")
	}
	if cfg.WorkerAPIKey == "" {
		return Config{}, errors.New("server: FACTORY_WORKER_API_KEY is required")
	}
	if strings.TrimSpace(cfg.DefinitionPath) == "" {
		return Config{}, errors.New("server: FACTORY_DEFINITION_PATH is required")
	}
	if strings.TrimSpace(cfg.TaskServerURL) == "" {
		cfg.TaskServerURL = DefaultTaskServerURL
	}
	if cfg.WorkerPool == "" {
		cfg.WorkerPool = defaultWorkerPool
	}
	if cfg.WorkerPlatform == "" {
		cfg.WorkerPlatform = defaultWorkerPlatform
	}
	if cfg.WorkerMaxConcurrent <= 0 {
		cfg.WorkerMaxConcurrent = 1
	}
	if cfg.CancelGrace <= 0 {
		cfg.CancelGrace = defaultCancelGrace
	}
	if cfg.LeaseTTL <= 0 {
		cfg.LeaseTTL = defaultLeaseTTL
	}
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.DiscardHandler)
	}
	if cfg.Secrets == nil {
		cfg.Secrets = map[string]string{}
	}
	if cfg.DefinitionGitURL != "" {
		if cfg.DefinitionGitRef == "" {
			cfg.DefinitionGitRef = "main"
		}
		if cfg.DefinitionGitInterval <= 0 {
			cfg.DefinitionGitInterval = time.Minute
		}
	}
	mode, err := normalizeTokenMode(cfg.GitHubTokenMode)
	if err != nil {
		return Config{}, err
	}
	cfg.GitHubTokenMode = mode
	if mode == tokenModeApp && (cfg.GitHubAppID <= 0 || cfg.GitHubAppKey == nil) {
		return Config{}, errors.New("server: FACTORY_GITHUB_TOKEN_MODE=app requires FACTORY_GITHUB_APP_ID and FACTORY_GITHUB_APP_PRIVATE_KEY_FILE")
	}
	if (cfg.GitHubAppID > 0) != (cfg.GitHubAppKey != nil) {
		return Config{}, errors.New("server: FACTORY_GITHUB_APP_ID and FACTORY_GITHUB_APP_PRIVATE_KEY_FILE must both be set")
	}
	if cfg.DeliveryRetention <= 0 {
		cfg.DeliveryRetention = defaultDeliveryRetention
	}
	if cfg.ScheduleCatchUp <= 0 {
		cfg.ScheduleCatchUp = defaultScheduleCatchUp
	}
	switch strings.ToLower(strings.TrimSpace(cfg.Console)) {
	case "", "on":
		cfg.Console = "on"
	case "off":
		cfg.Console = "off"
	default:
		return Config{}, errors.New("server: FACTORY_CONSOLE must be on or off")
	}
	if strings.TrimSpace(cfg.S3.Endpoint) != "" {
		if cfg.S3.Region == "" {
			cfg.S3.Region = "us-east-1"
		}
		if cfg.S3.HTTP == nil {
			cfg.S3.HTTP = &http.Client{Timeout: 15 * time.Second}
		}
	}
	return cfg, nil
}

// Handler serves metrics, the worker websocket, the API, and the console.
// /api/ and /healthz stay on the API handler. The console is the catch-all
// when FACTORY_CONSOLE is on (the default). Off keeps today's API catch-all.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /metrics", s.handleMetrics)
	mux.Handle("GET /api/v1/selfhosted/worker/ws", s.gw)
	mux.Handle("/api/", s.api)
	mux.Handle("GET /healthz", s.api)
	if s.consoleOn() {
		mux.Handle("/", console.Handler(s.api))
	} else {
		mux.Handle("/", s.api)
	}
	return mux
}

func (s *Server) consoleOn() bool {
	return s.cfg.Console != "off"
}

// Close stops loops and releases database connections.
func (s *Server) Close() error {
	s.once.Do(func() {
		if s.cancel != nil {
			s.cancel()
		}
		s.closeOutbound()
		s.wg.Wait()
		s.mu.Lock()
		for id, timer := range s.grace {
			timer.Stop()
			delete(s.grace, id)
		}
		s.mu.Unlock()
		s.leaderMu.Lock()
		if s.leader != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			if _, err := s.leader.Exec(ctx, `SELECT pg_advisory_unlock($1::int4, $2::int4)`, leaderLockClass, leaderLockObj); err != nil && s.log != nil {
				s.log.Debug("leader unlock", "err", err)
			}
			if err := s.leader.Close(ctx); err != nil && s.log != nil {
				s.log.Debug("leader close", "err", err)
			}
			cancel()
			s.leader = nil
		}
		s.leaderMu.Unlock()
		s.sessionMu.Lock()
		if s.session != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			if s.sessionHeld && s.sessionID != "" {
				if _, err := s.session.Exec(ctx, `SELECT pg_advisory_unlock(hashtextextended($1, $2::bigint))`, serverSessionLockKey(s.sessionID), serverSessionLockSalt); err != nil && s.log != nil {
					s.log.Debug("session unlock", "err", err)
				}
			}
			if err := s.session.Close(ctx); err != nil && s.log != nil {
				s.log.Debug("session close", "err", err)
			}
			cancel()
			s.session = nil
			s.sessionHeld = false
		}
		s.sessionMu.Unlock()
		if s.db != nil {
			s.db.Close()
		}
		if s.store != nil {
			s.store.Close()
		}
	})
	return nil
}

func (s *Server) authenticate(bearer string) (string, bool) {
	if subtle.ConstantTimeCompare([]byte(bearer), []byte(s.cfg.WorkerAPIKey)) != 1 {
		return "", false
	}
	return "factory", true
}

func (s *Server) now() time.Time {
	if s.cfg.Now != nil {
		return s.cfg.Now()
	}
	return time.Now()
}

func (s *Server) nudge() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *Server) loop() {
	defer s.wg.Done()
	if s.scheduleGate != nil {
		select {
		case <-s.ctx.Done():
			return
		case <-s.scheduleGate:
		}
	}
	s.tick(s.ctx)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-s.wake:
			s.tick(s.ctx)
		case <-ticker.C:
			s.tick(s.ctx)
		}
	}
}

func (s *Server) listenQueue() {
	defer s.wg.Done()
	ch, err := s.store.Listen(s.ctx, "run_queue")
	if err != nil {
		if s.ctx.Err() == nil {
			s.log.Error("listen run_queue", "err", err)
		}
		return
	}
	for {
		select {
		case <-s.ctx.Done():
			return
		case _, ok := <-ch:
			if !ok {
				return
			}
			s.nudge()
		}
	}
}

func (s *Server) armGrace(runID string) {
	s.stopGrace(runID)
	timer := time.AfterFunc(s.cfg.CancelGrace, func() {
		s.graceExpired(runID)
	})
	s.mu.Lock()
	s.grace[runID] = timer
	s.mu.Unlock()
}

func (s *Server) stopGrace(runID string) {
	s.mu.Lock()
	timer := s.grace[runID]
	delete(s.grace, runID)
	s.mu.Unlock()
	if timer != nil {
		timer.Stop()
	}
}

func (s *Server) graceExpired(runID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	s.settleCancel(ctx, runID)
}
