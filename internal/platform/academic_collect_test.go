package platform

import (
	"academic/backend/internal/contract"
	"academic/backend/internal/model"
	"context"
	"testing"
)

// MOOC discussion is an account/term stream, unlike Chaoxing's optional class partition port.
type academicCollectAdapter struct{ calls []contract.Resource }

type academicCollectRepo struct{ *collectRepo }

func (r *academicCollectRepo) Get(_ context.Context, s model.DWSScope) (model.PlatformConnection, error) {
	if r.state.Connection.UserID != s.UserID || r.state.Connection.ID != s.ConnectionID {
		return model.PlatformConnection{}, model.ErrNotFound
	}
	return r.state.Connection, nil
}

func (a *academicCollectAdapter) Describe() contract.Descriptor {
	return contract.Descriptor{Source: "mooc", Resources: []contract.Resource{contract.Chapter, contract.Lesson, contract.Discussion}}
}
func (*academicCollectAdapter) BeginAuth(context.Context, contract.Scope) (contract.AuthResult, error) {
	return contract.AuthResult{}, contract.ErrUnsupported
}
func (*academicCollectAdapter) PollAuth(context.Context, contract.Scope, contract.Session) (contract.AuthResult, error) {
	return contract.AuthResult{}, contract.ErrUnsupported
}
func (a *academicCollectAdapter) Sync(_ context.Context, _ contract.Scope, _ contract.Session, r contract.Resource, _ string) (contract.Page, error) {
	a.calls = append(a.calls, r)
	return contract.Page{Records: []contract.Record{{Kind: r, ExternalID: string(r)}}}, nil
}
func TestAcademicCollectionSupportsUnpartitionedMOOCDiscussion(t *testing.T) {
	a := &academicCollectAdapter{}
	s, r := collectFixture(t, a)
	r.state.Connection.Source = "mooc"
	s.repo = &academicCollectRepo{r}
	// collectFixture chooses only the adapter's descriptor source; source owner remains trusted host state.
	if err := s.collectOnce(context.Background(), 10, 5); err != nil {
		t.Fatal(err)
	}
	if len(a.calls) != 3 || a.calls[2] != contract.Discussion {
		t.Fatalf("new source resources not drained: %v", a.calls)
	}
	if err := s.Sync(context.Background(), model.DWSScope{UserID: "owner", ConnectionID: "connection"}, contract.Discussion); err != nil {
		t.Fatal(err)
	}
}
