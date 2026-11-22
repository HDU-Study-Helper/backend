// Package continuation describes host-owned routing state. Nothing in a model
// response or a RunRequest can select a prior run, channel, owner or parent.
package continuation

import (
	f "academic/agent/framework"
	"context"
	"time"
)

const ChA = "ChA"
const ChB = "ChB"

type Source struct{ Owner, Channel, Key string }
type sourceKey struct{}

func WithSource(ctx context.Context, owner, channel, key string) context.Context {
	return context.WithValue(ctx, sourceKey{}, Source{owner, channel, key})
}
func Current(ctx context.Context) (Source, bool) {
	s, ok := ctx.Value(sourceKey{}).(Source)
	return s, ok
}
func (s Source) Valid(owner string) bool {
	return owner != "" && s.Owner == owner && (s.Channel == ChA || s.Channel == ChB) && len(s.Key) > 0 && len(s.Key) <= 512
}

// ControlLink is host-originated run-control bookkeeping. It is never read from
// model input and is deliberately not reused by scheduled jobs.
type ControlLink struct{ Owner, Batch, Item string }
type linkKey struct{}

func WithControl(ctx context.Context, l ControlLink) context.Context {
	return context.WithValue(ctx, linkKey{}, l)
}
func Control(ctx context.Context, owner string) (ControlLink, bool) {
	l, ok := ctx.Value(linkKey{}).(ControlLink)
	return l, ok && l.Owner == owner && l.Batch != ""
}

type answerKey struct{}

func WithAnswerRef(ctx context.Context, owner, id string) context.Context {
	return context.WithValue(ctx, answerKey{}, [2]string{owner, "question_answer:" + id})
}
func UserMessageRef(ctx context.Context, owner string) string {
	x, _ := ctx.Value(answerKey{}).([2]string)
	if x[0] != owner {
		return ""
	}
	return x[1]
}

type Archive struct {
	Control            ControlLink
	Source             Source
	Role, Path, Target string
	FirstAsOf          time.Time
	Transcript         f.Transcript
}
type Option struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}
type Interaction struct {
	Type     string   `json:"type"`
	Question string   `json:"question"`
	Options  []Option `json:"options"`
}
type Question struct {
	Interaction
	ID      string `json:"question_id"`
	Text    string `json:"text"`
	Channel string `json:"channel"`
	Key     string `json:"source_key"`
	Role    string `json:"role"`
	RunID   string `json:"run_id"`
}

// CommitBinding lets the staging store include a validated Learner's question,
// archive and silent notice in the same transaction as its known work. Values
// are host-only; selected target comes from the durable stage receipt.
type CommitBinding func(workflowID string, receipt []byte) (Archive, *Question)
type commitKey struct{}

func WithCommitBinding(ctx context.Context, fn CommitBinding) context.Context {
	return context.WithValue(ctx, commitKey{}, fn)
}
func Binding(ctx context.Context) CommitBinding {
	fn, _ := ctx.Value(commitKey{}).(CommitBinding)
	return fn
}

type Store interface {
	SaveContinuation(context.Context, Archive, *Question) error
	LoadContinuation(context.Context, string, string) (Archive, error)
	PendingContinuations(context.Context, Source) ([]Question, error)
	CancelContinuations(context.Context, Source) error
	ClaimContinuation(context.Context, Source, string, string) (Question, *f.RunResult, error)
	FinishContinuation(context.Context, Source, string, f.RunResult) error
}
