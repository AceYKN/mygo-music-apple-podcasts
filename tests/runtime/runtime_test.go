package runtimecheck

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/MoeForge/moejs"
	"github.com/MoeForge/moejs/eventloop"
	"github.com/MoeForge/moejs/webapi"
)

func TestActualMyGoJavaScriptEngine(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Origin") != "" {
			t.Error("unexpected browser Origin header")
		}
		var body struct {
			Method string `json:"method"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		var data any
		if body.Method == "auth.status" {
			data = map[string]any{"authorized": true, "country": "us", "id": "test-account"}
		} else {
			data = map[string]any{"results": map[string]any{"podcasts": map[string]any{"data": []any{map[string]any{"id": "42", "attributes": map[string]any{"name": "Example"}}}}}}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "data": data})
	}))
	defer api.Close()
	rt := moejs.NewRuntime(moejs.Options{})
	loop, err := eventloop.New(rt, eventloop.Options{})
	if err != nil {
		t.Fatal(err)
	}
	web, err := webapi.Install(loop, webapi.Options{Client: &http.Client{Timeout: 3 * time.Second}, Allow: webapi.AllowHosts("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer web.Close()
	handshake, _ := json.Marshal(map[string]any{"protocol": 1, "url": api.URL, "token": strings.Repeat("a", 64)})
	rt.SetGlobal("handshake", string(handshake))
	setup, err := moejs.CompileScript("host.js", `globalThis.host={dir:'plugin',dataDir:'private',platform:'windows',setting:()=>'',t:x=>x,spawn:()=>({readLine:async()=>handshake,wait:()=>new Promise(()=>{}),kill(){}})};`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = rt.RunScript(setup); err != nil {
		t.Fatal(err)
	}
	source, err := os.ReadFile("../../index.js")
	if err != nil {
		t.Fatal(err)
	}
	mod, err := moejs.Compile("index.js", string(source))
	if err != nil {
		t.Fatal(err)
	}
	if err = rt.Load(mod); err != nil {
		t.Fatal(err)
	}
	fn, ok := rt.Export("search")
	if !ok {
		t.Fatal("search export missing")
	}
	rt.SetGlobal("searchPlugin", fn)
	check, err := moejs.CompileScript("verify.js", `globalThis.result=undefined;globalThis.failure='';searchPlugin({query:'Example',kind:'shows'}).then(value=>{result=value},error=>{failure=String(error.stack||error)});`)
	if err != nil {
		t.Fatal(err)
	}
	if err = loop.Run(func(rt *moejs.Runtime) error { _, err := rt.RunScript(check); return err }); err != nil {
		t.Fatal(err)
	}
	extract, err := moejs.CompileScript("result.js", `JSON.stringify({result,failure})`)
	if err != nil {
		t.Fatal(err)
	}
	value, err := rt.RunScript(extract)
	if err != nil {
		t.Fatal(err)
	}
	var outcome struct {
		Result  json.RawMessage `json:"result"`
		Failure string          `json:"failure"`
	}
	if json.Unmarshal([]byte(value.String()), &outcome) != nil {
		t.Fatal("unreadable result")
	}
	if outcome.Failure != "" {
		t.Fatal(outcome.Failure)
	}
	if !strings.Contains(string(outcome.Result), `"Example"`) {
		t.Fatalf("unexpected search result: %s", outcome.Result)
	}
}
