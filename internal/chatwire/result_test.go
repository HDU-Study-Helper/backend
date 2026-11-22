package chatwire

import "testing"

func TestDecodeChatContract(t *testing.T) {
	cases := []struct {
		raw string
		ok  bool
	}{{`{"text":"任务已整理","cards":[],"interaction":null}`, true}, {`{"text":"选一个","cards":[{"type":"future","description":"占位"}],"interaction":{"type":"CHOICE","question":"时间？","options":[{"id":"a","label":"周五"}]}}`, true}, {`{"text":"晚点再说","cards":[]}`, false}, {`{"text":"","cards":[],"interaction":null}`, false}, {`{"text":"test","cards":["<script>unsupported</script>"],"interaction":null}`, false}, {`{"text":"test","cards":[],"interaction":{"type":"CHOICE","question":"选？","options":[{"id":"a","label":"A"},{"id":"a","label":"B"}]}}`, false}, {`{"text":"test","cards":[],"interaction":null,"workflow_id":"client-selected"}`, false}}
	for _, tc := range cases {
		_, e := Decode(tc.raw)
		if (e == nil) != tc.ok {
			t.Fatal(tc.raw, e)
		}
	}
}
