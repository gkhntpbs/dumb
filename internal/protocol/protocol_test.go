package protocol

import (
	"encoding/json"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

func TestSemVer(t *testing.T) {
	a, err := ParseSemVer("v1.2.3")
	if err != nil || a != (SemVer{1, 2, 3}) {
		t.Fatalf("got %v, %v", a, err)
	}
	for _, bad := range []string{"", "1.2", "1.2.x", "1.-2.3", "1.2.3.4"} {
		if _, err := ParseSemVer(bad); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
	less := [][2]string{{"1.2.3", "1.2.4"}, {"1.9.9", "2.0.0"}, {"0.10.0", "0.11.0"}}
	for _, c := range less {
		x, _ := ParseSemVer(c[0])
		y, _ := ParseSemVer(c[1])
		if !x.Less(y) || y.Less(x) {
			t.Errorf("%s < %s failed", c[0], c[1])
		}
	}
	if a.Less(a) {
		t.Error("equal versions reported as less")
	}
	// ParseSemVer stays strict: a pre-release is ParseReleaseVersion's business.
	if _, err := ParseSemVer("1.2.3-beta.1"); err == nil {
		t.Error("ParseSemVer accepted a pre-release")
	}
}

func TestVersion(t *testing.T) {
	v, err := ParseReleaseVersion("v0.2.0-beta.1")
	if err != nil || v != (ReleaseVersion{SemVer{0, 2, 0}, "beta.1"}) || v.String() != "0.2.0-beta.1" || !v.IsPrerelease() {
		t.Fatalf("got %+v, %v", v, err)
	}
	for _, bad := range []string{"", "1.2", "1.2.3-", "1.2.3-beta..1", "1.2.3+build", "1.2.3-be_ta", "v-1.2.3"} {
		if _, err := ParseReleaseVersion(bad); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
	// semver.org §11 ordering.
	order := []string{"1.0.0-alpha", "1.0.0-alpha.1", "1.0.0-alpha.beta", "1.0.0-beta", "1.0.0-beta.2",
		"1.0.0-beta.11", "1.0.0-rc.1", "1.0.0", "1.0.1-beta.1", "1.0.1"}
	for i := 0; i+1 < len(order); i++ {
		x, err1 := ParseReleaseVersion(order[i])
		y, err2 := ParseReleaseVersion(order[i+1])
		if err1 != nil || err2 != nil {
			t.Fatal(err1, err2)
		}
		if !x.Less(y) || y.Less(x) || x.Compare(x) != 0 {
			t.Errorf("%s < %s failed", order[i], order[i+1])
		}
	}
	// Every plain SemVer parses to the same core with no pre-release.
	if v, err := ParseReleaseVersion("1.2.3"); err != nil || v.SemVer != (SemVer{1, 2, 3}) || v.Pre != "" {
		t.Errorf("got %+v, %v", v, err)
	}
}

// TestTypesMatchSpec checks that every schema in openapi.yaml has a Go type
// with exactly the same JSON field names, and vice versa.
func TestTypesMatchSpec(t *testing.T) {
	types := map[string]any{
		"Error": Error{}, "UpgradeError": UpgradeError{}, "HelloRequest": HelloRequest{},
		"HelloResponse": HelloResponse{}, "WhoAmI": WhoAmI{}, "SubmitRequest": SubmitRequest{}, "Assignment": Assignment{},
		"ResultRequest": ResultRequest{}, "AckRequest": AckRequest{}, "JobStatus": JobStatus{}, "Status": Status{},
	}
	spec := specSchemas(t)
	for name, props := range spec {
		v, ok := types[name]
		if !ok {
			t.Errorf("schema %s has no Go type", name)
			continue
		}
		var got []string
		rt := reflect.TypeOf(v)
		for i := range rt.NumField() {
			got = append(got, strings.Split(rt.Field(i).Tag.Get("json"), ",")[0])
		}
		if strings.Join(got, ",") != strings.Join(props, ",") {
			t.Errorf("%s: Go fields %v, spec properties %v", name, got, props)
		}
		if _, err := json.Marshal(v); err != nil {
			t.Error(err)
		}
	}
	for name := range types {
		if _, ok := spec[name]; !ok {
			t.Errorf("Go type %s has no schema", name)
		}
	}
}

// specSchemas reads components.schemas.*.properties keys in order. The spec's
// indentation is fixed (2 spaces per level), so a line scanner is enough and
// keeps the module free of a YAML dependency.
func specSchemas(t *testing.T) map[string][]string {
	b, err := os.ReadFile("../../docs/protocol/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	key := regexp.MustCompile(`^( *)([A-Za-z_]+):`)
	out := map[string][]string{}
	inSchemas, schema, inProps := false, "", false
	for _, line := range strings.Split(string(b), "\n") {
		m := key.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		indent := len(m[1])
		switch {
		case indent == 2:
			inSchemas = m[2] == "schemas"
		case !inSchemas:
		case indent == 4:
			schema, inProps = m[2], false
			out[schema] = nil
		case indent == 6:
			inProps = m[2] == "properties"
		case indent == 8 && inProps:
			out[schema] = append(out[schema], m[2])
		}
	}
	if len(out) == 0 {
		t.Fatal("no schemas found")
	}
	return out
}
