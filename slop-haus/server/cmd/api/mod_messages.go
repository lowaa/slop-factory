package main

import (
	"github.com/lowaa/slop-factory/slop-haus/server/internal/agentmsg"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/apiserver"
)

type messagesModule struct {
	Service *agentmsg.Service
	Handler *agentmsg.Handler
}

func newMessagesModule(d *moduleDeps) (module, error) {
	c := d.container
	svc := agentmsg.NewService(agentmsg.NewPostgresStore(c.database), agentmsg.Config{}, c.logHandler)
	return &messagesModule{Service: svc, Handler: agentmsg.NewHandler(svc, c.logHandler)}, nil
}

func (m *messagesModule) Routes(r *apiserver.Deps) { r.MessageHandler = m.Handler }

func (*messagesModule) Close() {}
