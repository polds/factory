package server

import (
	"net/http"
	"sync"

	"github.com/polds/factory/internal/automation"
	"github.com/polds/factory/internal/definition"
	"github.com/polds/factory/internal/forge/github"
	"github.com/polds/factory/internal/ingress"
	"github.com/polds/factory/internal/outbound"
)

// m4State is the ingress, automation, outbound, and cron runtime.
// Engine.Handle and Allow replacement share mu.
type m4State struct {
	mu         sync.Mutex
	engine     *automation.Engine
	notifier   *outbound.Notifier
	dispatcher *ingress.Dispatcher
	githubApp  github.App
	githubBase string

	ingressGitHub http.Handler
	ingressSlack  http.Handler
	ingressLinear http.Handler
	webhooks      http.Handler

	dispatchWake chan struct{}
	defWake      defWaker
	holder       string

	overlay map[string]runOverlay
	started map[string]struct{}
}

// runOverlay is the automation runner, secrets, and repos for one queued
// run. The runs table has no columns for them.
type runOverlay struct {
	Runner  string
	Secrets []string
	Repos   []definition.Repository
}

// defWaker is the non-blocking poke defsync polls.
type defWaker chan struct{}

func (w defWaker) Wake() <-chan struct{} { return w }
