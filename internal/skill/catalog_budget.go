package skill

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

func boundedCatalog(skills []Skill) string {
	const budget = IndexMaxChars - len("```\n\n```")
	render := func(limit int) string {
		lines := make([]string, len(skills))
		for i, sk := range skills {
			lines[i] = indexLineWithLimit(sk, limit)
		}
		return strings.Join(lines, "\n")
	}
	joined := render(130)
	if utf8.RuneCountInString(joined) > budget {
		if utf8.RuneCountInString(render(0)) <= budget {
			low, high := 0, 130
			for low < high {
				mid := (low + high + 1) / 2
				if utf8.RuneCountInString(render(mid)) <= budget {
					low = mid
				} else {
					high = mid - 1
				}
			}
			joined = render(low)
		} else {
			joined = catalogPreview(skills, budget)
		}
	}
	return "```\n" + joined + "\n```"
}

func catalogPreview(skills []Skill, budget int) string {
	var lines []string
	used := 0
	for i, sk := range skills {
		line := indexLineWithLimit(sk, 24)
		size := utf8.RuneCountInString(line) + 1
		if used+size+utf8.RuneCountInString(catalogOverflow(len(skills)-i-1)) > budget {
			break
		}
		lines = append(lines, line)
		used += size
	}
	lines = append(lines, catalogOverflow(len(skills)-len(lines)))
	return strings.Join(lines, "\n")
}

func catalogOverflow(count int) string {
	return fmt.Sprintf("… (%d more skills; discover with use_capability action=search and a task-specific query, or action=list.)", count)
}
