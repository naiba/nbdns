package utils

import "strings"

func ParseRules(rulesRaw []string) [][]string {
	var rules [][]string
	for _, r := range rulesRaw {
		if r == "" {
			continue
		}
		if !strings.HasSuffix(r, ".") {
			r += "."
		}
		rules = append(rules, strings.Split(r, "."))
	}
	return rules
}

func HasMatchedRule(rules [][]string, domain string) bool {
	var hasMatch bool
OUTER:
	for _, m := range rules {
		domainSplited := strings.Split(domain, ".")
		i := len(m) - 1
		j := len(domainSplited) - 1
		// 从根域名开始匹配
		for i >= 0 && j >= 0 {
			if m[i] != domainSplited[j] && m[i] != "" {
				continue OUTER
			}
			i--
			j--
		}
		// 如果规则中还有剩余，但是域名已经匹配完了，检查规则最后一位是否是任意匹配
		if j != -1 && i == -1 && m[0] != "" {
			continue OUTER
		}
		hasMatch = i == -1
		// 如果匹配到了，就不用再匹配了
		if hasMatch {
			break
		}
	}
	return hasMatch
}
