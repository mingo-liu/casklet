package project

import (
	"reflect"
	"testing"
)

func TestProjectConfigOrdersDependenciesAndScopesNames(t *testing.T) {
	doc, err := Decode([]byte(`{"name":"demo","volumes":["data"],"services":{"api":{"run":{"image":"app:1"},"depends-on":{"db":"healthy"}},"db":{"run":{"image":"db:1"}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	order, err := doc.Order()
	if err != nil || !reflect.DeepEqual(order, []string{"db", "api"}) || !reflect.DeepEqual(doc.Networks, []string{"default"}) {
		t.Fatalf("order %v %v", order, err)
	}
	name, err := ScopedName(doc.Name, "data")
	if err != nil || name != "cp-demo-data" {
		t.Fatalf("scope %q %v", name, err)
	}
}

func TestProjectConfigRejectsAmbiguousAndInvalidPlans(t *testing.T) {
	for _, data := range []string{
		`{}`, `[]`, `null`,
		`{"name":"demo","name":"other","services":{"a":{"run":{}}}}`,
		`{"name":"demo","services":{"a":{"run":{"env":["A=1"],"env":["A=2"]}}}}`,
		`{"name":"demo","services":{"a":{"run":{}}},"unknown":true}`,
		`{"name":"demo","services":{"a":{"run":{},"unknown":true}}}`,
		`{"name":"demo","services":{"a":{"run":{},"depends-on":{"missing":"started"}}}}`,
		`{"name":"demo","services":{"a":{"run":{},"depends-on":{"a":"healthy"}}}}`,
		`{"name":"demo","services":{"a":{"run":{},"depends-on":{"b":"ready"}},"b":{"run":{}}}}`,
		`{"name":"demo","services":{"a":{"run":{},"depends-on":{"b":"started"}},"b":{"run":{},"depends-on":{"a":"started"}}}}`,
		`{"name":"demo","networks":["default","default"],"services":{"a":{"run":{}}}}`,
		`{"name":"demo","networks":null,"services":{"a":{"run":{}}}}`,
		`{"name":"demo","services":{"a":{"run":{}}}} {}`,
	} {
		if _, err := Decode([]byte(data)); err == nil {
			t.Errorf("accepted %s", data)
		}
	}
}
