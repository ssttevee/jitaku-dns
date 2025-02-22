package webui

func dedupe(s []string) []string {
	seen := make(map[string]struct{})
	var result []string
	for _, v := range s {
		if _, ok := seen[v]; !ok {
			seen[v] = struct{}{}
			result = append(result, v)
		}
	}
	return result
}
