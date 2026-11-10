package mooc

import (
	"academic/backend/internal/contract"
	"strings"
	"testing"
)

func TestCatalogDescriptionsExcludeQuestionsScriptsAndLinks(t *testing.T) {
	dto := fixtureDTO(t, `{"id":101,"description":"<p>Course intro</p><script>private()</script>","requirements":"<b>Read before class</b>","chiefLectorDto":{"name":"Teacher"},"chapters":[{"id":1,"lessons":[{"id":2,"units":[{"id":3,"contentType":6,"description":"<p>Discuss the concept</p><a href='https://example.invalid/?signature=secret'>reference</a>"}]}],"homeworks":[{"id":4,"test":{"description":"<b>Submit a report</b>","questions":[{"description":"secret question","answer":"secret answer"}]}}]}]}`)
	co := course{ID: "10", Term: "101", Type: 1}
	for _, kind := range []contract.Resource{contract.Course, "discussion", contract.Assignment} {
		rows, e := termRecords(dto, co, kind)
		if e != nil || len(rows) != 1 {
			t.Fatal(kind, rows, e)
		}
		body := rows[0].Content
		if body == "" || strings.Contains(body, "secret") || strings.Contains(body, "private()") || strings.Contains(body, "<") {
			t.Fatal("unsafe or missing body", body)
		}
	}
	rows, _ := termRecords(dto, co, contract.Course)
	if rows[0].Fields["requirements"] != "Read before class" {
		t.Fatal("requirements lost")
	}
}
