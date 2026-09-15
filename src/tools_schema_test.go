package main

import "testing"

// Regression: tools without mandatory arguments used to advertise
// "required": null, which is not valid JSON Schema. Clients that validate
// tools/list dropped the entire list, so the server looked unreachable.
func TestToolSchemasAreValidJSONSchema(t *testing.T) {
	_, h, token := testServer(t)
	w := post(t, h, token, "", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	res, _ := mustNotBeRPCError(t, w)["result"].(map[string]any)
	tools, _ := res["tools"].([]any)
	if len(tools) == 0 {
		t.Fatal("tools/list returned no tools")
	}
	for _, raw := range tools {
		tool := raw.(map[string]any)
		name, _ := tool["name"].(string)
		schema, ok := tool["inputSchema"].(map[string]any)
		if !ok {
			t.Fatalf("%s: inputSchema is not an object", name)
		}
		if schema["type"] != "object" {
			t.Errorf("%s: inputSchema.type = %v, want object", name, schema["type"])
		}
		if req, present := schema["required"]; present {
			arr, isArr := req.([]any)
			if !isArr || len(arr) == 0 {
				t.Errorf("%s: required must be a non-empty array when present, got %#v", name, req)
			}
		}
		if props, present := schema["properties"]; present {
			if _, isObj := props.(map[string]any); !isObj {
				t.Errorf("%s: properties must be an object, got %#v", name, props)
			}
		}
	}
}
