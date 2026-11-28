package state

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/uptrace/bun"

	clock "github.com/lowaa/slop-factory/go-clock"
	"github.com/lowaa/slop-factory/slop-bot/internal/ident"
	"github.com/lowaa/slop-factory/slop-bot/internal/sqlitex"
)

// Party is a message's sender or recipient: the user, or an agent.
type Party string

// UserParty is the user. No agent id may equal it (the agents table refuses
// one), so a Party is never ambiguous.
const UserParty Party = "user"

// AgentParty is the agent with id.
func AgentParty(id ident.AgentID) Party { return Party(id) }

// PartyOf is the party a caller sends as: who sent a message comes from the
// caller, never from an argument.
func PartyOf(p ident.Principal) Party {
	if p.IsUser() {
		return UserParty
	}
	return AgentParty(p.Agent)
}

// MessageKind says what a message is for.
type MessageKind string

const (
	KindTell MessageKind = "tell"
	// KindAsk is an agent's question to the user; see Messages.Ask.
	KindAsk MessageKind = "ask"
	// KindAnswer is the user's reply to an agent with open asks; see
	// Messages.Answer.
	KindAnswer MessageKind = "answer"
	KindNudge  MessageKind = "nudge"
	KindSystem MessageKind = "system"
)

// MessageStatus is how far a message has got to its recipient.
type MessageStatus string

const (
	StatusQueued    MessageStatus = "queued"
	StatusDelivered MessageStatus = "delivered"
	StatusRead      MessageStatus = "read"
)

// DeliveryPath is how a message reached its recipient. It is recorded for
// the audit trail; the delivery policy that picks one lives with the
// harness driver.
type DeliveryPath string

const (
	// DeliverySteer is mid-turn, where the protocol can steer.
	DeliverySteer DeliveryPath = "steer"
	// DeliveryTurn is as the next turn.
	DeliveryTurn DeliveryPath = "turn"
	// DeliveryHook is through a harness hook, e.g. PostToolUse.
	DeliveryHook DeliveryPath = "hook"
	// DeliveryAttach is shown to the person attaching or handing back.
	DeliveryAttach DeliveryPath = "attach"
	// DeliveryInbox is the recipient pulling it with `slop-bot inbox`.
	DeliveryInbox DeliveryPath = "inbox"
)

// Message is one inbox message.
type Message struct {
	ID           int64
	From         Party
	To           Party
	Kind         MessageKind
	Body         string
	CreatedAt    time.Time
	Status       MessageStatus
	DeliveryPath DeliveryPath
	DeliveredAt  *time.Time
	ReadAt       *time.Time
}

// NewMessage is what Enqueue and Answer need.
type NewMessage struct {
	From Party
	To   Party
	Kind MessageKind
	Body string
}

// NewAsk is an agent's question to the user.
type NewAsk struct {
	From           ident.AgentID
	Question       string
	Options        []string
	Recommendation string
}

// Ask is a question, with whether it is still open.
type Ask struct {
	Message
	Options        []string
	Recommendation string
	// ClosedAt is when the user answered it, or its agent ended.
	ClosedAt *time.Time
	// AnswerID is the answering message, or 0: for an ask closed because
	// its agent ended, and once pruning removes the answer.
	AnswerID int64
}

// LogQuery filters Messages.Log.
type LogQuery struct {
	// Party, when set, keeps the messages it sent or received.
	Party Party
	// BeforeID, when set, pages back: only messages older than it.
	BeforeID int64
	// Limit caps the page; 0 means DefaultLogLimit.
	Limit int
}

// DefaultLogLimit is a Log page when LogQuery.Limit is 0.
const DefaultLogLimit = 100

type messageCols struct {
	ID           int64  `bun:"id,pk,autoincrement"`
	Sender       string `bun:"sender"`
	Recipient    string `bun:"recipient"`
	Kind         string `bun:"kind"`
	Body         string `bun:"body"`
	CreatedAt    int64  `bun:"created_at"`
	Status       string `bun:"status"`
	DeliveryPath string `bun:"delivery_path"`
	DeliveredAt  *int64 `bun:"delivered_at"`
	ReadAt       *int64 `bun:"read_at"`
}

