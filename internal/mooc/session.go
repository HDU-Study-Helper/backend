package mooc

import (
	"context"
	"net/http"
	"net/url"
	"strconv"

	"academic/backend/internal/contract"
)

var _ contract.SessionRefresher = (*adapter)(nil)

// RefreshSession exchanges only the existing login cookies, then proves that the
// resulting jar can read the official learner course list. Authorized is local
// state, not evidence that the upstream session is still logged in.
func (a *adapter) RefreshSession(ctx context.Context, scope contract.Scope, session contract.Session) (contract.Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s, h, err := a.restore(scope, session)
	if err != nil {
		return nil, err
	}
	result, err := refreshCoursePanels(ctx, h, 1, 1)
	if err != nil {
		return nil, err
	}
	// This exact null/zero-total response is the observed expired login shape.
	// Other absent or malformed lists remain parse errors; no null becomes [].
	if unauthenticatedCoursePanels(result, 1) {
		return nil, contract.ErrAuthRequired
	}
	if _, _, _, err = parseCoursePage(result, 1, 1); err != nil {
		return nil, err
	}
	s.Authorized = true
	s.PollKey = ""
	s.QRURL = ""
	return save(s, h)
}

func coursePanelForm(typ, page int) url.Values {
	return url.Values{"type": {"30"}, "p": {strconv.Itoa(page)}, "psize": {"8"}, "courseType": {strconv.Itoa(typ)}}
}

// Keep the exchange separate from Sync: the host owns the single authentication
// refresh/retry, while legacy stale-panel recovery reuses this bounded handshake.
func refreshCoursePanels(ctx context.Context, h *http.Client, typ, page int) (map[string]any, error) {
	if _, err := request(ctx, h, http.MethodGet, "/home.htm", origin+"/", nil); err != nil {
		return nil, err
	}
	return rpc(ctx, h, "learnerCourseRpcBean.getMyLearnedCoursePanelList", origin+"/", coursePanelForm(typ, page))
}
