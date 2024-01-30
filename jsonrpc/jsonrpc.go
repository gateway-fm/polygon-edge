package jsonrpc

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/hashicorp/go-hclog"

	"github.com/0xPolygon/polygon-edge/versioning"
)

type serverType int

const (
	serverIPC serverType = iota
	serverHTTP
	serverWS
)

func (s serverType) String() string {
	switch s {
	case serverIPC:
		return "ipc"
	case serverHTTP:
		return "http"
	case serverWS:
		return "ws"
	default:
		panic("BUG: Not expected") //nolint:gocritic
	}
}

// JSONRPC is an API consensus
type JSONRPC struct {
	logger     hclog.Logger
	config     *Config
	dispatcher dispatcher
	stopChan   chan struct{}
}

type dispatcher interface {
	RemoveFilterByWs(conn wsConn)
	HandleWs(reqBody []byte, conn wsConn) ([]byte, error)
	Handle(reqBody []byte, timings *timings) ([]byte, error)
}

// JSONRPCStore defines all the methods required
// by all the JSON RPC endpoints
type JSONRPCStore interface {
	EthStore
	networkStore
	txPoolStore
	filterManagerStore
	debugStore
	bridgeDataFactory
}

type Config struct {
	StoreContainer           *StoreContainer
	Addr                     *net.TCPAddr
	ChainID                  uint64
	ChainName                string
	AccessControlAllowOrigin []string
	PriceLimit               uint64
	BatchLengthLimit         uint64
	BlockRangeLimit          uint64
	TxHandoff                string
	LogTimings               bool
}

// NewJSONRPC returns the JSONRPC http server
func NewJSONRPC(logger hclog.Logger, config *Config) (*JSONRPC, error) {
	d, err := newDispatcher(
		logger,
		config.StoreContainer,
		&dispatcherParams{
			chainID:                 config.ChainID,
			chainName:               config.ChainName,
			priceLimit:              config.PriceLimit,
			jsonRPCBatchLengthLimit: config.BatchLengthLimit,
			blockRangeLimit:         config.BlockRangeLimit,
			txHandoff:               config.TxHandoff,
		},
	)

	if err != nil {
		return nil, err
	}

	srv := &JSONRPC{
		logger:     logger.Named("jsonrpc"),
		config:     config,
		dispatcher: d,
	}

	// start http server
	if err := srv.setupHTTP(); err != nil {
		return nil, err
	}

	return srv, nil
}

func (j *JSONRPC) setupHTTP() error {
	j.logger.Info("http server started", "addr", j.config.Addr.String())

	lis, err := net.Listen("tcp", j.config.Addr.String())
	if err != nil {
		return err
	}

	// NewServeMux must be used, as it disables all debug features.
	// For some strange reason, with DefaultServeMux debug/vars is always enabled (but not debug/pprof).
	// If pprof need to be enabled, this should be DefaultServeMux
	mux := http.NewServeMux()

	// The middleware factory returns a handler, so we need to wrap the handler function properly.
	jsonRPCHandler := http.HandlerFunc(j.handle)
	mux.Handle("/", middlewareFactory(j.config)(jsonRPCHandler))

	mux.HandleFunc("/ws", j.handleWs)

	mux.HandleFunc("/health", j.handleHealth)

	srv := http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 60 * time.Second,
	}

	j.stopChan = make(chan struct{})

	go func() {
		if err := srv.Serve(lis); err != nil {
			j.logger.Error("closed http connection", "err", err)
		}
	}()

	go func() {
		select {
		case <-j.stopChan:
			err = srv.Shutdown(context.Background())
			if err != nil {
				j.logger.Error("unable to close http connection", "err", err)
			}
		}
	}()

	return nil
}

func (j *JSONRPC) Stop() {
	close(j.stopChan)
}

func (j *JSONRPC) handleHealth(w http.ResponseWriter, _ *http.Request) {
	_, _ = w.Write([]byte("OK"))
}

