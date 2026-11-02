package foreman

import (
	"strings"
	"unicode"
)

// Reply-language control.
//
// The agent contract already said "reply in the same language as the user", but
// that is one bullet buried in a long summary, and models still drifted into
// English mid-conversation — especially when the tool output they were reading
// (code, stack traces, English error strings) was in English. A generic
// instruction competes with everything else in the prompt.
//
// So the language is resolved deterministically from the user's own message and
// stated once, near the top of the prompt, as a concrete instruction with the
// language name. Detection is a stopword vote: no model call, no dependency, no
// network, and stable across runs. When the vote is inconclusive (a code paste,
// a one-word message, a mixed message) nothing is claimed and the generic
// contract rule stays in force.

// languageHints maps a language to the stopwords that most reliably identify
// it. Words are chosen to be common in ordinary prose and rare as identifiers
// in source code, so pasted code does not outvote the human sentence around it.
//
// Typos are expected: the people using this write fast, and Indonesian in
// particular arrives as "blum", "apkah", "juag", "perbiaki". Exact matching
// alone abstained on real messages, so short near-misses are matched fuzzily
// (see fuzzyMatch) — but only for words long enough that a one-edit difference
// cannot be a different word by accident.
var languageHints = []struct {
	name  string
	words []string
}{
	{"Indonesian", []string{
		"yang", "dan", "tidak", "untuk", "dengan", "saya", "aku", "kamu", "ini", "itu",
		"ada", "ad", "bisa", "sudah", "belum", "blum", "udah", "udh", "kenapa", "gimana",
		"bagaimana", "tolong", "coba", "biar", "jadi", "kalau", "kalo", "gak", "nggak",
		"enggak", "banget", "bikin", "perbaiki", "kasih", "mau", "harus", "masih", "sih",
		"dong", "aja", "juga", "atau", "dari", "di", "pada", "saat", "karena", "supaya",
		"cek", "apakah", "tersedia", "gini", "gitu", "kok", "bang", "mas", "kak",
	}},
	{"English", []string{
		"the", "and", "you", "that", "this", "with", "have", "from", "not", "but",
		"what", "why", "how", "please", "should", "would", "could", "there", "they",
		"when", "which", "about", "into", "because", "does", "did", "was", "were",
	}},
	{"Spanish", []string{
		"que", "los", "las", "una", "por", "para", "con", "como", "pero", "esto",
		"esta", "muy", "también", "hay", "cuando", "porque", "puedes", "hola",
	}},
	{"Portuguese", []string{
		"que", "não", "uma", "para", "com", "como", "mas", "isso", "está", "muito",
		"também", "quando", "porque", "você", "obrigado", "olá",
	}},
	{"French", []string{
		"les", "des", "une", "pour", "avec", "dans", "mais", "est", "pas", "vous",
		"nous", "quand", "parce", "merci", "bonjour", "peux",
	}},
	{"German", []string{
		"und", "nicht", "eine", "für", "mit", "dass", "ist", "auch", "aber", "wie",
		"wenn", "warum", "bitte", "danke", "hallo", "kann",
	}},
	{"Vietnamese", []string{
		"không", "được", "những", "người", "trong", "phải", "cũng", "nhưng", "với",
		"cho", "này", "để", "khi", "vì", "cảm", "ơn",
	}},
	{"Japanese", []string{"です", "ます", "これ", "それ", "して", "ください", "ありがとう"}},
	{"Chinese", []string{"这个", "那个", "我们", "可以", "不是", "什么", "怎么", "谢谢"}},
	{"Korean", []string{"입니다", "합니다", "그리고", "하지만", "감사합니다", "어떻게"}},
	{"Russian", []string{"что", "это", "как", "для", "или", "если", "спасибо", "привет"}},
	{"Arabic", []string{"هذا", "هذه", "لكن", "على", "من", "إلى", "شكرا", "كيف"}},
}

