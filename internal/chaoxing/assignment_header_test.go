package chaoxing

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"academic/backend/internal/contract"
)

func TestAssignmentHeaderMetadataSourceTimeEvidence(t *testing.T) {
	for _, tc := range []struct {
		name, start, end, precision, reason, label, separator string
	}{
		{"user exact missing year", "11-04 09:30", "11-08 23:59", "unknown", "missing_year", "作答时间", "至"},
		{"explicit full year", "2026-11-04 09:30", "2026-11-08 23:59", "minute", "", "作答时间", "至"},
		{"alternate label and separator", "2026/11/04 09:30:00", "2026/11/08 23:59:00", "second", "", "答题时间", "~"},
		{"date only", "2026年11月04日", "2026年11月08日", "day", "", "作答时间", "～"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := tc.start + tc.separator + tc.end
			body := `<div class="ceyan_top"><h2>2026 第二章第三次作业20261102</h2><p>题量:10 满分:100 作答时间:` + raw + `</p></div>`
			if tc.label != "作答时间" {
				body = strings.ReplaceAll(body, "作答时间", tc.label)
			}
			fields, err := assignmentHeaderMetadata([]byte(body))
			if err != nil {
				t.Fatal(err)
			}
			if fields["answer_time_raw"] != raw || fields["answer_time_source"] != "assignment_header:"+tc.label || fields["question_count"] != 10 || fields["full_score"] != float64(100) || fields["time_status"] != "provided" {
				t.Fatal(fields)
			}
			for name, wantRaw := range map[string]string{"start_at": tc.start, "end_at": tc.end, "deadline": tc.end} {
				if fields[name+"_raw"] != wantRaw || fields[name+"_source"] != "assignment_header:"+tc.label || fields[name+"_precision"] != tc.precision {
					t.Fatalf("%s evidence: %#v", name, fields)
				}
				if tc.reason != "" && fields[name+"_reason"] != tc.reason {
					t.Fatal(fields)
				}
				if tc.reason == "" && fields[name+"_reason"] != nil {
					t.Fatal(fields)
				}
			}
			for _, key := range []string{"published_at_raw", "published_at_source", "title", "body", "content", "body_status", "coverage"} {
				if _, exists := fields[key]; exists {
					t.Fatalf("invented or out-of-scope field %s: %#v", key, fields)
				}
			}
		})
	}
}

func TestAssignmentHeaderStartOnly(t *testing.T) {
	for _, label := range []string{"开始时间", "作答时间", "答题时间"} {
		fields, err := assignmentHeaderMetadata([]byte(`<div class="detailsHead"><p>` + label + `：10-28 09:35</p></div>`))
		if err != nil || fields["start_at_raw"] != "10-28 09:35" || fields["start_at_source"] != "assignment_header:"+label || fields["start_at_reason"] != "missing_year" {
			t.Fatal(label, fields, err)
		}
		for _, key := range []string{"end_at_raw", "deadline_raw", "published_at_raw"} {
			if _, ok := fields[key]; ok {
				t.Fatalf("start-only header emitted %s", key)
			}
		}
	}
}

func TestAssignmentHeaderActualMarkingShell(t *testing.T) {
	fields, err := assignmentHeaderMetadata([]byte(`<div id="fanyaMarking" class="fanyaMarking TiMu"><div class="fanyaMarking_left whiteBg"><div class="borderBom padBom20 detailsHead"><h2 class="mark_title">2026 Homework</h2><div class="infoHead fl"><span>题量: 10</span><span>满分: 100</span><div><p class="rightBord">作答时间: 11-04 09:30 至 11-08 23:59</p></div></div></div><div class="TiMu"><div class="infoHead"><p>作答时间: 01-01至01-02</p></div></div></div></div>`))
	if err != nil || fields["deadline_raw"] != "11-08 23:59" || fields["question_count"] != 10 {
		t.Fatal(fields, err)
	}
}

func TestAssignmentHeaderMetadataNestedLabelsAndVisibleOnly(t *testing.T) {
	body := `<div id="work-header">
<h1>2026 第二章第三次作业20261102</h1>
<p><span>题量：</span><span>10</span><span>满分：</span><strong>100.5</strong></p>
<div><span>作答时间：</span><span>11-04 09:30</span><span>至</span><span>11-08 23:59</span></div>
<script>作答时间：2026-01-01至2026-01-02 SECRET_SCRIPT</script>
<input type="hidden" value="题量：99 作答时间：2026-01-01至2026-01-02 SECRET_INPUT">
<div hidden><p>作答时间：2026-01-01至2026-01-02</p></div>
<div aria-hidden="true"><p>作答时间：2026-01-01至2026-01-02</p></div>
<div style="display: none"><p>题量：99</p></div>
<div style="visibility: hidden"><p>题量：99</p></div>
<div class="hidden"><p>题量：99</p></div>
</div>
<div class="question-list"><div class="ceyan_top"><p>题量：99 满分：999 作答时间：2026-01-01至2026-01-02 SECRET_QUESTION</p></div></div>
<div class="ceyan_top"><p>作答时间：2026-01-01至2026-01-02</p></div>`
	fields, err := assignmentHeaderMetadata([]byte(body))
	if err != nil || fields["question_count"] != 10 || fields["full_score"] != 100.5 || fields["answer_time_raw"] != "11-04 09:30 至 11-08 23:59" || fields["deadline_reason"] != "missing_year" {
		t.Fatal(fields, err)
	}
	wire, err := json.Marshal(fields)
	if err != nil || strings.Contains(string(wire), "SECRET") || strings.Contains(string(wire), "2026-01") {
		t.Fatal("non-header content escaped", string(wire), err)
	}
}

