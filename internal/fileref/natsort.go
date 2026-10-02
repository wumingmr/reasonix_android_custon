package fileref

import (
	"strconv"
	"strings"
	"unicode"
)

// NaturalLess orders names the way file managers do: digit runs compare by
// numeric value ("file2" < "file10", "第2卷" < "第10卷"), everything else
// case-insensitively, and a digit sorts before a non-digit.
func NaturalLess(a, b string) bool {
	a = strings.ToLower(a)
	b = strings.ToLower(b)
	ra := []rune(a)
	rb := []rune(b)

	i, j := 0, 0
	for i < len(ra) && j < len(rb) {
		aDig := unicode.IsDigit(ra[i])
		bDig := unicode.IsDigit(rb[j])

		if aDig && bDig {
			aStart := i
			for i < len(ra) && unicode.IsDigit(ra[i]) {
				i++
			}
			bStart := j
			for j < len(rb) && unicode.IsDigit(rb[j]) {
				j++
			}

			aNum, _ := strconv.Atoi(string(ra[aStart:i]))
			bNum, _ := strconv.Atoi(string(rb[bStart:j]))
			if aNum != bNum {
				return aNum < bNum
			}
			// Same numeric value: shorter digit string first (e.g. "1" < "01").
			aLen := i - aStart
			bLen := j - bStart
			if aLen != bLen {
				return aLen < bLen
			}
		} else if !aDig && !bDig {
			if ra[i] != rb[j] {
				return ra[i] < rb[j]
			}
			i++
			j++
		} else {
			return aDig
		}
	}
	return len(ra) < len(rb)
}