func (r *messageCols) message() Message {
	return Message{
		ID: r.ID, From: Party(r.Sender), To: Party(r.Recipient), Kind: MessageKind(r.Kind), Body: r.Body,
		CreatedAt: fromMillis(r.CreatedAt), Status: MessageStatus(r.Status), DeliveryPath: DeliveryPath(r.DeliveryPath),
		DeliveredAt: fromMillisPtr(r.DeliveredAt), ReadAt: fromMillisPtr(r.ReadAt),
	}
}

type messageRow struct {
	bun.BaseModel `bun:"table:messages"`
	messageCols
}

type askRow struct {
	bun.BaseModel `bun:"table:asks"`

	MessageID       int64  `bun:"message_id,pk"`
	Options         string `bun:"options"`
	Recommendation  string `bun:"recommendation"`
	ClosedAt        *int64 `bun:"closed_at"`
	AnswerMessageID *int64 `bun:"answer_message_id"`
}

type askJoinRow struct {
	messageCols
	Options         string `bun:"options"`
	Recommendation  string `bun:"recommendation"`
	ClosedAt        *int64 `bun:"closed_at"`
	AnswerMessageID *int64 `bun:"answer_message_id"`
}

func (r *askJoinRow) ask() (*Ask, error) {
	a := &Ask{Message: r.message(), Recommendation: r.Recommendation, ClosedAt: fromMillisPtr(r.ClosedAt)}
	if r.AnswerMessageID != nil {
		a.AnswerID = *r.AnswerMessageID
	}
	if err := json.Unmarshal([]byte(r.Options), &a.Options); err != nil {
		return nil, fmt.Errorf("state: options of ask %d: %w", r.ID, err)
	}
	return a, nil
}

// Messages is the inbox: tells, asks and their answers, nudges and system
// notices, to agents and to the user.
type Messages struct {
	db    *sqlitex.DB
	clock clock.Clock
}

// Enqueue queues a message for delivery. Asks and answers go through Ask
// and Answer, which keep their bookkeeping in step.
func (m *Messages) Enqueue(ctx context.Context, n *NewMessage) (*Message, error) {
	if n.Kind == KindAsk || n.Kind == KindAnswer {
		return nil, fmt.Errorf("state: an %s goes through Messages.Ask or Messages.Answer", n.Kind)
	}
	return m.insert(ctx, m.db, n)
}

// Ask queues an agent's question to the user, open until answered.
func (m *Messages) Ask(ctx context.Context, n *NewAsk) (*Ask, error) {
	// Never nil, so no options reads back the same as it was written: [].
	options := append([]string{}, n.Options...)
	opts, err := json.Marshal(options)
	if err != nil {
		return nil, fmt.Errorf("state: encoding ask options: %w", err)
	}
	var ask *Ask
	err = m.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		msg, err := m.insert(ctx, tx, &NewMessage{From: AgentParty(n.From), To: UserParty, Kind: KindAsk, Body: n.Question})
		if err != nil {
			return err
		}
		if _, err := tx.NewInsert().Model(&askRow{MessageID: msg.ID, Options: string(opts), Recommendation: n.Recommendation}).Exec(ctx); err != nil {
			return fmt.Errorf("state: recording ask: %w", err)
		}
		ask = &Ask{Message: *msg, Options: options, Recommendation: n.Recommendation}
		return nil
	})
	return ask, err
}

// Answer queues the user's reply to n.To, an agent, and closes every ask it
// has open as answered by it, in one transaction. It returns how many asks
// it answered. Asks go to the user, so only the user answers them. Whether
// a message is an answer (the user telling an agent that is waiting on them)
// is the caller's call; this keeps the record straight.
func (m *Messages) Answer(ctx context.Context, n *NewMessage) (*Message, int64, error) {
	if n.From != UserParty || n.To == UserParty {
		return nil, 0, errors.New("state: an answer goes from the user to an agent")
	}
	var (
		msg      *Message
		answered int64
	)
	err := m.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		var err error
		msg, err = m.insert(ctx, tx, &NewMessage{From: n.From, To: n.To, Kind: KindAnswer, Body: n.Body})
		if err != nil {
			return err
		}
		answered, err = closeAsksOf(ctx, tx, n.To, toMillis(msg.CreatedAt), &msg.ID)
		return err
	})
	if err != nil {
		return nil, 0, err
	}
	return msg, answered, nil
}

