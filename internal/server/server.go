package server

import (
	"context"
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
	"github.com/polds/factory/internal/events"
	"github.com/polds/factory/internal/gateway"
	"github.com/polds/factory/internal/scheduler"
	"github.com/polds/factory/internal/store"
)

// Config is the process configuration. Secret values live only in Secrets
// and in task env at assignment time; they are not written to the log.
type Config struct {
	DatabaseURL         string
	APIKey              string
	WorkerAPIKey        string
	DefinitionPath      string
	ExternalURL         string
	TaskServerURL       string
	HTTPAddr            string
	InferenceBaseURL    string
	InferenceAPIKey     string
	Secrets             map[string]string
	WorkerPool          string
	WorkerPlatform      string
	WorkerMaxConcurrent int
	S3                  artifacts.S3
	CancelGrace         time.Duration
	LeaseTTL            time.Duration
	Now                 func() time.Time
	Log                 *slog.Logger
}

// Server is one factoryd process: REST, SSE, the worker gateway, and the
// scheduler leader loop.
type Server struct {
	cfg    Config
	log    *slog.Logger
	store  *store.Store
	db     *pgxpool.Pool
	leader *pgx.Conn
	gw     *gateway.Gateway
	hub    *events.Hub
	api    http.Handler

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	once   sync.Once

	pings      chan beat
	wake       chan struct{}
	mu         sync.Mutex
	leaseMu    sync.Mutex
	countersMu sync.Mutex
	counters   scheduler.Counters

	loaded    loaded
	live      map[string]execRec
	byRun     map[string]string
	idleSince map[string]time.Time
	grace     map[string]*time.Timer
}

var (
	_ gateway.Sessions = (*Server)(nil)
	_ api.Backend      = (*Server)(nil)
	_ events.Source    = logSource{}
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
	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		st.Close()
		return nil, fmt.Errorf("server: db: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		st.Close()
		return nil, fmt.Errorf("server: db: %w", err)
	}
	leader, err := pgx.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		pool.Close()
		st.Close()
		return nil, fmt.Errorf("server: leader: %w", err)
	}

	runCtx, cancel := context.WithCancel(ctx)
	s := &Server{
		cfg:       cfg,
		log:       cfg.Log,
		store:     st,
		db:        pool,
		leader:    leader,
		hub:       events.NewHub(),
		ctx:       runCtx,
		cancel:    cancel,
		pings:     make(chan beat, 32),
		wake:      make(chan struct{}, 1),
		live:      map[string]execRec{},
		byRun:     map[string]string{},
		idleSince: map[string]time.Time{},
		grace:     map[string]*time.Timer{},
	}
	s.gw = gateway.New(s.authenticate, s, s.log)
	s.api = api.NewHandler(s, cfg.APIKey, problemSSE(events.Handler(logSource{s: s}, s.hub)))
	if err := s.applyBoot(runCtx); err != nil {
		s.Close()
		return nil, err
	}
	s.wg.Add(3)
	go s.loop()
	go s.listenQueue()
	go s.pingLoop()
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

// Handler serves /metrics, the worker websocket, and the REST API.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /metrics", s.handleMetrics)
	mux.Handle("GET /api/v1/selfhosted/worker/ws", s.gw)
	mux.Handle("/", s.api)
	return mux
}

// Close stops loops and releases database connections.
func (s *Server) Close() error {
	s.once.Do(func() {
		if s.cancel != nil {
			s.cancel()
		}
		s.wg.Wait()
		s.mu.Lock()
		for id, timer := range s.grace {
			timer.Stop()
			delete(s.grace, id)
		}
		s.mu.Unlock()
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
	run, err := s.store.GetRun(ctx, runID)
	if err != nil || isTerminalStatus(run.Status) {
		return
	}
	execID := s.execForRun(runID)
	if execID == "" {
		return
	}
	s.finish(ctx, runID, execID, store.ExecCancelled, "cancel_grace", "", nil, nil)
}