// fuzzyMatch reports whether token is a near-miss of hint. Only words long
// enough to stay unambiguous are eligible: a 4-letter word may differ by one
// edit (typo), a 5+ letter word by up to two. Shorter words must match exactly,
// which is why the hint lists carry the short typo forms themselves.
func fuzzyMatch(token, hint string) bool {
	if token == hint {
		return true
	}
	// Multi-byte scripts have no "typo" model worth guessing.
	tr, hr := []rune(token), []rune(hint)
	if len(tr) < 4 || len(hr) < 4 {
		return false
	}
	if absInt(len(tr)-len(hr)) > 2 {
		return false
	}
	maxDist := 1
	if len(tr) >= 6 && len(hr) >= 6 {
		maxDist = 2
	}
	return editDistanceAtMost(tr, hr, maxDist)
}

func absInt(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// editDistanceAtMost is a bounded Levenshtein check: it returns true as soon as
// the distance is known to exceed limit, so the common case stays cheap.
func editDistanceAtMost(a, b []rune, limit int) bool {
	if len(a) > len(b) {
		a, b = b, a
	}
	if len(b)-len(a) > limit {
		return false
	}
	prev := make([]int, len(a)+1)
	cur := make([]int, len(a)+1)
	for i := range prev {
		prev[i] = i
	}
	for j := 1; j <= len(b); j++ {
		cur[0] = j
		rowMin := cur[0]
		for i := 1; i <= len(a); i++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[i] = minInt(prev[i]+1, cur[i-1]+1, prev[i-1]+cost)
			if cur[i] < rowMin {
				rowMin = cur[i]
			}
		}
		if rowMin > limit {
			return false
		}
		prev, cur = cur, prev
	}
	return prev[len(a)] <= limit
}

func minInt(vals ...int) int {
	m := vals[0]
	for _, v := range vals[1:] {
		if v < m {
			m = v
		}
	}
	return m
}

// detectReplyLanguage returns the language name the user is writing in, or ""
// when the vote is not decisive enough to justify an instruction.
//
// The bar is deliberately high: a wrong "reply in X" is worse than the generic
// rule, so a language must both reach minLanguageScore and beat the runner-up
// by a clear margin.
func detectReplyLanguage(text string) string {
	words := tokenizeForLanguage(text)
	if len(words) < 2 {
		return ""
	}
	// Count each hint word at most once per message: a message that repeats
	// "tidak" ten times is not ten times more Indonesian, and repetition would
	// let one word hijack the vote.
	seen := map[string]bool{}
	unique := words[:0]
	for _, w := range words {
		if !seen[w] {
			seen[w] = true
			unique = append(unique, w)
		}
	}
	best, bestScore, runnerUp := "", 0, 0
	for _, hint := range languageHints {
		score := 0
		for _, w := range unique {
			for _, hw := range hint.words {
				if fuzzyMatch(w, hw) {
					score++
					break
				}
			}
		}
		if score > bestScore {
			best, runnerUp = hint.name, bestScore
			bestScore = score
		} else if score > runnerUp {
			runnerUp = score
		}
	}
	const minLanguageScore = 3
	if bestScore < minLanguageScore || bestScore-runnerUp < 2 {
		return ""
	}
	return best
}

// tokenizeForLanguage lowercases and splits text into word tokens, keeping
// non-Latin scripts (which have no spaces) as whole runs.
func tokenizeForLanguage(text string) []string {
	var tokens []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			tokens = append(tokens, strings.ToLower(cur.String()))
			cur.Reset()
		}
	}
	for _, r := range text {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			cur.WriteRune(r)
		case r == '\'' || r == '-' || r == '_':
			// keep contractions/identifiers together
			cur.WriteRune(r)
		default:
			flush()
		}
	}
	flush()
	return tokens
}

// replyLanguageDirective is injected near the top of the run prompt. It is
// stated once, concretely, and paired with the reason the model tends to drift
// (English tool output) so the instruction survives contact with a long
// English log.
func replyLanguageDirective(language string) string {
	if language == "" {
		return ""
	}
	return "## Reply language (enforced)\n" +
		"The user is writing in **" + language + "**. Write your reply in " + language + ".\n" +
		"- Code, identifiers, file paths, command names, and quoted error strings stay exactly as they are — never translate them.\n" +
		"- Tool output you read may be in English; that is not a reason to answer in English.\n" +
		"- Keep the same language for the whole reply, including summaries and questions."
}
