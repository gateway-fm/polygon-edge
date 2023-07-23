package devp2p

import (
	"github.com/ethereum/go-ethereum/log"
	"github.com/hashicorp/go-hclog"
)

type PassThroughLogger struct {
	Log hclog.Logger
}

func (p PassThroughLogger) New(ctx ...interface{}) log.Logger {
	return PassThroughLogger{p.Log}
}

func (p PassThroughLogger) GetHandler() log.Handler {
	panic("get handler not supported on the passthrough logger")
}

func (p PassThroughLogger) SetHandler(h log.Handler) {
	//TODO implement me
	panic("implement me")
}

func (p PassThroughLogger) Trace(msg string, ctx ...interface{}) {
	p.Log.Trace(msg, ctx)
}

func (p PassThroughLogger) Debug(msg string, ctx ...interface{}) {
	p.Log.Debug(msg, ctx)
}

func (p PassThroughLogger) Info(msg string, ctx ...interface{}) {
	p.Log.Info(msg, ctx)
}

func (p PassThroughLogger) Warn(msg string, ctx ...interface{}) {
	p.Log.Warn(msg, ctx)
}

func (p PassThroughLogger) Error(msg string, ctx ...interface{}) {
	p.Log.Error(msg, ctx)
}

func (p PassThroughLogger) Crit(msg string, ctx ...interface{}) {
	p.Log.Error(msg, ctx)
}
