package server

import (
	"encoding/json"
	"testing"
)

func TestGenerationLimitValidationAndAssignment(t *testing.T) {
	for _, tc := range []struct {
		name       string
		value      any
		code, want int
	}{
		{"omitted", nil, 202, 128}, {"zero", 0, 202, 128}, {"one", 1, 202, 1}, {"custom", 17, 202, 17}, {"ceiling", 128, 202, 128}, {"negative", -1, 400, 0}, {"unlimited", -2, 400, 0}, {"above ceiling", 129, 400, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			e.hello()
			body := map[string]any{"model": model, "prompt": "hi", "deadline_ms": 5000}
			if tc.value != nil {
				body["num_predict"] = tc.value
			}
			res, b := e.do("POST", "/v1/jobs", callerToken, "", body)
			if res.StatusCode != tc.code {
				t.Fatalf("submit: %d %s", res.StatusCode, b)
			}
			if tc.code != 202 {
				return
			}
			res, b = e.do("GET", "/v1/jobs/next?wait=1s", workerToken, "1.2.0", nil)
			if res.StatusCode != 200 {
				t.Fatalf("claim: %d %s", res.StatusCode, b)
			}
			var fields map[string]any
			json.Unmarshal(b, &fields)
			if fields["num_predict"] != float64(tc.want) {
				t.Fatalf("assignment limit: %s", b)
			}
		})
	}
}