// Pending returns the messages queued for to and not yet delivered, oldest
// first: what the delivery policy pushes next.
func (m *Messages) Pending(ctx context.Context, to Party) ([]Message, error) {
	return m.list(ctx, func(q *bun.SelectQuery) *bun.SelectQuery {
		return q.Where("recipient = ?", string(to)).Where("status = ?", string(StatusQueued)).Order("id")
	})
}

// Unread returns to's messages not yet read, delivered or not, oldest first:
// what `slop-bot inbox` shows.
func (m *Messages) Unread(ctx context.Context, to Party) ([]Message, error) {
	return m.list(ctx, func(q *bun.SelectQuery) *bun.SelectQuery {
		return q.Where("recipient = ?", string(to)).Where("status <> ?", string(StatusRead)).Order("id")
	})
}

// MarkDelivered records that a queued message reached its recipient by
// path. A message already delivered or read is ErrConflict, so two
// delivery paths can't both claim it.
func (m *Messages) MarkDelivered(ctx context.Context, id int64, path DeliveryPath) error {
	res, err := m.db.NewUpdate().Table("messages").
		Set("status = ?", string(StatusDelivered)).Set("delivery_path = ?", string(path)).
		Set("delivered_at = ?", toMillis(m.clock.Now())).
		Where("id = ?", id).Where("status = ?", string(StatusQueued)).Exec(ctx)
	if err != nil {
		return fmt.Errorf("state: marking message %d delivered: %w", id, err)
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return nil
	}
	var exists bool
	if exists, err = m.db.NewSelect().Table("messages").Where("id = ?", id).Exists(ctx); err != nil {
		return fmt.Errorf("state: reading message %d: %w", id, err)
	}
	if !exists {
		return fmt.Errorf("%w: message %d", ErrNotFound, id)
	}
	return fmt.Errorf("%w: message %d was already delivered", ErrConflict, id)
}

// MarkRead marks to's messages ids read and returns how many changed. Ids
// that aren't to's, or are already read, are skipped, so one recipient
// can't mark another's inbox. A message read before any push counts as
// delivered through the inbox.
func (m *Messages) MarkRead(ctx context.Context, to Party, ids []int64) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	at := toMillis(m.clock.Now())
	res, err := m.db.NewUpdate().Table("messages").
		Set("status = ?", string(StatusRead)).Set("read_at = ?", at).
		Set("delivered_at = coalesce(delivered_at, ?)", at).
		Set("delivery_path = CASE WHEN delivery_path = '' THEN ? ELSE delivery_path END", string(DeliveryInbox)).
		Where("recipient = ?", string(to)).Where("id IN (?)", bun.List(ids)).Where("status <> ?", string(StatusRead)).
		Exec(ctx)
	if err != nil {
		return 0, fmt.Errorf("state: marking messages of %s read: %w", to, err)
	}
	n, err := res.RowsAffected()
	return n, err
}

// Log returns a page of the message history, oldest first within the page:
// the newest q.Limit messages before q.BeforeID.
func (m *Messages) Log(ctx context.Context, q *LogQuery) ([]Message, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = DefaultLogLimit
	}
	msgs, err := m.list(ctx, func(sq *bun.SelectQuery) *bun.SelectQuery {
		if q.Party != "" {
			sq = sq.Where("sender = ? OR recipient = ?", string(q.Party), string(q.Party))
		}
		if q.BeforeID > 0 {
			sq = sq.Where("id < ?", q.BeforeID)
		}
		return sq.OrderExpr("id DESC").Limit(int64(limit))
	})
	slices.Reverse(msgs)
	return msgs, err
}

