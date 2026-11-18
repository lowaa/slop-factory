package intake

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/hookkit"
)

// Receiver is the HTTP side of Receive: a sender's delivery in, a short
// plain-text answer out. It is served on the intake listener alone, not on
// the board's API.
type Receiver struct {
	svc    *Service
	logger *slog.Logger
}

func NewReceiver(svc *Service, logHandler slog.Handler) *Receiver {
	return &Receiver{svc: svc, logger: slog.New(logHandler)}
}

// Handler is the intake listener's whole surface: the receive route, and
// 404 for everything else.
func (rc *Receiver) Handler() http.Handler {
	return rc.HandlerBehind(nil)
}

// HandlerBehind is Handler with gate in front of the whole mux, so it sees
// every request to the listener, a 404 included. A nil gate is none.
func (rc *Receiver) HandlerBehind(gate hookkit.Gate) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+ReceivePath, rc.receive)
	if gate == nil {
		return mux
	}
	return gate(mux)
}

func (rc *Receiver) receive(w http.ResponseWriter, r *http.Request) {
	// Limited on the read: nothing, not even the credential check, sees
	// more than MaxRequestBody.
	body, err := hookkit.ReadBody(w, r, MaxRequestBody)
	if errors.Is(err, hookkit.ErrTooLarge) {
		http.Error(w, "delivery too large", http.StatusRequestEntityTooLarge)
		return
	}
	if err != nil {
		http.Error(w, "could not read delivery", http.StatusBadRequest)
		return
	}

	receipt, err := rc.svc.Receive(r.Context(), &Incoming{
		SourceID:      r.PathValue("id"),
		Authorization: r.Header.Get("Authorization"),
		Signature:     r.Header.Get(SignatureHeader),
		DeliveryID:    r.Header.Get(DeliveryHeader),
		ContentType:   r.Header.Get("Content-Type"),
		Body:          body,
	})
	switch {
	case errors.Is(err, ErrUnauthorized):
		// Debug, not info: anyone who can reach the listener can make one
		// of these, and a log line per attempt is theirs to fill the disk
		// with.
		rc.logger.DebugContext(r.Context(), "refused intake delivery", "remote", r.RemoteAddr)
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	case errors.Is(err, ErrRateLimited):
		w.Header().Set("Retry-After", strconv.Itoa(60))
		http.Error(w, err.Error(), http.StatusTooManyRequests)
		return
	case err != nil:
		rc.logger.ErrorContext(r.Context(), "intake delivery failed", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	switch {
	case receipt.Duplicate:
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "duplicate: already handled\n")
	case receipt.Outcome == OutcomeRejected:
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, "rejected: "+receipt.Detail+"\n")
	default:
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, string(receipt.Outcome)+": "+receipt.Detail+"\n")
	}
}
