package parse

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var NumberRe = regexp.MustCompile(`\d+(\.\d+)?`)

var arabicDigitReplacer = strings.NewReplacer(
	"٠", "0",
	"١", "1",
	"٢", "2",
	"٣", "3",
	"٤", "4",
	"٥", "5",
	"٦", "6",
	"٧", "7",
	"٨", "8",
	"٩", "9",
)

var arabicDualReplacer = strings.NewReplacer(
	"ثانيتين", "2 ثوان",
	"ثانيتان", "2 ثوان",
	"دقيقتين", "2 دقائق",
	"دقيقتان", "2 دقائق",
	"ساعتين", "2 ساعات",
	"ساعتان", "2 ساعات",
	"يومين", "2 أيام",
	"يومان", "2 أيام",
	"أسبوعين", "2 أسابيع",
	"أسبوعان", "2 أسابيع",
	"اسبوعين", "2 أسابيع",
	"اسبوعان", "2 أسابيع",
	"شهرين", "2 أشهر",
	"شهران", "2 أشهر",
	"سنتين", "2 سنوات",
	"سنتان", "2 سنوات",
)

var (
	yearWordRe   = regexp.MustCompile(`(?:^|[^\p{L}])(years?|سنة|سنوات|عام|أعوام|اعوام)(?:$|[^\p{L}])`)
	monthWordRe  = regexp.MustCompile(`(?:^|[^\p{L}])(months?|شهر|أشهر|اشهر|شهور)(?:$|[^\p{L}])`)
	weekWordRe   = regexp.MustCompile(`(?:^|[^\p{L}])(weeks?|أسبوع|اسبوع|أسابيع|اسابيع)(?:$|[^\p{L}])`)
	dayWordRe    = regexp.MustCompile(`(?:^|[^\p{L}])(days?|يوم|أيام|ايام)(?:$|[^\p{L}])`)
	hourWordRe   = regexp.MustCompile(`(?:^|[^\p{L}])(hours?|ساعة|ساعات)(?:$|[^\p{L}])`)
	minuteWordRe = regexp.MustCompile(`(?:^|[^\p{L}])(minutes?|mins?|دقيقة|دقائق|دقايق)(?:$|[^\p{L}])`)
	secondWordRe = regexp.MustCompile(`(?:^|[^\p{L}])(seconds?|secs?|ثانية|ثوان|ثواني)(?:$|[^\p{L}])`)
)

func normalizeDigits(s string) string {
	return arabicDigitReplacer.Replace(s)
}

func ParseChapterNumber(raw string) (float64, error) {
	match := NumberRe.FindString(normalizeDigits(raw))
	if match == "" {
		return 0, fmt.Errorf("no number found in %q", raw)
	}
	return strconv.ParseFloat(match, 64)
}

var arabicMonths = map[string]time.Month{
	"يناير":        time.January,
	"كانون الثاني": time.January,
	"فبراير":       time.February,
	"شباط":         time.February,
	"مارس":         time.March,
	"آذار":         time.March,
	"اذار":         time.March,
	"أبريل":        time.April,
	"ابريل":        time.April,
	"نيسان":        time.April,
	"مايو":         time.May,
	"أيار":         time.May,
	"ايار":         time.May,
	"يونيو":        time.June,
	"حزيران":       time.June,
	"يوليو":        time.July,
	"تموز":         time.July,
	"أغسطس":        time.August,
	"اغسطس":        time.August,
	"آب":           time.August,
	"اب":           time.August,
	"سبتمبر":       time.September,
	"أيلول":        time.September,
	"ايلول":        time.September,
	"أكتوبر":       time.October,
	"اكتوبر":       time.October,
	"تشرين الأول":  time.October,
	"تشرين الاول":  time.October,
	"نوفمبر":       time.November,
	"تشرين الثاني": time.November,
	"ديسمبر":       time.December,
	"كانون الأول":  time.December,
	"كانون الاول":  time.December,
}

