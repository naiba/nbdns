package model_test

import (
	"index/suffixarray"
	"strings"
	"testing"
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
