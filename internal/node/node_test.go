package node

import (
	"reflect"
	"testing"
)

func TestSameConfigIncludesEveryConnectionField(t *testing.T) {
	original := Node{Protocol: VLESS, Host: "a.example", Port: 443, UUID: "id", Network: "ws", Security: "tls"}
	typ := reflect.TypeFor[Node]()
	for i := range typ.NumField() {
		name := typ.Field(i).Name
		t.Run(name, func(t *testing.T) {
			changed := original
			f := reflect.ValueOf(&changed).Elem().Field(i)
			switch f.Kind() {
			case reflect.String:
				f.SetString(f.String() + "changed")
			case reflect.Int:
				f.SetInt(f.Int() + 1)
			case reflect.Bool:
				f.SetBool(!f.Bool())
			default:
				t.Fatalf("new field kind needs equality test: %v", f.Kind())
			}
			// AutoPin is learned from the server, not set: a node set up the same keeps its server's pin.
			metadata := name == "ID" || name == "GroupID" || name == "Name" || name == "Link" || name == "AutoPin"
			if SameConfig(&original, &changed) != metadata {
				t.Fatalf("field %s comparison incorrect", name)
			}
		})
	}
}

func TestJSONParametersRejectNull(t *testing.T) {
	for _, param := range []string{"fm", "extra"} {
		if _, err := Parse("vless://id@a.example:443?type=xhttp&" + param + "=null"); err == nil {
			t.Fatalf("%s=null is not an object", param)
		}
	}
}
