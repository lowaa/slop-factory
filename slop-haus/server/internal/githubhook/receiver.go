package githubhook

import (
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/hookkit"
)

// maxBody bounds a delivery. A pull request event carries the PR's
// description and a review's body, tens of KB at most; this is read, and
// MACed, before anything is known about who sent it.
const maxBody = 1 << 20

// Receiver is the HTTP side of Receive: GitHub's delivery in, a short
// plain-text answer out. It is served on the webhook listener alone, not
// as a huma operation on the main API -- it is not part of the board's
// API, and the main listener's /hooks/ is Claude Code's.
type Receiver struct {
	svc    *Service
	logger *slog.Logger
}

func NewReceiver(svc *Service, logHandler slog.Handler) *Receiver {
	return &Receiver{svc: svc, logger: slog.New(logHandler)}
}

// Handler is the webhook listener's whole surface: the receive routes, and
// 404 for everything else.
func (rc *Receiver) Handler() http.Handler {
	return rc.HandlerBehind(nil)
}

// HandlerBehind is Handler with gate in front of the whole mux, so it sees
// every request to the listener, a 404 included. A nil gate is none.
func (rc *Receiver) HandlerBehind(gate hookkit.Gate) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+AppReceivePath, rc.serve)
	if gate == nil {
		return mux
	}
	return gate(mux)
}

// serve reads a delivery and answers for what ReceiveApp made of it.
func (rc *Receiver) serve(w http.ResponseWriter, r *http.Request) {
	body, err := hookkit.ReadBody(w, r, maxBody)
	if errors.Is(err, hookkit.ErrTooLarge) {
		http.Error(w, "delivery too large", http.StatusRequestEntityTooLarge)
		return
	}
	if err != nil {
		http.Error(w, "could not read delivery", http.StatusBadRequest)
		return
	}

	receipt, err := rc.svc.ReceiveApp(r.Context(), &Incoming{
		Event:       r.Header.Get("X-GitHub-Event"),
		DeliveryID:  r.Header.Get("X-GitHub-Delivery"),
		Signature:   r.Header.Get("X-Hub-Signature-256"),
		ContentType: r.Header.Get("Content-Type"),
		Body:        body,
	})
	switch {
	case errors.Is(err, ErrUnauthorized):
		// Debug, not info: anyone on the internet can make one of these,
		// and a log line per attempt is theirs to fill the disk with.
		rc.logger.DebugContext(r.Context(), "refused github delivery", "remote", r.RemoteAddr,
			"forwarded_for", r.Header.Get("X-Forwarded-For"))
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	case errors.Is(err, ErrBadDelivery):
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	case err != nil:
		rc.logger.ErrorContext(r.Context(), "github delivery failed", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if receipt.Duplicate {
		_, _ = io.WriteString(w, "duplicate: already handled\n")
		return
	}
	_, _ = io.WriteString(w, string(receipt.Outcome)+": "+receipt.Detail+"\n")
}
