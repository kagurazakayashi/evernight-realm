// Package idgen 提供全服務唯一的內部標識產生與判定入口。
//
// 內部實體主鍵一律為 UUIDv7，對外（JSON、標頭、日誌）表示固定為 36 字元小寫正規字串
// （規格 §27.1、ADR-013、DEC-003）。業務代碼不得自行拼接標識，也不得直接取用亂數
// 或其他 UUID 套件：產生一律經 New，讀入外部輸入一律經 Parse，
// 使「版本恆為 7、格式恆為正規小寫」成為只需一處維護的協議不變量。
//
// 展示短碼（如 TX-8F2K、玩家編號、物品 #00012）不是內部主鍵：依 DEC-003 由展示層派生，
// 且不得取代主鍵或充當安全憑證，本套件不產生也不驗證短碼。
package idgen

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

// canonicalLen 為協議字串長度：8-4-4-4-12 共 32 個十六進位字元加 4 個連字號。
const canonicalLen = 36

// ID 為內部實體標識：UUIDv7 的強型別包裝。
//
// 刻意定義為 uuid.UUID 的使用者定義型別而非別名，因此不繼承其方法，
// 格式與驗證一律由本套件負責；可複製、可比較、可直接作為 map 的鍵。
// 零值為 Nil，代表「尚未指派」，不得出現在對外輸出或資料庫記錄中。
type ID uuid.UUID

// Nil 為尚未指派的零值標識。
//
// 其版本欄位為 0，故 Parse 必然拒絕——未指派的標識無法被讀回，
// 也就不可能被誤當成有效主鍵帶進業務邏輯。
var Nil = ID{}

// New 產生一個 UUIDv7 標識。
//
// 亂數來源異常時回傳錯誤，不降級為其他版本或格式（DEC-003 要求版本恆為 7）；
// 呼叫端應拒絕該次操作並記錄錯誤，而不是換用別的標識。
//
// 同一進程內產生的標識，其前 8 個位元組嚴格遞增：48 位元為毫秒時間戳，
// 其後的 12 位元（rand_a 欄位）為次毫秒計數。
// 對應到正規字串，即去掉連字號後的前 16 個十六進位字元（第 13 個恆為版本 7）。
func New() (ID, error) {
	u, err := uuid.NewV7()
	if err != nil {
		return Nil, fmt.Errorf("idgen: 產生 UUIDv7 失敗: %w", err)
	}
	return ID(u), nil
}

// Parse 解析外部輸入的標識字串，只接受協議的正規格式。
//
// 拒絕非正規寫法（大寫、花括號、urn:uuid: 前綴、32 字元無連字號）的理由：
// 同一標識若有多種合法寫法，字串比對、快取與唯一索引會出現兩個不同的鍵對應同一實體。
// 版本不為 7 或變體位元不符 RFC 4122 時同樣拒絕，避免把其他來源的 UUID 當成內部主鍵。
func Parse(s string) (ID, error) {
	if len(s) != canonicalLen {
		return Nil, fmt.Errorf("idgen: 標識長度應為 %d 字元，實際 %d", canonicalLen, len(s))
	}
	u, err := uuid.Parse(s)
	if err != nil {
		return Nil, fmt.Errorf("idgen: 標識格式不合法: %w", err)
	}
	if u.String() != s {
		return Nil, errors.New("idgen: 標識須為 36 字元小寫正規格式（大寫、花括號與 urn 前綴皆不接受）")
	}
	if u.Version() != 7 {
		return Nil, fmt.Errorf("idgen: 標識版本應為 UUIDv7，實際 VERSION_%d", u.Version())
	}
	if u.Variant() != uuid.RFC4122 {
		return Nil, errors.New("idgen: 標識變體位元不符合 RFC 4122")
	}
	return ID(u), nil
}

// String 回傳協議表示：36 字元小寫正規字串。
func (id ID) String() string {
	return uuid.UUID(id).String()
}

// IsNil 回傳是否為尚未指派的零值標識。
func (id ID) IsNil() bool {
	return id == Nil
}

// MarshalJSON 以協議字串輸出標識（DEC-003：內部 ID 經 JSON 直傳字串）。
func (id ID) MarshalJSON() ([]byte, error) {
	return json.Marshal(id.String())
}

// UnmarshalJSON 讀入協議字串；非字串、非正規格式或非 UUIDv7 一律回傳錯誤。
func (id *ID) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return fmt.Errorf("idgen: 標識在 JSON 中必須是字串: %w", err)
	}
	parsed, err := Parse(s)
	if err != nil {
		return err
	}
	*id = parsed
	return nil
}
