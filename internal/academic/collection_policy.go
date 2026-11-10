package academic

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"

	"academic/backend/internal/httpkit"
	"academic/backend/internal/model"
)

func (s *Service) policyRepository(w http.ResponseWriter, r *http.Request) (model.CollectionPolicyRepository, bool) {
	if r.URL.RawQuery != "" {
		policyFail(w, r, model.ErrInvalidArgument)
		return nil, false
	}
	repo, ok := s.repo.(model.CollectionPolicyRepository)
	if !ok {
		policyFail(w, r, model.ErrUnsupported)
	}
	return repo, ok
}

func (s *Service) collectionPolicy(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.policyRepository(w, r)
	if !ok {
		return
	}
	u, _ := httpkit.CurrentUser(r.Context())
	policy, err := repo.CollectionPolicy(r.Context(), u.ID)
	if err != nil {
		policyFail(w, r, err)
		return
	}
	writePolicy(w, policy)
}

// decodePolicy uses a larger route-local bound than DecodeJSON's 16 KiB because
// a full replacement may contain 2,000 IDs. Field names are exact and unique.
func decodePolicy(w http.ResponseWriter, r *http.Request) (int, []string, bool) {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		httpkit.Error(w, r, 415, "unsupported_media_type", "请使用 application/json")
		return 0, nil, false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 256<<10)
	dec := json.NewDecoder(r.Body)
	token, err := dec.Token()
	if err != nil || token != json.Delim('{') {
		policyFail(w, r, model.ErrInvalidArgument)
		return 0, nil, false
	}
	fields := map[string]json.RawMessage{}
	for dec.More() {
		key, err := dec.Token()
		name, ok := key.(string)
		if err != nil || !ok || (name != "version" && name != "offering_ids") || fields[name] != nil {
			policyFail(w, r, model.ErrInvalidArgument)
			return 0, nil, false
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			policyFail(w, r, model.ErrInvalidArgument)
			return 0, nil, false
		}
		fields[name] = raw
	}
	if _, err := dec.Token(); err != nil {
		policyFail(w, r, model.ErrInvalidArgument)
		return 0, nil, false
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		policyFail(w, r, model.ErrInvalidArgument)
		return 0, nil, false
	}
	var version *int
	var ids []string
	if json.Unmarshal(fields["version"], &version) != nil || version == nil || *version < 0 || json.Unmarshal(fields["offering_ids"], &ids) != nil || ids == nil || len(ids) > 2000 {
		policyFail(w, r, model.ErrInvalidArgument)
		return 0, nil, false
	}
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if id == "" || len(id) > 128 || strings.TrimSpace(id) != id || strings.ContainsAny(id, "\x00\r\n\t") || seen[id] {
			policyFail(w, r, model.ErrInvalidArgument)
			return 0, nil, false
		}
		seen[id] = true
	}
	return *version, ids, true
}

func (s *Service) saveCollectionPolicy(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.policyRepository(w, r)
	if !ok {
		return
	}
	version, ids, ok := decodePolicy(w, r)
	if !ok {
		return
	}
	u, _ := httpkit.CurrentUser(r.Context())
	policy, err := repo.SaveCollectionPolicy(r.Context(), u.ID, version, ids)
	if err != nil {
		policyFail(w, r, err)
		return
	}
	writePolicy(w, policy)
}

func writePolicy(w http.ResponseWriter, policy model.CollectionPolicy) {
	if policy.Excluded == nil {
		policy.Excluded = []model.CollectionExclusion{}
	}
	httpkit.WriteJSON(w, http.StatusOK, policy)
}

func policyFail(w http.ResponseWriter, r *http.Request, err error) {
	status, code, message := 500, "internal", "无法读取或保存课程采集黑名单，请稍后重试"
	switch {
	case errors.Is(err, model.ErrConflict):
		status, code, message = 409, "conflict", "课程采集黑名单已在其他页面更新，请重新读取后再保存"
	case errors.Is(err, model.ErrInvalidArgument):
		status, code, message = 400, "invalid_argument", "黑名单参数无效：须提供非负整数 version 和最多 2000 个不重复的 offering_ids，不接受其他字段或查询参数"
	case errors.Is(err, model.ErrNotFound):
		status, code, message = 404, "not_found", "所选课程不存在、连接不可用或不属于当前用户，请重新读取课程目录"
	case errors.Is(err, model.ErrUnsupported):
		status, code, message = 422, "unsupported", "当前服务尚不支持课程采集黑名单"
	}
	httpkit.Error(w, r, status, code, message)
}
