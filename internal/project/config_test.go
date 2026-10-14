package project

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
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

func TestProjectExternalVolumeDeclarations(t *testing.T) {
	base := func(fields string) string { return `{"name":"demo",` + fields + `,"services":{"app":{"run":{}}}}` }
	doc, err := Decode([]byte(base(`"volumes":["owned"],"external-volumes":{"data":"Restored_Data.1"}`)))
	if err != nil || doc.ExternalVolumes["data"] != "Restored_Data.1" {
		t.Fatalf("external declaration: %+v %v", doc, err)
	}
	for _, fields := range []string{
		`"external-volumes":null`, `"external-volumes":[]`, `"external-volumes":{"data":true}`,
		`"external-volumes":{"data":"one","data":"two"}`, `"external-volumes":{"Data":"one"}`,
		`"external-volumes":{"data":""}`, `"external-volumes":{"data":"../outside"}`,
		`"external-volumes":{"data":"-bad"}`, `"external-volumes":{"data":"one","other":"one"}`,
		`"volumes":["data"],"external-volumes":{"data":"restored"}`,
		`"volumes":["data"],"external-volumes":{"other":"cp-demo-data"}`,
	} {
		if _, err := Decode([]byte(base(fields))); err == nil {
			t.Errorf("accepted %s", fields)
		}
	}
	for _, length := range []int{63, 64} {
		fields := `"external-volumes":{"` + strings.Repeat("a", length) + `":"` + strings.Repeat("A", length) + `"}`
		_, err := Decode([]byte(base(fields)))
		if (err == nil) != (length == 63) {
			t.Fatalf("external name length %d: %v", length, err)
		}
	}
	for _, owned := range []int{0, 1, 16} {
		document := map[string]any{"name": "demo", "services": map[string]any{"app": map[string]any{"run": map[string]any{}}}}
		volumes := []string{}
		external := map[string]string{}
		for i := 0; i < owned; i++ {
			volumes = append(volumes, fmt.Sprintf("owned-%d", i))
		}
		for i := owned; i < 16; i++ {
			external[fmt.Sprintf("external-%d", i)] = fmt.Sprintf("existing-%d", i)
		}
		document["volumes"], document["external-volumes"] = volumes, external
		data, _ := json.Marshal(document)
		if _, err := Decode(data); err != nil {
			t.Fatalf("16 total rejected: %v", err)
		}
		external["overflow"] = "overflow"
		data, _ = json.Marshal(document)
		if _, err := Decode(data); err == nil {
			t.Fatal("accepted 17 total volumes")
		}
	}
}