// The middlewareFactory builds a middleware which enables CORS using the provided config.
func middlewareFactory(config *Config) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")

			for _, allowedOrigin := range config.AccessControlAllowOrigin {
				if allowedOrigin == "*" {
					w.Header().Set("Access-Control-Allow-Origin", "*")

					break
				}

				if allowedOrigin == origin {
					w.Header().Set("Access-Control-Allow-Origin", origin)

					break
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

// wsUpgrader defines upgrade parameters for the WS connection
var wsUpgrader = websocket.Upgrader{
	// Uses the default HTTP buffer sizes for Read / Write buffers.
	// Documentation specifies that they are 4096B in size.
	// There is no need to have them be 4x in size when requests / responses
	// shouldn't exceed 1024B
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
}

// wsWrapper is a wrapping object for the web socket connection and logger
type wsWrapper struct {
	sync.Mutex

	ws       *websocket.Conn // the actual WS connection
	logger   hclog.Logger    // module logger
	filterID string          // filter ID
}

func (w *wsWrapper) SetFilterID(filterID string) {
	w.filterID = filterID
}

func (w *wsWrapper) GetFilterID() string {
	return w.filterID
}

// WriteMessage writes out the message to the WS peer
func (w *wsWrapper) WriteMessage(messageType int, data []byte) error {
	w.Lock()
	defer w.Unlock()
	writeErr := w.ws.WriteMessage(messageType, data)

	if writeErr != nil {
		w.logger.Error(
			fmt.Sprintf("Unable to write WS message, %s", writeErr.Error()),
		)
	}

	return writeErr
}

// isSupportedWSType returns a status indicating if the message type is supported
func isSupportedWSType(messageType int) bool {
	return messageType == websocket.TextMessage ||
		messageType == websocket.BinaryMessage
}

func (j *JSONRPC) handleWs(w http.ResponseWriter, req *http.Request) {
	// CORS rule - Allow requests from anywhere
	wsUpgrader.CheckOrigin = func(r *http.Request) bool { return true }

	// Upgrade the connection to a WS one
	ws, err := wsUpgrader.Upgrade(w, req, nil)
	if err != nil {
		j.logger.Error(fmt.Sprintf("Unable to upgrade to a WS connection, %s", err.Error()))

		return
	}

	// Defer WS closure
	defer func(ws *websocket.Conn) {
		err = ws.Close()
		if err != nil {
			j.logger.Error(
				fmt.Sprintf("Unable to gracefully close WS connection, %s", err.Error()),
			)
		}
	}(ws)

	wrapConn := &wsWrapper{ws: ws, logger: j.logger}

	j.logger.Info("Websocket connection established")
	// Run the listen loop
	for {
		// Read the incoming message
		msgType, message, err := ws.ReadMessage()
		if err != nil {
			if websocket.IsCloseError(err,
				websocket.CloseGoingAway,
				websocket.CloseNormalClosure,
				websocket.CloseAbnormalClosure,
			) {
				// Accepted close codes
				j.logger.Info("Closing WS connection gracefully")
			} else {
				j.logger.Error(fmt.Sprintf("Unable to read WS message, %s", err.Error()))
				j.logger.Info("Closing WS connection with error")
			}

			j.dispatcher.RemoveFilterByWs(wrapConn)

			break
		}

		if isSupportedWSType(msgType) {
			go func() {
				resp, handleErr := j.dispatcher.HandleWs(message, wrapConn)
				if handleErr != nil {
					j.logger.Error(fmt.Sprintf("Unable to handle WS request, %s", handleErr.Error()))

					_ = wrapConn.WriteMessage(
						msgType,
						[]byte(fmt.Sprintf("WS Handle error: %s", handleErr.Error())),
					)
				} else {
					_ = wrapConn.WriteMessage(msgType, resp)
				}
			}()
		}
	}
}

func (j *JSONRPC) handle(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	w.Header().Set(
		"Access-Control-Allow-Headers",
		"Accept, Content-Type, Content-Length, Accept-Encoding, X-CSRF-Token, Authorization",
	)

	switch req.Method {
	case "POST":
		j.handleJSONRPCRequest(w, req)
	case "GET":
		j.handleGetRequest(w)
	case "OPTIONS":
		// nothing to return
	default:
		_, _ = w.Write([]byte("method " + req.Method + " not allowed"))
	}
}

func (j *JSONRPC) handleJSONRPCRequest(w http.ResponseWriter, req *http.Request) {
	data, err := io.ReadAll(req.Body)
	if err != nil {
		_, _ = w.Write([]byte(err.Error()))

		return
	}

	// log request
	j.logger.Trace("handle", "request", string(data))

	timings := &timings{}
	resp, err := j.dispatcher.Handle(data, timings)

	if err != nil {
		_, _ = w.Write([]byte(err.Error()))
	} else {
		_, _ = w.Write(resp)
	}

	if j.config.LogTimings {
		t, err := timings.Output()
		if err != nil {
			j.logger.Error("handle", "timings", err.Error())
		} else {
			j.logger.Info(fmt.Sprintf("call_timings=%s", t))
		}
	}
	j.logger.Trace("handle", "response", string(resp))
}

type GetResponse struct {
	Name    string `json:"name"`
	ChainID uint64 `json:"chain_id"`
	Version string `json:"version"`
}

func (j *JSONRPC) handleGetRequest(writer io.Writer) {
	data := &GetResponse{
		Name:    j.config.ChainName,
		ChainID: j.config.ChainID,
		Version: versioning.Version,
	}

	resp, err := json.Marshal(data)
	if err != nil {
		_, _ = writer.Write([]byte(err.Error()))
	}

	if _, err = writer.Write(resp); err != nil {
		_, _ = writer.Write([]byte(err.Error()))
	}
}

type timing struct {
	Method   string
	Duration time.Duration
}

type timings struct {
	Timings []timing
}

func (t *timings) Output() (string, error) {
	type output struct {
		Method   string `json:"m"`
		Duration string `json:"d"`
	}

	res := make([]output, 0, len(t.Timings))

	for _, timing := range t.Timings {
		res = append(res, output{
			Method:   timing.Method,
			Duration: timing.Duration.String(),
		})
	}

	asJson, err := jsonit.Marshal(res)
	if err != nil {
		return "", err
	}

	return string(asJson), nil
}