func ParseDate(raw string) (time.Time, error) {
	return parseDateAt(raw, time.Now().UTC())
}

func parseDateAt(raw string, now time.Time) (time.Time, error) {
	raw = strings.TrimSpace(normalizeDigits(raw))
	if raw == "" {
		return time.Time{}, fmt.Errorf("empty date")
	}

	value := arabicDualReplacer.Replace(raw)
	normalized := strings.ToLower(value)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)

	switch {
	case strings.HasPrefix(normalized, "today") || normalized == "اليوم":
		return today, nil
	case strings.HasPrefix(normalized, "yesterday") ||
		strings.HasPrefix(normalized, "يوم واحد") ||
		normalized == "أمس" || normalized == "امس":
		return today.AddDate(0, 0, -1), nil
	case strings.Contains(normalized, "ثانية واحدة"):
		return now.Add(-1 * time.Second), nil
	case strings.Contains(normalized, "دقيقة واحدة"):
		return now.Add(-1 * time.Minute), nil
	case strings.Contains(normalized, "ساعة واحدة"):
		return now.Add(-1 * time.Hour), nil
	case strings.Contains(normalized, "أسبوع واحد") || strings.Contains(normalized, "اسبوع واحد"):
		return now.AddDate(0, 0, -7), nil
	case strings.Contains(normalized, "شهر واحد"):
		return now.AddDate(0, -1, 0), nil
	case strings.Contains(normalized, "سنة واحدة"):
		return now.AddDate(-1, 0, 0), nil
	}

	// Relative dates with digits
	if n := NumberRe.FindString(normalized); n != "" {
		if num, err := strconv.Atoi(strings.SplitN(n, ".", 2)[0]); err == nil {
			switch {
			case yearWordRe.MatchString(normalized):
				return now.AddDate(-num, 0, 0), nil
			case monthWordRe.MatchString(normalized):
				return now.AddDate(0, -num, 0), nil
			case weekWordRe.MatchString(normalized):
				return now.AddDate(0, 0, -(num * 7)), nil
			case dayWordRe.MatchString(normalized):
				return now.AddDate(0, 0, -num), nil
			case hourWordRe.MatchString(normalized):
				return now.Add(-time.Duration(num) * time.Hour), nil
			case minuteWordRe.MatchString(normalized):
				return now.Add(-time.Duration(num) * time.Minute), nil
			case secondWordRe.MatchString(normalized):
				return now.Add(-time.Duration(num) * time.Second), nil
			}
		}
	}

	// Standard numeric / English date layouts (including Mangalek's yyyy-MM-dd)
	layouts := []string{
		"2006-01-02",
		"2006/01/02",
		"02/01/2006",
		"January 2, 2006",
		"Jan 2, 2006",
		"2 January 2006",
		"2 Jan 2006",
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, raw); err == nil {
			return t.UTC(), nil
		}
	}

	// Arabic "MMMM d, yyyy" (e.g. "يناير 01, 2025", "يناير 5، 2025") or "d MMMM yyyy"
	cleaned := strings.ReplaceAll(strings.ReplaceAll(raw, "،", " "), ",", " ")
	parts := strings.Fields(cleaned)
	if len(parts) == 3 {
		if month, ok := arabicMonths[parts[0]]; ok {
			day, err1 := strconv.Atoi(parts[1])
			year, err2 := strconv.Atoi(parts[2])
			if err1 == nil && err2 == nil {
				return time.Date(year, month, day, 0, 0, 0, 0, time.UTC), nil
			}
		}
		if month, ok := arabicMonths[parts[1]]; ok {
			day, err1 := strconv.Atoi(parts[0])
			year, err2 := strconv.Atoi(parts[2])
			if err1 == nil && err2 == nil {
				return time.Date(year, month, day, 0, 0, 0, 0, time.UTC), nil
			}
		}
	}

	return time.Time{}, fmt.Errorf("unknown date format: %q", raw)
}
