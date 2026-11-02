package contract

import "context"

type CoursePartition struct {
	CourseID string
	ClassID  string
}

// ReplyUserReader reads participant identities only, never response bodies/files.
type ReplyUserReader interface {
	DiscussionReplyUsers(context.Context, Scope, Session, string, string, string) (Page, error)
}

// DiscussionPartitioner keeps course failures/checkpoints independent.
type DiscussionPartitioner interface {
	DiscussionCourses(context.Context, Scope, Session) ([]CoursePartition, error)
	SyncDiscussionCourse(context.Context, Scope, Session, string, string, string) (Page, error)
}
