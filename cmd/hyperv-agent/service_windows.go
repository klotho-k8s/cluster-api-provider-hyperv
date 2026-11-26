//go:build windows

/*
Copyright 2026 Luther Monson.

Licensed under the MIT License. See LICENSE in the project root for details.
*/

package main

import (
	"context"
	"fmt"

	"golang.org/x/sys/windows/svc"
)

// agentService implements svc.Handler, running the gRPC server under the
// Windows Service Control Manager.
type agentService struct {
	cfg serveConfig
}

// Execute runs the service: it starts serve in a goroutine and stops it when
// the SCM asks it to stop or shut down.
func (a *agentService) Execute(_ []string, r <-chan svc.ChangeRequest, s chan<- svc.Status) (bool, uint32) {
	const accepted = svc.AcceptStop | svc.AcceptShutdown
	s <- svc.Status{State: svc.StartPending}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errc := make(chan error, 1)
	go func() { errc <- serve(ctx, a.cfg, func(string, ...any) {}) }()

	s <- svc.Status{State: svc.Running, Accepts: accepted}
	for {
		select {
		case err := <-errc:
			if err != nil {
				return true, 1
			}
			return false, 0
		case c := <-r:
			switch c.Cmd {
			case svc.Interrogate:
				s <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				s <- svc.Status{State: svc.StopPending}
				cancel()
				<-errc
				return false, 0
			}
		}
	}
}

// runAgent runs under the SCM when launched as a service, else in the
// foreground.
func runAgent(cfg serveConfig) error {
	isSvc, err := svc.IsWindowsService()
	if err != nil {
		return fmt.Errorf("agent: detect service mode: %w", err)
	}
	if isSvc {
		return svc.Run(serviceName, &agentService{cfg: cfg})
	}
	return runForeground(cfg)
}
