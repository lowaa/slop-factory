package onboarding

import (
	"context"
	"fmt"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/manifest"
)

// Manifests reads the tenant's declared workspace; manifest.Service is one.
type Manifests interface {
	Get(ctx context.Context) (*manifest.Manifest, error)
}

// CodeHost says whether slop-haus can act on the tenant's code host;
// githubapp.Service is one.
type CodeHost interface {
	Connected(ctx context.Context) (bool, error)
}

// Step is one piece of setup a new tenant needs.
type Step struct {
	Key    string
	Title  string
	Detail string
	// Path is the board page where the step is done.
	Path string
	Done bool
}

// Status is where a tenant stands against the setup its workers need.
type Status struct {
	Steps []Step
}

// CodeWorkReady is whether a worker has anything to work on. A tenant with
// either a repo declared or a code host connected is not turned away: the
// check exists for the tenant that has set up neither, where a claim can
// only fail obscurely. A tenant with a code host but no manifest (older
// tenants) keeps working as it did.
func (s *Status) CodeWorkReady() bool {
	for _, step := range s.Steps {
		if step.Done {
			return true
		}
	}
	return false
}

// Missing lists the steps not done.
func (s *Status) Missing() []Step {
	var out []Step
	for _, step := range s.Steps {
		if !step.Done {
			out = append(out, step)
		}
	}
	return out
}

const (
	StepRepos    = "repos"
	StepCodeHost = "code_host"
)

// Service reads a tenant's setup from what already records it, so there is
// no state of its own to drift from the manifest or the credential.
type Service struct {
	manifests Manifests
	codeHost  CodeHost
}

func NewService(manifests Manifests, codeHost CodeHost) *Service {
	return &Service{manifests: manifests, codeHost: codeHost}
}

func (s *Service) Status(ctx context.Context) (*Status, error) {
	m, err := s.manifests.Get(ctx)
	if err != nil {
		return nil, fmt.Errorf("onboarding: read manifest: %w", err)
	}
	connected, err := s.codeHost.Connected(ctx)
	if err != nil {
		return nil, fmt.Errorf("onboarding: read code host: %w", err)
	}
	return &Status{Steps: []Step{
		{
			Key:    StepRepos,
			Title:  "Declare a repo",
			Detail: "The workspace manifest lists the repos workers clone. Without one they have nothing to work on.",
			Path:   "/workspace",
			Done:   m != nil && len(m.Repos) > 0,
		},
		{
			Key:    StepCodeHost,
			Title:  "Connect a code host",
			Detail: "slop-haus opens, checks and merges pull requests through a GitHub App installed on your repos.",
			Path:   "/git",
			Done:   connected,
		},
	}}, nil
}

// SetupGap is the message a claim is refused with, or "" when there is
// nothing to say. It satisfies ticket's SetupCheck. An error reading the
// setup says nothing: a claim is not refused for a lookup that failed.
func (s *Service) SetupGap(ctx context.Context) string {
	st, err := s.Status(ctx)
	if err != nil || st.CodeWorkReady() {
		return ""
	}
	return "This tenant has no repo in its workspace manifest and no code host connected, so there is nothing for a worker to clone or open a pull request on. " +
		"A person has to declare a repo (the board's Workspace page) and connect the GitHub App (its Git page) first. " +
		"The ticket stays in the queue. Do not retry: stop."
}
