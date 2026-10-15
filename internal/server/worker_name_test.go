package server

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/gkhntpbs/dumb/internal/auth"
	"github.com/gkhntpbs/dumb/internal/protocol"
)

// rawHello posts a literal JSON body, so old and future workers can be
// imitated exactly (a Go struct would always carry today's fields).
func rawHello(t *testing.T, s *Server, token, body string) protocol.HelloResponse {
	t.Helper()
	w := publicCall(s, "POST", "/v1/workers/hello", token, json.RawMessage(body))
	if w.Code != 200 {
		t.Fatalf("hello %s: %d %s", body, w.Code, w.Body)
	}
	var res protocol.HelloResponse
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	return res
}

func adminWorkerRows(t *testing.T, s *Server, token string) (map[string]protocol.AdminWorker, string) {
	t.Helper()
	raw := adminGet(s, "/admin/v1/workers", token).Body.String()
	page := adminDecode[protocol.AdminWorkers](t, s, "/admin/v1/workers", token)
	rows := map[string]protocol.AdminWorker{}
	for _, row := range page.Items {
		rows[row.WorkerID] = row
	}
	return rows, raw
}

func TestWorkerNameStoredBoundedAndDisplayOnly(t *testing.T) {
	s, a, admin := adminFixture(t)
	tokens := map[string]string{}
	for _, id := range []string{"w-office", "w-old", "w-control", "w-long", "w-twin"} {
		tokens[id] = randomAdminToken()
		a.Add(id, auth.RoleWorker, tokens[id])
	}
	hello := func(id, extra string) protocol.HelloResponse {
		t.Helper()
		return rawHello(t, s, tokens[id], `{"worker_version":"1.0.0","protocol_version":1,"models":["m"],"capacity":1`+extra+`}`)
	}
	long := strings.Repeat("ç", 200)
	if res := hello("w-office", `,"worker_name":"  Office Mac  "`); res.WorkerID != "w-office" {
		t.Fatalf("worker id from credential, got %q", res.WorkerID)
	}
	hello("w-old", "") // a worker from before worker_name
	hello("w-control", `,"worker_name":"evil\u001b[31m\nname"`)
	hello("w-long", `,"worker_name":"`+long+`"`)
	// The name is not an identity: a name equal to another worker's id or to
	// another worker's name changes nothing about who is who.
	hello("w-twin", `,"worker_name":"Office Mac"`)

	rows, raw := adminWorkerRows(t, s, admin)
	if len(rows) != 5 {
		t.Fatalf("rows %v", rows)
	}
	if got := rows["w-office"].WorkerName; got != "Office Mac" {
		t.Errorf("trimmed name %q", got)
	}
	if got := rows["w-twin"].WorkerName; got != "Office Mac" {
		t.Errorf("twin name %q", got)
	}
	if got := rows["w-old"].WorkerName; got != "" {
		t.Errorf("old worker name %q", got)
	}
	if got := rows["w-control"].WorkerName; got != "" {
		t.Errorf("control characters kept: %q", got)
	}
	if got := rows["w-long"].WorkerName; got != strings.Repeat("ç", protocol.MaxWorkerNameRunes) || utf8.RuneCountInString(got) != protocol.MaxWorkerNameRunes {
		t.Errorf("long name %d runes", utf8.RuneCountInString(got))
	}
	if strings.Contains(raw, "evil") || strings.Contains(raw, long) {
		t.Fatal("unbounded or rejected name exposed")
	}
	// Absent, not empty: the row of a nameless worker carries no worker_name.
	var page struct{ Items []map[string]any }
	if err := json.Unmarshal([]byte(raw), &page); err != nil {
		t.Fatal(err)
	}
	for _, item := range page.Items {
		if _, ok := item["worker_name"]; ok && (item["worker_id"] == "w-old" || item["worker_id"] == "w-control") {
			t.Errorf("worker_name present for %v", item["worker_id"])
		}
	}

	// A later hello replaces the name; one without a name clears it.
	hello("w-office", `,"worker_name":"Renamed"`)
	hello("w-twin", "")
	rows, _ = adminWorkerRows(t, s, admin)
	if rows["w-office"].WorkerName != "Renamed" || rows["w-twin"].WorkerName != "" {
		t.Fatalf("after re-hello: %q, %q", rows["w-office"].WorkerName, rows["w-twin"].WorkerName)
	}

	// The claim path is keyed on the credential, never on the name.
	if w := publicCall(s, "GET", "/v1/jobs/next?wait=0", tokens["w-office"], nil); w.Code != 204 {
		t.Fatalf("claim: %d %s", w.Code, w.Body)
	}
}

// Hello ignores unknown fields; that is what lets a newer worker that sends
// worker_name talk to a server from before it, and it must stay that way.
func TestHelloIgnoresUnknownFields(t *testing.T) {
	s, a, admin := adminFixture(t)
	tok := randomAdminToken()
	a.Add("w-future", auth.RoleWorker, tok)
	rawHello(t, s, tok, `{"worker_version":"1.0.0","worker_name":"pi","protocol_version":1,"models":["m"],"capacity":1,"features":["ack","later"],"future_field":{"x":1}}`)
	rows, _ := adminWorkerRows(t, s, admin)
	if rows["w-future"].WorkerName != "pi" || rows["w-future"].Capacity != 1 {
		t.Fatalf("%+v", rows["w-future"])
	}
}
