package runlog

import (
	"hash/fnv"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// 日誌記號與上限：與前端 lib/core/diagnostics/redaction.dart 使用同一套佔標文字，
// 兩邊實作的都是根倉庫 ER-SEC-001 §7 的日誌脫敏清單。
const (
	// RedactedValue 取代「永不記錄」欄位的值（鍵名保留，讓人看得出擋了什麼）。
	RedactedValue = "[redacted]"
	// MaskedValue 取代形狀可疑、但無法歸屬到任何鍵名的憑證值。
	MaskedValue = "[masked]"
	// RefPrefix 加上一段不可還原的短辨識碼，用於「只留標識」的欄位。
	RefPrefix = "ref#"
	// MaxValueRunes 是單個欄位值寫出前保留的上限（以字元計，非位元組）。
	// 截斷一律發生在遮罩之後，避免長值被切短後反而露出可辨識的開頭。
	MaxValueRunes = 600
)

// ValueAction 描述某個欄位名在日誌裡的處置方式。
type ValueAction int

// 處置方式枚舉：ER-SEC-001 §7 的三大類加上「一般欄位」。
const (
	// ActionKeep 是預設值：鍵名與值都不特別處理，仍要過值形狀掃描與截斷。
	ActionKeep ValueAction = iota
	// ActionRedact 表示值永不記錄，一律換成 RedactedValue。
	ActionRedact
	// ActionRef 表示值改記為 RefPrefix 加短辨識碼（同值穩定、不可還原）。
	ActionRef
)

// keyActions 為「正規化欄位名 → 處置規則」的唯一清單。
//
// 鍵名先正規化（小寫並去掉底線、連字號、點與引號）再比對，因此 session_token、
// Session-Token 與 session.token 會落到同一個判定上。一張表而非兩組集合：
// 分成兩份時「同一個鍵名同時出現在兩組」這種矛盾不會讓建置失敗，只會讓其中一組失效。
var keyActions = map[string]ValueAction{
	// 永不記錄：憑據本體（§7「密碼/PIN/恢復碼」「Root 憑據」「TLS 私鑰」）。
	"password":         ActionRedact,
	"passwd":           ActionRedact,
	"pwd":              ActionRedact,
	"pin":              ActionRedact,
	"pintoken":         ActionRedact,
	"recoverycode":     ActionRedact,
	"recoverykey":      ActionRedact,
	"passphrase":       ActionRedact,
	"rootpassword":     ActionRedact,
	"rootpasswordhash": ActionRedact,
	"passwordhash":     ActionRedact,
	"privatekey":       ActionRedact,
	"tlskey":           ActionRedact,
	"clientkey":        ActionRedact,
	"secret":           ActionRedact,
	"clientsecret":     ActionRedact,
	"signingkey":       ActionRedact,
	"certificate":      ActionRedact,

	// 永不記錄：通訊正文（§7「私聊正文：記錄事件，預設不記錄正文」）。
	// 這些鍵名本身就是「內容」的載體，值一律不進日誌；
	// 要記錄的是事件（誰、對誰、何時），由呼叫端用別的欄位表達。
	"message":        ActionRedact,
	"messages":       ActionRedact,
	"messagebody":    ActionRedact,
	"messagecontent": ActionRedact,
	"content":        ActionRedact,
	"body":           ActionRedact,
	"rawbody":        ActionRedact,
	"requestbody":    ActionRedact,
	"responsebody":   ActionRedact,
	"postbody":       ActionRedact,
	"text":           ActionRedact,
	"chat":           ActionRedact,
	"chatbody":       ActionRedact,
	"chattext":       ActionRedact,
	"chatmessage":    ActionRedact,
	"privatechat":    ActionRedact,
	"privatemessage": ActionRedact,
	"dm":             ActionRedact,
	"dmtext":         ActionRedact,
	"payload":        ActionRedact,
	"arguments":      ActionRedact,

	// 只留標識：令牌、Cookie、金鑰（§7「Session Token / Cookie 值」「QR Token」）。
	"token":              ActionRef,
	"accesstoken":        ActionRef,
	"refreshtoken":       ActionRef,
	"authtoken":          ActionRef,
	"sessiontoken":       ActionRef,
	"sessionid":          ActionRef,
	"sid":                ActionRef,
	"cookie":             ActionRef,
	"setcookie":          ActionRef,
	"authorization":      ActionRef,
	"proxyauthorization": ActionRef,
	"bearer":             ActionRef,
	"csrftoken":          ActionRef,
	"csrf":               ActionRef,
	"apikey":             ActionRef,
	"apitoken":           ActionRef,
	"qrcode":             ActionRef,
	"qrcodetoken":        ActionRef,
	"idempotencykey":     ActionRef,
}

// normalizeKey 正規化欄位名：小寫並去掉底線、連字號、點與兩種引號。
func normalizeKey(key string) string {
	var b strings.Builder
	b.Grow(len(key))
	for _, r := range key {
		switch r {
		case '_', '-', '.', '"', '\'':
			continue
		default:
			b.WriteRune(r)
		}
	}
	return strings.ToLower(b.String())
}

// ActionForKey 回傳欄位名的處置方式；未登記的鍵名為 ActionKeep。
//
// request_id 刻意不列在任何清單：它是關聯識別碼而非憑據，日誌與回應標頭都要能看見原值
// （ER-SRS-001 §28「每個響應帶 request_id，便於本地日誌追蹤」）。
func ActionForKey(key string) ValueAction {
	if action, ok := keyActions[normalizeKey(key)]; ok {
		return action
	}
	return ActionKeep
}

// 值形狀規則：沒有鍵名可依據的憑證（自由文字裡的 Bearer 頭、PEM 區塊、JWT⋯）
// 只能靠形狀辨識。清單與前端 redaction.dart 逐條對應，兩邊都實作 §7。
var (
	// pemBlock 是私密金鑰的 PEM 區塊（含標頭與結尾；結尾缺失時一路取到文字結束）。
	pemBlock = regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?(?:-----END [A-Z ]*PRIVATE KEY-----|$)`)
	// uriUserInfo 是 URI 的 userinfo 段（scheme://user:pass@host）。
	// 協定名稱比前端實作放寬：連線字串以外還有 postgres://、mysql://、amqp:// 這類
	// 會把帳密嵌進位址的寫法，驅動報錯時原字帶出，必須一併擋。
	uriUserInfo = regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.\-]{1,15}://[^\s/?#@]*:[^\s/?@]*@`)
	// schemeToken 是 HTTP 認證方案的憑證值。
	schemeToken = regexp.MustCompile(`(?i)\b(Bearer|Basic|Digest|Token)\s+([A-Za-z0-9\-._~+/]{6,}=*)`)
	// jwtLike 是 JWT 的三段 Base64url 形狀（以 eyJ 開頭，即 JSON 表頭的 Base64url）。
	jwtLike = regexp.MustCompile(`\beyJ[A-Za-z0-9_\-]{8,}\.[A-Za-z0-9_\-]{8,}\.[A-Za-z0-9_\-]{4,}`)
	// longHex 是金鑰、雜湊與簽名的常見形狀。
	longHex = regexp.MustCompile(`\b[0-9a-fA-F]{24,}\b`)
	// paddedBase64 是帶結尾填充的 Base64（…=／…== 幾乎只出現在金鑰與簽名裡）。
	paddedBase64 = regexp.MustCompile(`\b[A-Za-z0-9+/]{16,}={1,2}`)
	// longAlnum 是無分隔符的長英數字串；是否打碼另看有無數字（見 redactShapes）。
	// 刻意不含斜線：路徑與堆疊影格都帶斜線，用同一條規則會把排錯線索一起打碼。
	longAlnum = regexp.MustCompile(`\b[A-Za-z0-9]{32,}\b`)
	// pair 比對 key: value、key=value 與 "key": "value" 三種寫法（錨定在搜尋起點）。
	pair = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9_\-]*)["']?\s*[:=]\s*["']?([^\s"',;}\]]+)`)
)

// structuralPathKeys 為「整個值就是一條檔案系統路徑」的鍵名。
//
// 長隨機串那三條規則（24+ 十六進位、帶填充 Base64、32+ 含數字的英數串）對路徑是誤傷：
// Go 的暫時目錄名、帶日期與雜湊的備份目錄名都會被當成金鑰打碼，
// 結果「資料庫開啟失敗」這條記錄反而看不出是哪個目錄。
// 這裡只免除那三條：PEM 區塊、Bearer/Basic、JWT 與 URI 內嵌憑證在路徑裡都不該出現，
// 出現了一樣擋下——免的是誤傷，不是放寬。
//
// 刻意不含 `path`：那是 URL 路徑，QR 令牌這類憑證正是會出現在路徑上的東西。
// 鍵名同樣必須是正規化形狀（小寫、無底線），理由與 keyActions 相同：
// 比對前正規化的是輸入，表裡寫成 data_dir 的話這條規則永遠不會命中。
var structuralPathKeys = map[string]bool{
	"datadir":     true,
	"database":    true,
	"dbpath":      true,
	"media":       true,
	"documents":   true,
	"attachments": true,
	"backups":     true,
	"logsdir":     true,
	"configpath":  true,
	"backuppath":  true,
	"filepath":    true,
	"logfile":     true,
	"logpath":     true,
}

// isStructuralPathKey 回傳鍵名是否屬於檔案系統路徑類。
func isStructuralPathKey(key string) bool {
	return structuralPathKeys[normalizeKey(key)]
}

// RedactText 遮罩自由文字中的憑證形狀並截斷長度，回傳可安全寫入日誌的文字。
//
// 「自由文字」指錯誤訊息、panic 內容、堆疊、客戶端標頭等拿不到欄位名的場合：
// 這裡先假設輸入含有敏感值，逐規則遮罩後才允許離開這層，
// 而不是指望拋出例外的人記得避開。規則順序是先專後泛、最後才成對遮罩（見 redactShapes 註解）。
func RedactText(text string) string {
	return truncate(redactShapes(text, true))
}

// RedactValue 依欄位名處置單個值；未登記的鍵名仍要過值形狀掃描。
func RedactValue(key, value string) string {
	switch ActionForKey(key) {
	case ActionRedact:
		return RedactedValue
	case ActionRef:
		return RefPrefix + referenceOf(value)
	default:
		return truncate(redactShapes(value, !isStructuralPathKey(key)))
	}
}

// redactShapes 逐條套用值形狀規則，順序與前端實作一致：
// 私鑰區塊 → URI 憑證 → 認證方案 → JWT → 鍵值對 → 長隨機串。
//
// 鍵值對排在認證方案與 JWT 之後：這兩條先把「Bearer xxxx」的 xxxxxxxx 打掉的話，
// 後面的成對掃描就看不見完整值了；反過來先成對再形狀則會把已判定安全的鍵值重複處理。
// blobRules 為 false 時跳過最後三條長隨機串規則（只給路徑類鍵名，理由見 structuralPathKeys）。
func redactShapes(text string, blobRules bool) string {
	out := pemBlock.ReplaceAllStringFunc(text, func(string) string { return RedactedValue + "(private-key)" })
	out = uriUserInfo.ReplaceAllStringFunc(out, func(match string) string {
		separator := strings.Index(match, "://")
		if separator < 0 {
			return RedactedValue
		}
		return match[:separator+3] + RedactedValue + "@"
	})
	out = schemeToken.ReplaceAllStringFunc(out, func(match string) string {
		groups := schemeToken.FindStringSubmatch(match)
		return groups[1] + " " + RefPrefix + referenceOf(groups[2])
	})
	out = jwtLike.ReplaceAllStringFunc(out, func(match string) string { return RefPrefix + referenceOf(match) })
	out = redactPairs(out)
	if !blobRules {
		return out
	}
	out = longHex.ReplaceAllStringFunc(out, func(string) string { return MaskedValue })
	out = paddedBase64.ReplaceAllStringFunc(out, func(string) string { return MaskedValue })
	out = longAlnum.ReplaceAllStringFunc(out, func(match string) string {
		// 只要求「夠長 + 含數字」：全大寫的 API 金鑰很常見，要求含小寫會漏擋；
		// 不含數字的長英數串通常是散列值以外的識別碼或程式文字，保留下來才排得動錯。
		if strings.ContainsAny(match, "0123456789") {
			return MaskedValue
		}
		return match
	})
	return out
}

// redactPairs 遮罩鍵值對中的值，鍵名與分隔符原樣保留（日誌仍要判讀得出來是哪個欄位）。
//
// 逐字元錨定掃描而不是 FindAllString：一次匹配會把整個鍵值對一併取走，於是
// 「前一個鍵的值剛好是下一個鍵名」的寫法會讓真正的敏感鍵被當成別鍵的值而漏擋。
// 未登記的鍵名只寫到鍵名結束，讓值的位置有機會被重新識別為下一個鍵。
func redactPairs(text string) string {
	var b strings.Builder
	b.Grow(len(text))
	for cursor := 0; cursor < len(text); {
		rest := text[cursor:]
		matched := pair.FindStringSubmatchIndex(rest)
		if matched == nil {
			_, size := utf8.DecodeRuneInString(rest)
			b.WriteString(rest[:size])
			cursor += size
			continue
		}
		key := rest[matched[2]:matched[3]]
		valueStart, valueEnd := cursor+matched[4], cursor+matched[5]
		switch ActionForKey(key) {
		case ActionRedact:
			b.WriteString(text[cursor:valueStart])
			b.WriteString(RedactedValue)
			cursor = valueEnd
		case ActionRef:
			b.WriteString(text[cursor:valueStart])
			b.WriteString(RefPrefix + referenceOf(text[valueStart:valueEnd]))
			cursor = valueEnd
		default:
			b.WriteString(key)
			cursor += len(key)
		}
	}
	return b.String()
}

// referenceOf 把值壓成 7 位十六進位的短辨識碼。
//
// **這是關聯識別碼，不是安全摘要**：目的只是讓同一個憑證在日誌裡穩定對應同一個標記，
// 而不把原值寫出來。FNV-1a 不是為抗逆像設計的，因此低熵值（例如 4 位數 PIN）
// 一律走 ActionRedact 而非 ActionRef——清單裡把 PIN 歸在「永不記錄」正是這個理由。
func referenceOf(value string) string {
	h := fnv.New64a()
	_, _ = h.Write([]byte(value))
	const digits = 7
	raw := strconv.FormatUint(h.Sum64()&0xFFFFFFF, 16)
	if len(raw) >= digits {
		return raw[len(raw)-digits:]
	}
	return strings.Repeat("0", digits-len(raw)) + raw
}

// truncate 以字元為上限截斷文字（超出時以刪節標記收尾）。
//
// 以 rune 計而非位元組：日誌大量是中文內容，按位元組截斷會切出半個 UTF-8 序列。
func truncate(text string) string {
	if utf8.RuneCountInString(text) <= MaxValueRunes {
		return text
	}
	return string([]rune(text)[:MaxValueRunes]) + "…"
}

// SummarizeStack 取堆疊的前 maxLines 非空行並走同一條脫敏管線。
//
// 堆疊是排錯必要資訊，但完整 Go 堆疊動輒上百行、且每幀都含絕對路徑；
// 保留前段足以定位 panic 點，長度也在單行上限內。
func SummarizeStack(stack string, maxLines int) string {
	lines := strings.Split(stack, "\n")
	kept := make([]string, 0, maxLines)
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if len(kept) == maxLines {
			break
		}
		kept = append(kept, strings.TrimRight(line, "\r"))
	}
	suffix := ""
	if dropped := countNonEmpty(lines) - len(kept); dropped > 0 {
		suffix = " …（另有 " + strconv.Itoa(dropped) + " 行）"
	}
	return RedactText(strings.Join(kept, " ⏎ ") + suffix)
}

// countNonEmpty 回傳非空行數（供 SummarizeStack 計算被省略的行數）。
func countNonEmpty(lines []string) int {
	n := 0
	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			n++
		}
	}
	return n
}
