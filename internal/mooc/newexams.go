package mooc

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"

	"academic/backend/internal/contract"
)

// readNewExams follows the observed official frontend form request. Only the
// empty list has a verified response shape; populated lists remain unsupported.
func readNewExams(ctx context.Context, h *http.Client, co course) ([]contract.Record, error) {
	u, _ := url.Parse(origin)
	csrf := ""
	for _, cookie := range h.Jar.Cookies(u) {
		if cookie.Name == "NTESSTUDYSI" {
			csrf = cookie.Value
		}
	}
	if csrf == "" {
		return nil, contract.ErrAuthRequired
	}
	body, err := request(ctx, h, http.MethodPost, "/web/j/courseBean.getNewExamList.rpc?csrfKey="+url.QueryEscape(csrf), co.referer(), url.Values{"termId": {sourceTerm(co)}})
	if err != nil {
		return nil, err
	}
	return parseNewExams(body)
}

func parseNewExams(body []byte) ([]contract.Record, error) {
	d := json.NewDecoder(bytes.NewReader(body))
	d.UseNumber()
	var env map[string]any
	if d.Decode(&env) != nil {
		return nil, contract.ErrParse
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return nil, contract.ErrParse
	}
	code, ok := env["code"].(json.Number)
	if !ok {
		return nil, contract.ErrParse
	}
	if code.String() == "1" || code.String() == "-1007" {
		return nil, contract.ErrAuthRequired
	}
	if code.String() != "0" {
		return nil, contract.ErrUpstream
	}
	// Verified SPOC route returns explicit null; official UI uses
	// null!=items && items.length, rendering null and [] as the same empty state.
	if value, present := env["result"]; present && value == nil {
		return []contract.Record{}, nil
	}
	rows, ok := env["result"].([]any)
	if !ok {
		return nil, contract.ErrParse
	}
	if len(rows) != 0 {
		return nil, contract.ErrUnsupported
	}
	return []contract.Record{}, nil
}
