package server

import (
	"fmt"
	"net/http"
	"path/filepath"

	"github.com/hashicorp/go-hclog"
	"google.golang.org/grpc"

	"github.com/0xPolygon/polygon-edge/blockchain/storage"
	"github.com/0xPolygon/polygon-edge/blockchain/storage/leveldb"
	"github.com/0xPolygon/polygon-edge/blockchain/storage/memory"
	"github.com/0xPolygon/polygon-edge/helper/common"
	"github.com/0xPolygon/polygon-edge/network"
	"github.com/0xPolygon/polygon-edge/secrets"
	itrie "github.com/0xPolygon/polygon-edge/state/immutable-trie"
)

var dirPaths = []string{
	"blockchain",
	"trie",
}

// Manager is the main server manager - designed to exist once per process and will
// use the fork information from the genesis.json file to load to appropriate consensus engine
// for each fork and manage the transition between them by spinning up and down fresh Server instances
type Manager struct {
	Config           *Config
	active           *Server
	logger           hclog.Logger
	prometheusServer *http.Server
	secretsManager   secrets.SecretsManager
	libp2p           *network.Server
	stateStorage     itrie.Storage
	state            *itrie.State
	db               storage.Storage
	grpcServer       *grpc.Server
}

func NewManager(cfg *Config) (*Manager, error) {
	m := &Manager{
		Config:     cfg,
		grpcServer: grpc.NewServer(grpc.UnaryInterceptor(unaryInterceptor)),
	}

	logger, err := newLoggerFromConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("could not setup new logger instance, %w", err)
	}
	m.logger = logger.Named("server")

	m.logger.Info("Data dir", "path", cfg.DataDir)

	// Generate all the paths in the dataDir
	if err := common.SetupDataDir(cfg.DataDir, dirPaths, 0770); err != nil {
		return nil, fmt.Errorf("failed to create data directories: %w", err)
	}

	// prometheus
	if cfg.Telemetry.PrometheusAddr != nil {
		// Only setup telemetry if `PrometheusAddr` has been configured.
		if err := setupTelemetry(); err != nil {
			return nil, err
		}

		m.prometheusServer = startPrometheusServer(cfg.Telemetry.PrometheusAddr, m.logger)
	}

	// Set up datadog profiler
	if ddErr := enableDataDogProfiler(m.logger); err != nil {
		m.logger.Error("DataDog profiler setup failed", "err", ddErr.Error())
	}

	// setup secrets
	secretsManager, err := setupSecretsManager(cfg, m.logger)
	if err != nil {
		return nil, err
	}
	m.secretsManager = secretsManager

	// setup libp2p
	netConfig := cfg.Network
	netConfig.Chain = cfg.Chain
	netConfig.DataDir = filepath.Join(cfg.DataDir, "libp2p")
	netConfig.SecretsManager = m.secretsManager

	libp2p, err := network.NewServer(logger, netConfig)
	if err != nil {
		return nil, err
	}
	m.libp2p = libp2p

	// blockchain object
	stateStorage, err := itrie.NewLevelDBStorage(filepath.Join(cfg.DataDir, "trie"), logger)
	if err != nil {
		return nil, err
	}
	m.stateStorage = stateStorage

	st := itrie.NewState(stateStorage)
	m.state = st

	// create storage instance for blockchain
	var db storage.Storage
	{
		if cfg.DataDir == "" {
			db, err = memory.NewMemoryStorage(nil)
			if err != nil {
				return nil, err
			}
		} else {
			db, err = leveldb.NewLevelDBStorage(
				filepath.Join(cfg.DataDir, "blockchain"),
				m.logger,
			)
			if err != nil {
				return nil, err
			}
		}
	}
	m.db = db

	return m, nil
}

func (m *Manager) Start() error {
	// first analyse the forks and see if we need to do anything or just load up the default server
	if len(m.Config.Chain.Params.EngineForks) == 0 {
		// no forks, just start the default server
		srv, err := NewManagedServer(
			m.Config,
			m.logger,
			m.prometheusServer,
			m.secretsManager,
			m.libp2p,
			m.stateStorage,
			m.state,
			m.db,
			m.grpcServer,
		)
		if err != nil {
			return err
		}
		m.active = srv
	} else {
	}

	return nil
}

func (m *Manager) Close() {
	m.active.Close()
}
