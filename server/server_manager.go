package server

import (
	"fmt"
	"net/http"
	"path/filepath"

	"github.com/hashicorp/go-hclog"

	"github.com/0xPolygon/polygon-edge/blockchain/storage"
	"github.com/0xPolygon/polygon-edge/blockchain/storage/leveldb"
	"github.com/0xPolygon/polygon-edge/blockchain/storage/memory"
	"github.com/0xPolygon/polygon-edge/chain"
	"github.com/0xPolygon/polygon-edge/helper/common"
	"github.com/0xPolygon/polygon-edge/secrets"
	itrie "github.com/0xPolygon/polygon-edge/state/immutable-trie"
	"github.com/0xPolygon/polygon-edge/types"
)

var dirPaths = []string{
	"blockchain",
	"trie",
}

// Manager is the main server manager - designed to exist once per process and will
// use the fork information from the genesis.json file to load to appropriate consensus engine
// for each fork and manage the transition between them by spinning up and down fresh Server instances
type Manager struct {
	Config               *Config
	active               *Server
	logger               hclog.Logger
	prometheusServer     *http.Server
	secretsManager       secrets.SecretsManager
	stateStorage         itrie.Storage
	state                *itrie.State
	db                   storage.Storage
	originalEngineConfig map[string]interface{}
	nextFork             *uint64
	currentHeader        *types.Header
}

func NewManager(cfg *Config) (*Manager, error) {
	m := &Manager{
		Config:               cfg,
		originalEngineConfig: cfg.Chain.Params.Engine,
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

	// setup secrets
	secretsManager, err := setupSecretsManager(cfg, m.logger)
	if err != nil {
		return nil, err
	}
	m.secretsManager = secretsManager

	// blockchain object
	stateStorage, err := itrie.NewLevelDBStorage(filepath.Join(cfg.DataDir, "trie"), logger)
	if err != nil {
		return nil, err
	}
	m.stateStorage = stateStorage

	st := itrie.NewState(stateStorage)
	m.state = st

	return m, nil
}

func (m *Manager) Start() error {
	// first analyse the forks and see if we need to do anything or just load up the default server
	if len(m.Config.Chain.Params.EngineForks) == 0 {
		m.logger.Info("no forks detected, running in simple mode")
		// no forks, just start the default server
		srv, err := m.createServer(m.Config, types.ZeroHash, nil, 0)
		if err != nil {
			return err
		}
		m.active = srv
		err = m.active.start()
		if err != nil {
			return err
		}
	} else {
		m.logger.Info("forks detected, running in fork mode")
		err := m.loadNextFork()
		if err != nil {
			return fmt.Errorf("failed to load next fork: %w", err)
		}
	}

	return nil
}

func (m *Manager) createServer(
	config *Config,
	currentStateRoot types.Hash,
	additionalAlloc map[types.Address]*chain.GenesisAccount,
	forkNumber int,
) (*Server, error) {
	return NewManagedServer(
		config,
		m.logger,
		m.prometheusServer,
		m.secretsManager,
		m.stateStorage,
		m.state,
		m.db,
		currentStateRoot,
		additionalAlloc,
		forkNumber,
	)
}

// loadNextFork looks through the forks to find the starting point - forks should be ordered in the config file
// in the order you want to execute them
func (m *Manager) loadNextFork() error {
	// header could be available if we're in process at the fork, or if the application is starting
	// from cold we can read it from the database.
	initialStateRoot := types.ZeroHash
	var head uint64 = 0
	if m.currentHeader != nil {
		head = m.currentHeader.Number
		initialStateRoot = m.currentHeader.StateRoot
	}
	if head == 0 {
		hash, ok := m.db.ReadHeadHash()
		if ok {
			h, err := m.db.ReadHeader(hash)
			if err != nil {
				return err
			}
			head = h.Number
			initialStateRoot = h.StateRoot
		}
	}
	m.logger.Info("current head", "head", head)

	forks := m.Config.Chain.Params.EngineForks

	forkNumber := -1
	for _, fork := range forks {
		forkNumber++
		if fork.To == nil || (fork.To != nil && head < *fork.To) {
			// found the one we want - so now to manipulate the config for creating the server
			// starting with the engine config
			m.nextFork = fork.To
			var found = false
			newEngineParams := make(map[string]interface{})
			for key, value := range m.originalEngineConfig {
				if key == fork.Engine {
					found = true
					newEngineParams[key] = value
					break
				}
			}
			if !found {
				return fmt.Errorf("failed to find engine %s in config", fork.Engine)
			}

			// overwrite the engine params with the new one only containing details for the active
			// engine
			m.Config.Chain.Params.Engine = newEngineParams

			if fork.To == nil {
				m.logger.Info("server manager loading engine fork", "engine", fork.Engine, "block", "nil")
			} else {
				m.logger.Info("server manager loading engine fork", "engine", fork.Engine, "block", *fork.To)
			}

			// ensure that the stop block is set.  This will help the syncer to stop at the correct height
			// and ensures that any delay in the stop channel being closed won't cause the chain to run on
			// beyond the fork point
			m.Config.Chain.Params.StopBlock = fork.To

			srv, err := m.createServer(m.Config, initialStateRoot, fork.Alloc, forkNumber)
			if err != nil {
				return err
			}

			m.active = srv

			m.monitorForNextFork()

			err = m.active.start()
			if err != nil {
				return err
			}

			break
		}
	}

	return nil
}

func (m *Manager) monitorForNextFork() {
	sub := m.active.blockchain.SubscribeEvents()

	go func() {
		for {
			// internally blocks until an event is received or the subscription is closed (returning nil)
			ev := sub.GetEvent()

			if ev == nil {
				// subscription closed
				break
			}

			header := ev.Header()
			m.currentHeader = header
			m.logger.Debug("server manager monitored block", "number", header.Number, "next fork", *m.nextFork)
			if header.Number >= *m.nextFork {
				m.logger.Info("server manager reached next fork", "number", header.Number, "next fork", *m.nextFork)
				// we have reached the next fork point so stop the active server and create the next one
				m.active.Close()
				err := m.loadNextFork()
				if err != nil {
					m.logger.Error("failed to load next fork", "err", err)
					break
				}
			}
		}
	}()
}

func (m *Manager) Close() {
	m.active.Close()
	err := m.db.Close()
	if err != nil {
		m.logger.Error("failed to close database", "err", err)
	}
}
