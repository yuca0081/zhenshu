package llm

import "testing"

func TestExtractJSON(t *testing.T) {
	pure := `{"a":1}`
	cases := []struct{ in, want string }{
		{pure, pure},
		{"html\n        " + `{"note":"x","html":"<template>\"}…"}` + "", `{"note":"x","html":"<template>\"}…"}`},
		{"```json\n" + pure + "\n```", pure}, // 真实调用是 extractJSON(stripFence(x))；单独对围栏串也能榜出内层
		{"前缀解释 {\"a\":{\"b\":2}} 后缀解释", "{\"a\":{\"b\":2}}"},
		{"{\"s\":\"含}花括号\"}尾巴", "{\"s\":\"含}花括号\"}"},
		{"没有对象", "没有对象"},
		{"截断的 {\"a\":", "{\"a\":"},
	}
	for _, c := range cases {
		got := extractJSON(c.in)
		if got != c.want {
			t.Errorf("extractJSON(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