// CountBetween counts the messages either of a and b sent the other since
// since: the loop guard's back-and-forth cap.
func (m *Messages) CountBetween(ctx context.Context, a, b Party, since time.Time) (int64, error) {
	n, err := m.db.NewSelect().Table("messages").
		Where("((sender = ? AND recipient = ?) OR (sender = ? AND recipient = ?))", string(a), string(b), string(b), string(a)).
		Where("created_at >= ?", toMillis(since)).Count(ctx)
	if err != nil {
		return 0, fmt.Errorf("state: counting messages between %s and %s: %w", a, b, err)
	}
	return n, nil
}

// CountSentBy counts the messages from sent since since: the loop guard's
// per-agent send rate.
func (m *Messages) CountSentBy(ctx context.Context, from Party, since time.Time) (int64, error) {
	n, err := m.db.NewSelect().Table("messages").
		Where("sender = ?", string(from)).Where("created_at >= ?", toMillis(since)).Count(ctx)
	if err != nil {
		return 0, fmt.Errorf("state: counting messages from %s: %w", from, err)
	}
	return n, nil
}

// OpenAsks returns the unanswered asks, oldest first: `slop-bot asks`.
func (m *Messages) OpenAsks(ctx context.Context) ([]*Ask, error) {
	return m.asks(ctx, "")
}

// OpenAsksFrom returns agent's unanswered asks, oldest first.
func (m *Messages) OpenAsksFrom(ctx context.Context, agent ident.AgentID) ([]*Ask, error) {
	return m.asks(ctx, AgentParty(agent))
}

func (m *Messages) asks(ctx context.Context, from Party) ([]*Ask, error) {
	var rows []askJoinRow
	q := m.db.NewSelect().TableExpr("messages AS m").Join("JOIN asks AS a ON a.message_id = m.id").
		ColumnExpr("m.*").ColumnExpr("a.options, a.recommendation, a.closed_at, a.answer_message_id").
		Where("a.closed_at IS NULL")
	if from != "" {
		q = q.Where("m.sender = ?", string(from))
	}
	if err := q.Order("m.id").Scan(ctx, &rows); err != nil {
		return nil, fmt.Errorf("state: listing open asks: %w", err)
	}
	out := make([]*Ask, len(rows))
	for i := range rows {
		a, err := rows[i].ask()
		if err != nil {
			return nil, err
		}
		out[i] = a
	}
	return out, nil
}

func (m *Messages) insert(ctx context.Context, db bun.IDB, n *NewMessage) (*Message, error) {
	row := &messageRow{messageCols: messageCols{
		Sender: string(n.From), Recipient: string(n.To), Kind: string(n.Kind), Body: n.Body,
		CreatedAt: toMillis(m.clock.Now()), Status: string(StatusQueued),
	}}
	if _, err := db.NewInsert().Model(row).Exec(ctx); err != nil {
		return nil, fmt.Errorf("state: queueing %s from %s to %s: %w", n.Kind, n.From, n.To, err)
	}
	msg := row.message()
	return &msg, nil
}

func (m *Messages) list(ctx context.Context, where func(*bun.SelectQuery) *bun.SelectQuery) ([]Message, error) {
	var rows []messageRow
	if err := m.db.NewSelect().Model(&rows).Apply(where).Scan(ctx); err != nil {
		return nil, fmt.Errorf("state: listing messages: %w", err)
	}
	out := make([]Message, len(rows))
	for i := range rows {
		out[i] = rows[i].message()
	}
	return out, nil
}

// closeAsksOf closes every open ask from, at at, answered by answerID or,
// when nil, by nobody: its agent ended, so nobody is left to read an answer.
func closeAsksOf(ctx context.Context, db bun.IDB, from Party, at int64, answerID *int64) (int64, error) {
	res, err := db.NewUpdate().Table("asks").
		Set("closed_at = ?", at).Set("answer_message_id = ?", answerID).
		Where("closed_at IS NULL").
		Where("message_id IN (SELECT id FROM messages WHERE sender = ? AND kind = ?)", string(from), string(KindAsk)).
		Exec(ctx)
	if err != nil {
		return 0, fmt.Errorf("state: closing asks of %s: %w", from, err)
	}
	return res.RowsAffected()
}
