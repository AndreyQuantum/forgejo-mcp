package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const miniSwagger = `{
 "swagger":"2.0","basePath":"x/api/v1","info":{"version":"x"},
 "definitions":{"Opt":{"type":"object","required":["title"],"properties":{"title":{"type":"string"},"n":{"type":"integer"},"ls":{"type":"array"},"sub":{"$ref":"#/definitions/Opt"}}}},
 "paths":{
  "/repos/{owner}/{repo}/contents/{filepath}":{
    "get":{"tags":["repository"],"summary":"Get contents. More text here.","operationId":"getC","parameters":[
      {"name":"owner","in":"path","required":true,"type":"string"},
      {"name":"ref","in":"query","type":"string"},
      {"name":"state","in":"query","type":"string","enum":["open","closed"],"required":true},
      {"name":"page","in":"query","type":"integer"}]},
    "post":{"tags":["repository"],"summary":"Create","operationId":"postC","deprecated":true,"parameters":[
      {"name":"body","in":"body","schema":{"$ref":"#/definitions/Opt"}}]}},
  "/version":{"get":{"tags":["miscellaneous"],"summary":"v","operationId":"ver"}}
 }}`

func TestConvert(t *testing.T) {
	idx, err := Convert([]byte(miniSwagger), "1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	if idx.V != "1.2.3" || idx.N != 3 || len(idx.SwaggerSHA256) != 64 {
		t.Fatalf("header %+v", idx)
	}
	get, post := idx.Ops[0], idx.Ops[1]
	if get.M != "GET" || get.W != 1 || get.S != "Get contents." || get.SC != "repository" {
		t.Errorf("get = %+v", get)
	}
	if len(get.Q) != 3 || get.Q[1][1] != "e" || get.Q[1][2] != "open|closed*" || get.Q[2][1] != "i" {
		t.Errorf("q = %v", get.Q)
	}
	if !post.Dep || len(post.B) != 4 || post.B[3][0] != "title" || post.B[3][2] != 1 {
		t.Errorf("post = %+v", post)
	}
	data, err := Marshal(idx)
	if err != nil || !strings.HasPrefix(string(data), `{"v":"1.2.3","n":3,"swagger_sha256":"`) {
		t.Errorf("marshal: %v %.60s", err, data)
	}
}

func TestRunVersionRules(t *testing.T) {
	dir := t.TempDir()
	if err := run(filepath.Join(dir, "missing.json"), "1.0.0", filepath.Join(dir, "o.json")); err == nil {
		t.Error("expected error for a missing file")
	}
	f := filepath.Join(dir, "sw.json")
	if err := os.WriteFile(f, []byte(miniSwagger), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := run(f, "", filepath.Join(dir, "o.json")); err == nil || !strings.Contains(err.Error(), "-version is required") {
		t.Errorf("err = %v", err)
	}
	if err := run(f, "x", filepath.Join(dir, "o.json")); err == nil || !strings.Contains(err.Error(), "semver") {
		t.Errorf("err = %v", err)
	}
	if err := run(f, "v17.0.2", filepath.Join(dir, "o.json")); err != nil {
		t.Errorf("err = %v", err)
	}
}
