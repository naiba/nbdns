package model

import (
	"index/suffixarray"
	"strings"
	"testing"

	"github.com/naiba/nbdns/pkg/utils"
)

var primaryLocations = []string{"中国", "省", "市", "自治区"}
var nonPrimaryLocations = []string{"台湾", "香港", "澳门"}

var primaryLocationsBytes = [][]byte{[]byte("中国"), []byte("省"), []byte("市"), []byte("自治区")}
var nonPrimaryLocationsBytes = [][]byte{[]byte("台湾"), []byte("香港"), []byte("澳门")}

func BenchmarkCheckPrimary(b *testing.B) {
	for i := 0; i < b.N; i++ {
		checkPrimary("哈哈")
	}
}

func BenchmarkCheckPrimaryStringsContains(b *testing.B) {
	for i := 0; i < b.N; i++ {
		checkPrimaryStringsContains("哈哈")
	}
}

func TestIsMatch(t *testing.T) {
	var up Upstream
	up.matchSplited = utils.ParseRules([]string{"."})
	checkUpstreamMatch(&up, map[string]bool{
		"":             false,
		"a.com.":       true,
		"b.a.com.":     true,
		".b.a.com.cn.": true,
		"b.a.com.cn.":  true,
		"d.b.a.com.":   true,
	}, t)

	up.matchSplited = utils.ParseRules([]string{""})
	checkUpstreamMatch(&up, map[string]bool{
		"":             false,
		"a.com.":       false,
		"b.a.com.":     false,
		".b.a.com.cn.": false,
		"b.a.com.cn.":  false,
		"d.b.a.com.":   false,
	}, t)

	up.matchSplited = utils.ParseRules([]string{"a.com."})
	checkUpstreamMatch(&up, map[string]bool{
		"":             false,
		"a.com.":       true,
		"b.a.com.":     false,
		".b.a.com.cn.": false,
		"b.a.com.cn.":  false,
		"d.b.a.com.":   false,
	}, t)

	up.matchSplited = utils.ParseRules([]string{".a.com."})
	checkUpstreamMatch(&up, map[string]bool{
		"":             false,
		"a.com.":       false,
		"b.a.com.":     true,
		".b.a.com.cn.": false,
		"b.a.com.cn.":  false,
		"d.b.a.com.":   true,
	}, t)

	up.matchSplited = utils.ParseRules([]string{"b.d.com."})
	checkUpstreamMatch(&up, map[string]bool{
		"":             false,
		"a.com.":       false,
		".a.com.":      false,
		"b.d.com.":     true,
		".b.d.com.cn.": false,
		"b.d.com.cn.":  false,
		".c.d.com.":    false,
		"b.d.a.com.":   false,
	}, t)
}

func checkUpstreamMatch(up *Upstream, cases map[string]bool, t *testing.T) {
	for k, v := range cases {
		isMatch := up.IsMatch(k)
		if isMatch != v {
			t.Errorf("Upstream(%s).IsMatch(%s) = %v, want %v", up.matchSplited, k, isMatch, v)
		}
	}
}

func checkPrimary(str string) bool {
	index := suffixarray.New([]byte(str))
	for i := 0; i < len(nonPrimaryLocationsBytes); i++ {
		if len(index.Lookup(nonPrimaryLocationsBytes[i], 1)) > 0 {
			return false
		}
	}
	for i := 0; i < len(primaryLocationsBytes); i++ {
		if len(index.Lookup(primaryLocationsBytes[i], 1)) > 0 {
			return true
		}
	}
	return false
}

func checkPrimaryStringsContains(str string) bool {
	for i := 0; i < len(nonPrimaryLocations); i++ {
		if strings.Contains(str, nonPrimaryLocations[i]) {
			return false
		}
	}
	for i := 0; i < len(primaryLocations); i++ {
		if strings.Contains(str, primaryLocations[i]) {
			return true
		}
	}
	return false
}
