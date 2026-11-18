package attachment

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/secretscan"
)

var (
	// ErrNotPermitted answers a worker attaching to a ticket it does not hold.
	ErrNotPermitted = errors.New("attachment: a worker may attach files only to the ticket it holds; only a console or conductor may attach to another")
	// ErrSecretInFilename is a filename shaped like a credential. Image bytes
	// cannot be scanned, so the filename is the one agent-written text.
	ErrSecretInFilename = errors.New("attachment: the filename looks like a credential")
)

// Holder says which ticket an agent holds, if any.
type Holder interface {
	HeldTicket(ctx context.Context, agentID uuid.UUID) (ticketID int64, held bool, err error)
}

// AgentCaller is the caller an agent's token stands for.
func AgentCaller(who authn.Identity) Caller {
	return Caller{Kind: UploaderAgent, ID: who.AgentID()}
}

// AgentUploadToTicket attaches a file for an agent. A console or conductor,
// which acts for its owner, may attach to any ticket of its tenant; a worker
// only to the ticket it holds, so a session that read hostile text cannot
// scatter files across the board.
func (s *Service) AgentUploadToTicket(ctx context.Context, who authn.Identity, ticketID int64, in *Upload) (Attachment, error) {
	if err := s.mayAttach(ctx, who, ticketID); err != nil {
		return Attachment{}, err
	}
	if found := secretscan.Check(in.Filename); found != nil {
		return Attachment{}, errors.Join(ErrSecretInFilename, found)
	}
	return s.UploadToTicket(ctx, AgentCaller(who), ticketID, in)
}

func (s *Service) mayAttach(ctx context.Context, who authn.Identity, ticketID int64) error {
	if ownerBound(who) {
		return nil
	}
	if s.holder == nil {
		return ErrNotPermitted
	}
	held, ok, err := s.holder.HeldTicket(ctx, who.AgentID())
	if err != nil {
		return err
	}
	if !ok || held != ticketID {
		return ErrNotPermitted
	}
	return nil
}

func ownerBound(who authn.Identity) bool {
	if c, ok := who.(interface{ IsConsole() bool }); ok && c.IsConsole() {
		return true
	}
	c, ok := who.(interface{ IsConductor() bool })
	return ok && c.IsConductor()
}