func TestAssignmentHeaderMetadataDuplicateAndMixedYearEvidence(t *testing.T) {
	fields, err := assignmentHeaderMetadata([]byte(`<div class="work-header"><p>作答时间：2026-11-04 09:30至11-08 23:59</p><p>题量：10</p><p>题量：10</p><p>答题时间：2026-11-04 09:30~11-08 23:59</p></div>`))
	if err != nil || fields["start_at_precision"] != "minute" || fields["deadline_precision"] != "unknown" || fields["deadline_reason"] != "missing_year" || fields["deadline_raw"] != "11-08 23:59" {
		t.Fatal(fields, err)
	}
}

func TestAssignmentHeaderMetadataFailsClosed(t *testing.T) {
	validRange := "11-04 09:30至11-08 23:59"
	for _, tc := range []struct{ name, body string }{
		{"empty", ""},
		{"no known header", `<div><p>作答时间：` + validRange + `</p></div>`},
		{"title and numbers only", `<div class="ceyan_top"><h1>2026 第二章第三次作业20261102</h1><p>题量：10 满分：100</p></div>`},
		{"unlabelled range", `<div class="ceyan_top"><p>` + validRange + `</p></div>`},
		{"label mentioned in prose", `<div class="ceyan_top"><p>请分析作答时间：` + validRange + `</p></div>`},
		{"question body only", `<div class="question"><div class="ceyan_top"><p>作答时间：` + validRange + `</p></div></div>`},
		{"exam body only", `<div id="examPaper"><div class="work-header"><p>作答时间：` + validRange + `</p></div></div>`},
		{"answer body only", `<div class="answer-content"><div class="work-header"><p>作答时间：` + validRange + `</p></div></div>`},
		{"header after body", `<div class="question-list">Question</div><div class="ceyan_top"><p>作答时间：` + validRange + `</p></div>`},
		{"script only", `<div class="ceyan_top"><script>作答时间：` + validRange + `</script></div>`},
		{"hidden input only", `<div class="ceyan_top"><input value="作答时间：` + validRange + `"></div>`},
		{"hidden header", `<div class="ceyan_top" hidden><p>作答时间：` + validRange + `</p></div>`},
		{"conflicting range", `<div class="ceyan_top"><p>作答时间：` + validRange + `</p><p>作答时间：11-04 09:30至11-09 23:59</p></div>`},
		{"conflicting aliases", `<div class="ceyan_top"><p>作答时间：` + validRange + `</p><p>答题时间：11-04 09:30至11-09 23:59</p></div>`},
		{"conflicting count", `<div class="ceyan_top"><p>作答时间：` + validRange + `</p><p>题量：10</p><p>题量：11</p></div>`},
		{"conflicting score", `<div class="ceyan_top"><p>作答时间：` + validRange + `</p><p>满分：100</p><p>满分：101</p></div>`},
		{"range suffix", `<div class="ceyan_top"><p>作答时间：` + validRange + ` 解答问题</p></div>`},
		{"range prefix", `<div class="ceyan_top"><p>作答时间：从` + validRange + `</p></div>`},
		{"range unsupported separator", `<div class="ceyan_top"><p>作答时间：11-04 09:30 - 11-08 23:59</p></div>`},
		{"invalid month", `<div class="ceyan_top"><p>作答时间：13-04 09:30至11-08 23:59</p></div>`},
		{"invalid day", `<div class="ceyan_top"><p>作答时间：2026-02-29 09:30至2026-03-01 23:59</p></div>`},
		{"invalid time", `<div class="ceyan_top"><p>作答时间：11-04 25:30至11-08 23:59</p></div>`},
		{"oversize line", `<div class="ceyan_top"><p>作答时间：` + validRange + strings.Repeat(" ", 2050) + `</p></div>`},
		{"oversize body", strings.Repeat(" ", assignmentHeaderMaxBody+1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fields, err := assignmentHeaderMetadata([]byte(tc.body))
			if !errors.Is(err, contract.ErrParse) || fields != nil {
				t.Fatal("expected no partial evidence", fields, err)
			}
		})
	}
}

func TestAssignmentHeaderMetadataNumericBounds(t *testing.T) {
	for _, line := range []string{"题量：-1", "题量：10001", "题量：1.5", "题量：NaN", "满分：NaN", "满分：Inf", "满分：1e300", "满分：-1", "满分：1000001", "满分：100 points"} {
		t.Run(line, func(t *testing.T) {
			fields, err := assignmentHeaderMetadata([]byte(`<div class="ceyan_top"><p>作答时间：11-04 09:30至11-08 23:59</p><p>` + line + `</p></div>`))
			if !errors.Is(err, contract.ErrParse) || fields != nil {
				t.Fatal(fields, err)
			}
		})
	}
	fields, err := assignmentHeaderMetadata([]byte(`<div class="ceyan_top"><p>作答时间：11-04 09:30至11-08 23:59</p><p>题量：0 满分：0</p></div>`))
	if err != nil || fields["question_count"] != 0 || fields["full_score"] != float64(0) {
		t.Fatal(fields, err)
	}
}
