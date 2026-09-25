package app

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/Y4NN777/7review/agent/channel"
	"github.com/Y4NN777/7review/agent/config"
	"github.com/Y4NN777/7review/agent/orchestrator"
	"github.com/Y4NN777/7review/agent/pipeline"
	"github.com/Y4NN777/7review/agent/profile"
	"github.com/Y4NN777/7review/agent/skills"
	"github.com/Y4NN777/7review/agent/tools"
)

// Server wires HTTP routes to the review pipeline.
type Server struct {
	cfg      *config.Config
	pipeline *pipeline.Pipeline
	mux      *http.ServeMux
	work     chan workItem
	seenMu   sync.Mutex
	seen     map[string]time.Time
	activeMu sync.Mutex
	active   map[string]bool
	stats    workerStats
}

const defaultWebhookJobTimeout = 15 * time.Minute
const chatMaxBodyBytes int64 = 64 * 1024
const reportMaxBodyBytes int64 = 512 * 1024

// NewServer builds the application server and all runtime dependencies.
func NewServer() (*Server, error) {
	cfg, err := config.LoadConfig()
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}

	inputProfile, err := profile.Load(cfg.InputProfilePath)
	if err != nil {
		return nil, err
	}

	skillsDir := cfg.SkillsDir
	if inputProfile != nil && inputProfile.Skills.Directory != "" && !envSet("SKILLS_DIR") {
		skillsDir = inputProfile.Skills.Directory
	}
	sl := &skills.Loader{SkillsDir: skillsDir}
	if inputProfile != nil {
		sl.Profile = inputProfile.Skills
	}
	if err := sl.Load(); err != nil {
		return nil, fmt.Errorf("skills: %w", err)
	}

	modelOrchestrator, err := orchestrator.BuildOrchestrator(cfg)
	if err != nil {
		return nil, fmt.Errorf("orchestrator: %w", err)
	}

	s := &Server{
		cfg: cfg,
		pipeline: &pipeline.Pipeline{
			Config:           cfg,
			SkillLoader:      sl,
			Profile:          inputProfile,
			Orchestrator:     modelOrchestrator,
			Jobs:             pipeline.NewFileRunStore(cfg.MemoryDir + "/runs"),
			Policy:           policyFilter(inputProfile),
			FindingValidator: findingValidator(inputProfile),
		},
		mux:    http.NewServeMux(),
		seen:   make(map[string]time.Time),
		active: make(map[string]bool),
	}
	s.work = make(chan workItem, cfg.WebhookQueueSize)
	gitlab := tools.NewGitLabClient(cfg.GitLabURL, cfg.GitLabToken)
	github := tools.NewGitHubClient(cfg.GitHubAPIURL, cfg.GitHubToken)
	router := tools.ProviderRouter{
		SCM: map[string]tools.SCM{
			"gitlab": gitlab,
			"github": github,
		},
		Publishers: map[string]tools.Publisher{
			"gitlab": gitlab,
			"github": github,
		},
	}
	s.pipeline.SCM = router
	s.pipeline.SCMPublisher = router
	if cfg.PolicyV2Mode != "legacy" {
		s.pipeline.TrustedPolicy = pipeline.SCMPolicyAdmission{
			Mode: cfg.PolicyV2Mode, Path: cfg.PolicyV2Path, Reader: router,
			RuntimeCapabilities: append([]string(nil), cfg.PolicyRuntimeCapabilities...),
		}
	}
	s.pipeline.ContextReducer = tools.NewHeadroomReducer(cfg.HeadroomURL, time.Duration(cfg.HeadroomTimeout)*time.Millisecond)
	s.pipeline.Memory = reviewMemoryStore(cfg)
	s.pipeline.Channels = channel.NewManager(channelConfigs(inputProfile, cfg))
	s.startWorkers()
	s.startChannelListeners(context.Background())
	s.routes()
	return s, nil
}

func (s *Server) startChannelListeners(ctx context.Context) {
	if s == nil || s.pipeline == nil || s.pipeline.Channels == nil {
		return
	}
	if err := s.pipeline.Channels.Start(ctx, func(msg channel.InboundMessage) {
		if _, _, err := s.routeChannelMessageContext(ctx, msg); err != nil {
			log.Printf("[server] channel listener message rejected: channel=%s run=%s error=%v", msg.Channel, msg.RunID, err)
		}
	}); err != nil {
		log.Printf("[server] channel listeners not started: %v", err)
	}
}

func findingValidator(inputProfile *profile.CompiledProfile) pipeline.FindingValidator {
	if inputProfile == nil || inputProfile.Validation.MinConfidence == 0 {
		return pipeline.DefaultFindingValidator{}
	}
	return pipeline.DefaultFindingValidator{MinConfidence: inputProfile.Validation.MinConfidence}
}

func policyFilter(inputProfile *profile.CompiledProfile) pipeline.PolicyFilter {
	if inputProfile == nil || len(inputProfile.PathPolicy.Ignore) == 0 {
		return pipeline.DefaultPolicyFilter{}
	}
	return pipeline.PathPolicyFilter{Ignore: inputProfile.PathPolicy.Ignore}
}

func channelConfigs(inputProfile *profile.CompiledProfile, cfg *config.Config) []channel.Config {
	var configs []channel.Config
	if inputProfile != nil {
		for _, item := range inputProfile.Channels {
			if !item.Enabled {
				continue
			}
			configs = append(configs, channel.Config{
				Name:              item.Name,
				Provider:          item.Provider,
				Enabled:           item.Enabled,
				InboundToken:      firstNonEmptyString(item.InboundToken, cfg.ChannelInboundToken),
				AuthorizedSenders: firstNonEmptyList(item.AuthorizedSenders, cfg.ChannelAuthorizedSenders),
				Settings:          item.Settings,
			})
		}
	}
	if len(configs) == 0 && cfg.ChannelInboundToken != "" {
		configs = append(configs, channel.Config{
			Name:              "operator_log",
			Provider:          "log",
			Enabled:           true,
			InboundToken:      cfg.ChannelInboundToken,
			AuthorizedSenders: cfg.ChannelAuthorizedSenders,
		})
	}
	return configs
}

func firstNonEmptyList(primary []string, fallback []string) []string {
	if len(primary) > 0 {
		return primary
	}
	return fallback
}

func envSet(key string) bool {
	_, ok := os.LookupEnv(key)
	return ok
}

// ListenAndServe starts the HTTP server.
func (s *Server) ListenAndServe() error {
	log.Printf("[server] 7review listening on %s", s.cfg.ListenAddr)
	return s.httpServer().ListenAndServe()
}

func (s *Server) httpServer() *http.Server {
	cfg := &config.Config{ListenAddr: ":8080"}
	if s != nil && s.cfg != nil {
		cfg = s.cfg
	}
	return &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           s.mux,
		ReadHeaderTimeout: durationMS(cfg.ReadHeaderTimeout, 5*time.Second),
		ReadTimeout:       durationMS(cfg.ReadTimeout, 30*time.Second),
		WriteTimeout:      durationMS(cfg.WriteTimeout, 120*time.Second),
		IdleTimeout:       durationMS(cfg.IdleTimeout, 120*time.Second),
	}
}

func durationMS(value int, fallback time.Duration) time.Duration {
	if value > 0 {
		return time.Duration(value) * time.Millisecond
	}
	return fallback
}
